package proxy

import (
	"context"
	"fmt"
	"github.com/AdguardTeam/dnsproxy/utils"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/AdguardTeam/golibs/log"
	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

// TODO (rafalfr): nothing to do
var numQueries uint64 = 0
var numAnswers uint64 = 0

// startListeners configures and starts listener loops
func (p *Proxy) startListeners(ctx context.Context) error {
	err := p.createUDPListeners(ctx)
	if err != nil {
		return err
	}

	err = p.createTCPListeners(ctx)
	if err != nil {
		return err
	}

	err = p.createTLSListeners()
	if err != nil {
		return err
	}

	err = p.createHTTPSListeners()
	if err != nil {
		return err
	}

	err = p.createQUICListeners()
	if err != nil {
		return err
	}

	err = p.createDNSCryptListeners()
	if err != nil {
		return err
	}

	for _, l := range p.udpListen {
		go p.udpPacketLoop(l, p.requestGoroutinesSema)
	}

	for _, l := range p.tcpListen {
		go p.tcpPacketLoop(l, ProtoTCP, p.requestGoroutinesSema)
	}

	for _, l := range p.tlsListen {
		go p.tcpPacketLoop(l, ProtoTLS, p.requestGoroutinesSema)
	}

	for _, l := range p.httpsListen {
		go func(l net.Listener) { _ = p.httpsServer.Serve(l) }(l)
	}

	for _, l := range p.h3Listen {
		go func(l *quic.EarlyListener) { _ = p.h3Server.ServeListener(l) }(l)
	}

	for _, l := range p.quicListen {
		go p.quicPacketLoop(l, p.requestGoroutinesSema)
	}

	for _, l := range p.dnsCryptUDPListen {
		go func(l *net.UDPConn) { _ = p.dnsCryptServer.ServeUDP(l) }(l)
	}

	for _, l := range p.dnsCryptTCPListen {
		go func(l net.Listener) { _ = p.dnsCryptServer.ServeTCP(l) }(l)
	}

	return nil
}

// handleDNSRequest processes the incoming packet bytes and returns with an optional response packet.
func (p *Proxy) handleDNSRequest(d *DNSContext) error {
	//d.StartTime = time.Now()
	p.logDNSMessage(d, "req")

	if d.Req.Response {
		//log.Debug("Dropping incoming Reply packet from %s", d.Addr.String())
		return nil
	}

	if p.BeforeRequestHandler != nil {
		ok, err := p.BeforeRequestHandler(p, d)
		if err != nil {
			//log.Error("Error in the BeforeRequestHandler: %s", err)
			d.Res = p.genServerFailure(d.Req)
			p.respond(d)
			return nil
		}
		if !ok {
			return nil // do nothing, don't reply
		}
	}

	// ratelimit based on IP only, protects CPU cycles and outbound connections
	if d.Proto == ProtoUDP && p.isRatelimited(d.Addr) {
		//log.Tracef("Ratelimiting %v based on IP only", d.Addr)
		return nil // do nothing, don't reply, we got ratelimited
	}

	if len(d.Req.Question) != 1 {
		//log.Debug("got invalid number of questions: %v", len(d.Req.Question))
		d.Res = p.genServerFailure(d.Req)
	}

	// refuse ANY requests (anti-DDOS measure)
	if p.RefuseAny && len(d.Req.Question) > 0 && d.Req.Question[0].Qtype == dns.TypeANY {
		log.Tracef("Refusing type=ANY request")
		d.Res = p.genNotImpl(d.Req)
	}

	var err error

	if d.Res == nil {
		if len(p.UpstreamConfig.Upstreams) == 0 {
			panic("SHOULD NOT HAPPEN: no default upstreams specified")
		}

		// execute the DNS request
		// if there is a custom middleware configured, use it
		if p.RequestHandler != nil {
			err = p.RequestHandler(p, d)
		} else {
			err = p.Resolve(d)
		}

		if err != nil {
			err = fmt.Errorf("talking to dns upstream: %w", err)
		}
	}

	p.logDNSMessage(d, "res")
	p.respond(d)

	return err
}

// respond writes the specified response to the client (or does nothing if d.Res is empty)
func (p *Proxy) respond(d *DNSContext) {
	// d.Conn can be nil in the case of a DoH request.
	if d.Conn != nil {
		_ = d.Conn.SetWriteDeadline(time.Now().Add(defaultTimeout))
	}

	var err error

	switch d.Proto {
	case ProtoUDP:
		err = p.respondUDP(d)
	case ProtoTCP:
		err = p.respondTCP(d)
	case ProtoTLS:
		err = p.respondTCP(d)
	case ProtoHTTPS:
		err = p.respondHTTPS(d)
	case ProtoQUIC:
		err = p.respondQUIC(d)
	case ProtoDNSCrypt:
		err = p.respondDNSCrypt(d)
	default:
		err = fmt.Errorf("SHOULD NOT HAPPEN - unknown protocol: %s", d.Proto)
	}

	if err != nil {
		logWithNonCrit(err, fmt.Sprintf("responding %s request", d.Proto))
	}
}

// Set TTL value of all records according to our settings
func (p *Proxy) setMinMaxTTL(r *dns.Msg) {
	for _, rr := range r.Answer {
		originalTTL := rr.Header().Ttl
		newTTL := respectTTLOverrides(originalTTL, p.CacheMinTTL, p.CacheMaxTTL)

		if originalTTL != newTTL {
			//log.Debug("Override TTL from %d to %d", originalTTL, newTTL)
			rr.Header().Ttl = newTTL
		}
	}
}

func (p *Proxy) genServerFailure(request *dns.Msg) *dns.Msg {
	return p.genWithRCode(request, dns.RcodeServerFailure)
}

func (p *Proxy) genNotImpl(request *dns.Msg) (resp *dns.Msg) {
	resp = p.genWithRCode(request, dns.RcodeNotImplemented)
	// NOTIMPL without EDNS is treated as 'we don't support EDNS', so
	// explicitly set it.
	resp.SetEdns0(1452, false)

	return resp
}

func (p *Proxy) genWithRCode(req *dns.Msg, code int) (resp *dns.Msg) {
	resp = &dns.Msg{}
	resp.SetRcode(req, code)
	resp.RecursionAvailable = true

	return resp
}

func (p *Proxy) logDNSMessage(d *DNSContext, messageType string) {

	var m *dns.Msg
	if messageType == "req" {
		m = d.Req
	}
	if messageType == "res" {
		m = d.Res
	}

	if m == nil {
		return
	}

	// TODO (rafalfr): nothing to do
	if m.Response {
		if len(m.Answer) > 0 {
			numAnswers++
			answerDomain := strings.Trim(m.Answer[0].Header().Name, " \n\t")
			ipAddress := ""
			for _, answer := range m.Answer {
				if answer.Header().Rrtype == dns.TypeA {
					ipAddress = answer.(*dns.A).A.String()
					break
				} else if answer.Header().Rrtype == dns.TypeAAAA {
					ipAddress = answer.(*dns.AAAA).AAAA.String()
					break
				}
			}
			ipAddress = strings.Trim(ipAddress, " \n\t")
			if d.Upstream != nil {
				upstreamAddress := d.Upstream.Address()
				u, err := url.Parse(upstreamAddress)
				upstreamHost := ""
				if err == nil {
					upstreamHost = u.Host
				}
				upstreamHost = strings.Trim(upstreamHost, " \n\t")
				message := fmt.Sprintf("A#%-10d%-40.39s%-25.25s from %-50.50s\n", numAnswers, answerDomain, ipAddress, utils.ShortText(upstreamHost, 50))
				_, err = log.Writer().Write([]byte(message))
				if err != nil {
					return
				}
			} else {
				NumCacheHits++
				message := fmt.Sprintf("A#%-10d%-40.39s%-25.25s from cache (#%d)\n", numAnswers, answerDomain, ipAddress, NumCacheHits)
				_, err := log.Writer().Write([]byte(message))
				if err != nil {
					return
				}
			}
		}
	} else {
		if len(m.Question) > 0 {
			numQueries++
			sourceAddress := d.Addr.String()
			message := fmt.Sprintf("Q#%-10d%-65.65s from %-30.30s\n", numQueries, m.Question[0].Name, sourceAddress)
			_, err := log.Writer().Write([]byte(message))
			if err != nil {
				return
			}
		}
	}
}
