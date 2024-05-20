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

	"github.com/AdguardTeam/golibs/errors"
	"github.com/AdguardTeam/golibs/log"
	"github.com/AdguardTeam/golibs/netutil"
	"github.com/miekg/dns"
)

// TODO (rafal): nothing to do
// ////////////////////////////////////////////////
// numQueries is used to count the number of queries
var numQueries atomic.Uint64

// numAnswers is used to count the number of answers
var numAnswers atomic.Uint64

// numCacheHits is used to count the number of cache hits
var numCacheHits atomic.Uint64

////////////////////////////////////////////////////

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

// handleDNSRequest processes the context.  The only error it returns is the one
// from the [RequestHandler], or [Resolve] if the [RequestHandler] is not set.
// d is left without a response as the documentation to [BeforeRequestHandler]
// says, and if it's ratelimited.
func (p *Proxy) handleDNSRequest(d *DNSContext) (err error) {
// handleDNSRequest processes the incoming packet bytes and returns with an optional response packet.
func (p *Proxy) handleDNSRequest(d *DNSContext) error {
	// rafal
	p.mylogDNSMessage(d, "req")
	// end rafal

	p.logDNSMessage(d.Req)

	if d.Req.Response {
		log.Debug("dnsproxy: dropping incoming response packet from %s", d.Addr)

		//log.Debug("Dropping incoming Reply packet from %s", d.Addr.String())
		return nil
	}

	ip := d.Addr.Addr()
	d.IsPrivateClient = p.privateNets.Contains(ip)

	if !p.handleBefore(d) {
		return nil
	}

	// ratelimit based on IP only, protects CPU cycles and outbound connections
	//
	// TODO(e.burkov):  Investigate if written above true and move to UDP server
	// implementation?
	if d.Proto == ProtoUDP && p.isRatelimited(ip) {
		log.Debug("dnsproxy: ratelimiting %s based on IP only", d.Addr)

		// Don't reply to ratelimitted clients.
		return nil
	}

	d.Res = p.validateRequest(d)
	if d.Res == nil {
		if p.RequestHandler != nil {
			err = errors.Annotate(p.RequestHandler(p, d), "using request handler: %w")
		} else {
			err = errors.Annotate(p.Resolve(d), "using default request handler: %w")
		}
	}

	// rafal
	p.mylogDNSMessage(d, "res")
	// end rafal
	p.logDNSMessage(d.Res)
	p.respond(d)

	return err
}

// validateRequest returns a response for invalid request or nil if the request
// is ok.
func (p *Proxy) validateRequest(d *DNSContext) (resp *dns.Msg) {
	switch {
	case len(d.Req.Question) != 1:
		log.Debug("dnsproxy: got invalid number of questions: %d", len(d.Req.Question))

		// TODO(e.burkov):  Probably, FORMERR would be a better choice here.
		// Check out RFC.
		return p.messages.NewMsgSERVFAIL(d.Req)
	case p.RefuseAny && d.Req.Question[0].Qtype == dns.TypeANY:
		// Refuse requests of type ANY (anti-DDOS measure).
		log.Debug("dnsproxy: refusing type=ANY request")

		return p.messages.NewMsgNOTIMPLEMENTED(d.Req)
	case p.recDetector.check(d.Req):
		log.Debug("dnsproxy: recursion detected resolving %q", d.Req.Question[0].Name)

		return p.messages.NewMsgNXDOMAIN(d.Req)
	case d.isForbiddenARPA(p.privateNets):
		log.Debug("dnsproxy: %s requests a private arpa domain %q", d.Addr, d.Req.Question[0].Name)

		return p.messages.NewMsgNXDOMAIN(d.Req)
	default:
		return nil
	}
}

// isForbiddenARPA returns true if dctx contains a PTR, SOA, or NS request for
// some private address and client's address is not within the private network.
// Otherwise, it sets [DNSContext.RequestedPrivateRDNS] for future use.
func (dctx *DNSContext) isForbiddenARPA(privateNets netutil.SubnetSet) (ok bool) {
	q := dctx.Req.Question[0]
	switch q.Qtype {
	case dns.TypePTR, dns.TypeSOA, dns.TypeNS:
		// Go on.
		//
		// TODO(e.burkov):  Reconsider the list of types involved to private
		// address space.  Perhaps, use the logic for any type.  See
		// https://www.rfc-editor.org/rfc/rfc6761.html#section-6.1.
	default:
		return false
	}

	requestedPref, err := netutil.ExtractReversedAddr(q.Name)
	if err != nil {
		log.Debug("proxy: parsing reversed subnet: %v", err)

		return false
	}

	if privateNets.Contains(requestedPref.Addr()) {
		dctx.RequestedPrivateRDNS = requestedPref

		return !dctx.IsPrivateClient
	}

	return false
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
			//log.Debug("Override TTL from %d to %d", originalTTL, newTTL)	// rafal
			rr.Header().Ttl = newTTL
		}
	}
}

func (p *Proxy) logDNSMessage(m *dns.Msg) {
	if m == nil {
		return
	}
}

// rafal
// //////////////////////////////////////////////////////////////////////////////
func (p *Proxy) mylogDNSMessage(d *DNSContext, messageType string) {
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
	//////////////////////////////////////////////////////////////////////////////
	// end rafal code
}
