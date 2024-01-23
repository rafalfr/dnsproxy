package proxy

import (
	"context"
	"fmt"
	"github.com/AdguardTeam/dnsproxy/utils"
	"github.com/quic-go/quic-go"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/AdguardTeam/golibs/log"
	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

// TODO (rafalfr): nothing to do
// numQueries is used to count the number of queries
var numQueries atomic.Uint64

// numAnswers is used to count the number of answers
var numAnswers atomic.Uint64

// numCacheHits is used to count the number of cache hits
var numCacheHits atomic.Uint64

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
		go p.udpPacketLoop(l, p.requestsSema)
	}

	for _, l := range p.tcpListen {
		go p.tcpPacketLoop(l, ProtoTCP, p.requestsSema)
	}

	for _, l := range p.tlsListen {
		go p.tcpPacketLoop(l, ProtoTLS, p.requestsSema)
	}

	for _, l := range p.httpsListen {
		go func(l net.Listener) { _ = p.httpsServer.Serve(l) }(l)
	}

	for _, l := range p.h3Listen {
		go func(l *quic.EarlyListener) { _ = p.h3Server.ServeListener(l) }(l)
	}

	for _, l := range p.quicListen {
		go p.quicPacketLoop(l, p.requestsSema)
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
	p.logDNSMessage(d.Req)

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
	if d.Proto == ProtoUDP && p.isRatelimited(d.Addr.Addr()) {
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

	p.logDNSMessage(d.Res)
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

func (p *Proxy) logDNSMessage(m *dns.Msg) {
	if m == nil {
		return
	}

	// rafalfr code
	if m.Response {
		if len(m.Answer) > 0 {
			numAnswers.Add(1)
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
				message := fmt.Sprintf("A#%-10d%-50.49s%-25.25s from %-50.50s\n", numAnswers.Load(), answerDomain, ipAddress, utils.ShortText(upstreamHost, 50))
				if SM.Exists("resolvers::" + upstreamHost) {
					SM.Set("resolvers::"+upstreamHost, SM.Get("resolvers::"+upstreamHost).(uint64)+1)
				} else {
					SM.Set("resolvers::"+upstreamHost, uint64(1))
				}
				_, err = log.Writer().Write([]byte(message))
				if err != nil {
					return
				}
			} else {
				numCacheHits.Add(1)
				if SM.Exists("local::num_cache_and_blocked_responses") {
					SM.Set("local::num_cache_and_blocked_responses", SM.Get("local::num_cache_and_blocked_responses").(uint64)+1)
				} else {
					SM.Set("local::num_cache_and_blocked_responses", uint64(1))
				}
				message := fmt.Sprintf("A#%-10d%-50.49s%-25.25s from cache (#%d)\n", numAnswers.Load(), answerDomain, ipAddress, numCacheHits.Load())
				_, err := log.Writer().Write([]byte(message))
				if err != nil {
					return
				}
			}
		}
	} else {
		if len(m.Question) > 0 {
			numQueries.Add(1)
			sourceAddress := d.Addr.String()
			message := fmt.Sprintf("Q#%-10d%-75.75s from %-30.30s\n", numQueries.Load(), m.Question[0].Name, sourceAddress)
			_, err := log.Writer().Write([]byte(message))
			if err != nil {
				return
			}
		}
	}
	// end rafalfr code
}
