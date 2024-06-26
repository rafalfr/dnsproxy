// Package proxy implements a DNS proxy that supports all known DNS
// encryption protocols.
package proxy

import (
	"cmp"
	"context"
	"fmt"
	"github.com/AdguardTeam/dnsproxy/utils"
	"github.com/ameshkov/dnscrypt/v2"
	"github.com/quic-go/quic-go"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AdguardTeam/dnsproxy/fastip"
	proxynetutil "github.com/AdguardTeam/dnsproxy/internal/netutil"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/AdguardTeam/golibs/errors"
	"github.com/AdguardTeam/golibs/log"
	"github.com/AdguardTeam/golibs/netutil"
	"github.com/AdguardTeam/golibs/service"
	"github.com/AdguardTeam/golibs/syncutil"
	"github.com/miekg/dns"
	gocache "github.com/patrickmn/go-cache"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/exp/rand"
)

const (
	defaultTimeout   = 10 * time.Second
	minDNSPacketSize = 12 + 5
)

// Proto is the DNS protocol.
type Proto string

// Proto values.
const (
	// ProtoUDP is the plain DNS-over-UDP protocol.
	ProtoUDP Proto = "udp"
	// ProtoTCP is the plain DNS-over-TCP protocol.
	ProtoTCP Proto = "tcp"
	// ProtoTLS is the DNS-over-TLS (DoT) protocol.
	ProtoTLS Proto = "tls"
	// ProtoHTTPS is the DNS-over-HTTPS (DoH) protocol.
	ProtoHTTPS Proto = "https"
	// ProtoQUIC is the DNS-over-QUIC (DoQ) protocol.
	ProtoQUIC Proto = "quic"
	// ProtoDNSCrypt is the DNSCrypt protocol.
	ProtoDNSCrypt Proto = "dnscrypt"
)

// Proxy combines the proxy server state and configuration.  It must not be used
// until initialized with [Proxy.Init].
//
// TODO(a.garipov): Consider extracting conf blocks for better fieldalignment.
type Proxy struct {
	// requestsSema limits the number of simultaneous requests.
	//
	// TODO(a.garipov): Currently we have to pass this exact semaphore to the
	// workers, to prevent races on restart.  In the future we will need a
	// better restarting mechanism that completely prevents such invalid states.
	//
	// See also: https://github.com/AdguardTeam/AdGuardHome/issues/2242.
	requestsSema syncutil.Semaphore

	// privateNets determines if the requested address and the client address
	// are private.
	privateNets netutil.SubnetSet

	// time provides the current time.
	//
	// TODO(e.burkov):  Consider configuring it.
	time clock

	// randSrc provides the source of randomness.
	//
	// TODO(e.burkov):  Consider configuring it.
	randSrc rand.Source

	// messages constructs DNS messages.
	messages MessageConstructor

	// beforeRequestHandler handles the request's context before it is resolved.
	beforeRequestHandler BeforeRequestHandler

	// dnsCryptServer serves DNSCrypt queries.
	dnsCryptServer *dnscrypt.Server

	// ratelimitBuckets is a storage for ratelimiters for individual IPs.
	ratelimitBuckets *gocache.Cache

	// fastestAddr finds the fastest IP address for the resolved domain.
	fastestAddr *fastip.FastestAddr

	// cache is used to cache requests.  It is disabled if nil.
	//
	// TODO(d.kolyshev): Move this cache to [Proxy.UpstreamConfig] field.
	cache *cache

	// shortFlighter is used to resolve the expired cached requests without
	// repetitions.
	shortFlighter *optimisticResolver

	// recDetector detects recursive requests that may appear when resolving
	// requests for private addresses.
	recDetector *recursionDetector

	// bytesPool is a pool of byte slices used to read DNS packets.
	//
	// TODO(e.burkov):  Use [syncutil.Pool].
	bytesPool *sync.Pool

	// udpListen are the listened UDP connections.
	udpListen []*net.UDPConn

	// tcpListen are the listened TCP connections.
	tcpListen []net.Listener

	// tlsListen are the listened TCP connections with TLS.
	tlsListen []net.Listener

	// quicListen are the listened QUIC connections.
	quicListen []*quic.EarlyListener

	// quicConns are UDP connections for all listened QUIC connections.  These
	// should be closed on shutdown, since *quic.EarlyListener doesn't close
	// them.
	quicConns []*net.UDPConn

	// quicTransports are transports for all listened QUIC connections.  These
	// should be closed on shutdown, since *quic.EarlyListener doesn't close
	// them.
	quicTransports []*quic.Transport

	// httpsListen are the listened HTTPS connections.
	httpsListen []net.Listener

	// h3Listen are the listened HTTP/3 connections.
	h3Listen []*quic.EarlyListener

	// httpsServer serves queries received over HTTPS.
	httpsServer *http.Server

	// h3Server serves queries received over HTTP/3.
	h3Server *http3.Server

	// dnsCryptUDPListen are the listened UDP connections for DNSCrypt.
	dnsCryptUDPListen []*net.UDPConn

	// dnsCryptTCPListen are the listened TCP connections for DNSCrypt.
	dnsCryptTCPListen []net.Listener

	// upstreamRTTStats maps the upstream address to its round-trip time
	// statistics.  It's holds the statistics for all upstreams to perform a
	// weighted random selection when using the load balancing mode.
	upstreamRTTStats map[string]upstreamRTTStats

	// dns64Prefs is a set of NAT64 prefixes that are used to detect and
	// construct DNS64 responses.  The DNS64 function is disabled if it is
	// empty.
	dns64Prefs netutil.SliceSubnetSet

	// Config is the proxy configuration.
	//
	// TODO(a.garipov): Remove this embed and create a proper initializer.
	Config

	// udpOOBSize is the size of the out-of-band data for UDP connections.
	udpOOBSize int

	// counter counts message contexts created with [Proxy.newDNSContext].
	counter atomic.Uint64

	// RWMutex protects the whole proxy.
	//
	// TODO(e.burkov):  Find out what exactly it protects and name it properly.
	// Also make it a pointer.
	sync.RWMutex

	// ratelimitLock protects ratelimitBuckets.
	ratelimitLock sync.Mutex

	// rttLock protects upstreamRTTStats.
	//
	// TODO(e.burkov):  Make it a pointer.
	rttLock sync.Mutex

	// started indicates if the proxy has been started.
	started bool
}

// New creates a new Proxy with the specified configuration.  c must not be nil.
//
// TODO(e.burkov):  Cover with tests.
func New(c *Config) (p *Proxy, err error) {
	p = &Proxy{
		Config: *c,
		privateNets: cmp.Or[netutil.SubnetSet](
			c.PrivateSubnets,
			netutil.SubnetSetFunc(netutil.IsLocallyServed),
		),
		beforeRequestHandler: cmp.Or[BeforeRequestHandler](
			c.BeforeRequestHandler,
			noopRequestHandler{},
		),
		upstreamRTTStats: map[string]upstreamRTTStats{},
		rttLock:          sync.Mutex{},
		ratelimitLock:    sync.Mutex{},
		RWMutex:          sync.RWMutex{},
		bytesPool: &sync.Pool{
			New: func() any {
				// 2 bytes may be used to store packet length (see TCP/TLS).
				b := make([]byte, 2+dns.MaxMsgSize)

				return &b
			},
		},
		udpOOBSize: proxynetutil.UDPGetOOBSize(),
		time:       realClock{},
		messages: cmp.Or[MessageConstructor](
			c.MessageConstructor,
			defaultMessageConstructor{},
		),
		recDetector: newRecursionDetector(recursionTTL, cachedRecurrentReqNum),
	}

	// TODO(e.burkov):  Validate config separately and add the contract to the
	// New function.
	err = p.validateConfig()
	if err != nil {
		return nil, err
	}

	// TODO(s.chzhen):  Consider moving to [Proxy.validateConfig].
	err = p.validateBasicAuth()
	if err != nil {
		return nil, fmt.Errorf("basic auth: %w", err)
	}

	p.initCache()

	if p.MaxGoroutines > 0 {
		log.Info("dnsproxy: max goroutines is set to %d", p.MaxGoroutines)

		p.requestsSema = syncutil.NewChanSemaphore(p.MaxGoroutines)
	} else {
		p.requestsSema = syncutil.EmptySemaphore{}
	}

	if p.UpstreamMode == UModeFastestAddr {
		log.Info("dnsproxy: fastest ip is enabled")

		p.fastestAddr = fastip.NewFastestAddr()
		if timeout := p.FastestPingTimeout; timeout > 0 {
			p.fastestAddr.PingWaitTimeout = timeout
		}
	}

	err = p.setupDNS64()
	if err != nil {
		return nil, fmt.Errorf("setting up DNS64: %w", err)
	}

	p.RatelimitWhitelist = slices.Clone(p.RatelimitWhitelist)
	slices.SortFunc(p.RatelimitWhitelist, netip.Addr.Compare)

	return p, nil
}

// Init populates fields of p but does not start listeners.
//
// Deprecated:  Use the [New] function instead.
func (p *Proxy) Init() (err error) {
	// TODO(s.chzhen):  Consider moving to [Proxy.validateConfig].
	err = p.validateBasicAuth()
	if err != nil {
		return fmt.Errorf("basic auth: %w", err)
	}

	p.initCache()

	if p.MaxGoroutines > 0 {
		// rafal
		//log.Info("dnsproxy: max goroutines is set to %d", p.MaxGoroutines)

		p.requestsSema = syncutil.NewChanSemaphore(p.MaxGoroutines)
	} else {
		p.requestsSema = syncutil.EmptySemaphore{}
	}

	p.udpOOBSize = proxynetutil.UDPGetOOBSize()
	p.bytesPool = &sync.Pool{
		New: func() interface{} {
			// 2 bytes may be used to store packet length (see TCP/TLS)
			b := make([]byte, 2+dns.MaxMsgSize)

			return &b
		},
	}

	if p.UpstreamMode == UModeFastestAddr {
		// rafal
		//log.Info("dnsproxy: fastest ip is enabled")

		p.fastestAddr = fastip.NewFastestAddr()
		if timeout := p.FastestPingTimeout; timeout > 0 {
			p.fastestAddr.PingWaitTimeout = timeout
		}
	}

	err = p.setupDNS64()
	if err != nil {
		return fmt.Errorf("setting up DNS64: %w", err)
	}

	p.RatelimitWhitelist = slices.Clone(p.RatelimitWhitelist)
	slices.SortFunc(p.RatelimitWhitelist, netip.Addr.Compare)

	p.time = realClock{}

	return nil
}

// validateBasicAuth validates the basic-auth mode settings if p.Config.Userinfo
// is set.
func (p *Proxy) validateBasicAuth() (err error) {
	conf := p.Config
	if conf.Userinfo == nil {
		return nil
	}

	if len(conf.HTTPSListenAddr) == 0 {
		return errors.Error("no https addrs")
	}

	return nil
}

// type check
var _ service.Interface = (*Proxy)(nil)

// Start implements the [service.Interface] for *Proxy.
func (p *Proxy) Start(ctx context.Context) (err error) {
	log.Info("dnsproxy: starting dns proxy server")

	p.Lock()
	defer p.Unlock()

	if p.started {
		return errors.Error("server has been already started")
	}

	err = p.validateListenAddrs()
	if err != nil {
		// Don't wrap the error since it's informative enough as is.
		return err
	}

	err = p.startListeners(ctx)
	if err != nil {
		return fmt.Errorf("starting listeners: %w", err)
	}

	p.started = true

	return nil
}

// closeAll closes all closers and appends the occurred errors to errs.
func closeAll[C io.Closer](errs []error, closers ...C) (appended []error) {
	for _, c := range closers {
		err := c.Close()
		if err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

// Shutdown implements the [service.Interface] for *Proxy.
//
// TODO(e.burkov):  Use the context.
func (p *Proxy) Shutdown(_ context.Context) (err error) {
	log.Info("dnsproxy: stopping server")

	p.Lock()
	defer p.Unlock()

	if !p.started {
		log.Info("dnsproxy: dns proxy server is not started")

		return nil
	}

	errs := closeAll(nil, p.tcpListen...)
	p.tcpListen = nil

	errs = closeAll(errs, p.udpListen...)
	p.udpListen = nil

	errs = closeAll(errs, p.tlsListen...)
	p.tlsListen = nil

	if p.httpsServer != nil {
		errs = closeAll(errs, p.httpsServer)
		p.httpsServer = nil

		// No need to close these since they're closed by httpsServer.Close().
		p.httpsListen = nil
	}

	if p.h3Server != nil {
		errs = closeAll(errs, p.h3Server)
		p.h3Server = nil
	}

	errs = closeAll(errs, p.h3Listen...)
	p.h3Listen = nil

	errs = closeAll(errs, p.quicListen...)
	p.quicListen = nil

	errs = closeAll(errs, p.quicTransports...)
	p.quicTransports = nil

	errs = closeAll(errs, p.quicConns...)
	p.quicConns = nil

	errs = closeAll(errs, p.dnsCryptUDPListen...)
	p.dnsCryptUDPListen = nil

	errs = closeAll(errs, p.dnsCryptTCPListen...)
	p.dnsCryptTCPListen = nil

	for _, u := range []*UpstreamConfig{
		p.UpstreamConfig,
		p.PrivateRDNSUpstreamConfig,
		p.Fallbacks,
	} {
		if u != nil {
			errs = closeAll(errs, u)
		}
	}

	p.started = false

	log.Println("dnsproxy: stopped dns proxy server")

	if len(errs) > 0 {
		return fmt.Errorf("stopping dns proxy server: %w", errors.Join(errs...))
	}

	return nil
}

// Addrs returns all listen addresses for the specified proto or nil if the proxy does not listen to it.
// proto must be "tcp", "tls", "https", "quic", or "udp"
func (p *Proxy) Addrs(proto Proto) []net.Addr {
	p.RLock()
	defer p.RUnlock()

	var addrs []net.Addr

	switch proto {
	case ProtoTCP:
		for _, l := range p.tcpListen {
			addrs = append(addrs, l.Addr())
		}

	case ProtoTLS:
		for _, l := range p.tlsListen {
			addrs = append(addrs, l.Addr())
		}

	case ProtoHTTPS:
		for _, l := range p.httpsListen {
			addrs = append(addrs, l.Addr())
		}

	case ProtoUDP:
		for _, l := range p.udpListen {
			addrs = append(addrs, l.LocalAddr())
		}

	case ProtoQUIC:
		for _, l := range p.quicListen {
			addrs = append(addrs, l.Addr())
		}

	case ProtoDNSCrypt:
		// Using only UDP addrs here
		// TODO: to do it better we should either do ProtoDNSCryptTCP/ProtoDNSCryptUDP
		// or we should change the configuration so that it was not possible to
		// set different ports for TCP/UDP listeners.
		for _, l := range p.dnsCryptUDPListen {
			addrs = append(addrs, l.LocalAddr())
		}

	default:
		panic("proto must be 'tcp', 'tls', 'https', 'quic', 'dnscrypt' or 'udp'")
	}

	return addrs
}

// Addr returns the first listen address for the specified proto or null if the proxy does not listen to it
// proto must be "tcp", "tls", "https", "quic", or "udp"
func (p *Proxy) Addr(proto Proto) net.Addr {
	p.RLock()
	defer p.RUnlock()
	switch proto {
	case ProtoTCP:
		if len(p.tcpListen) == 0 {
			return nil
		}
		return p.tcpListen[0].Addr()

	case ProtoTLS:
		if len(p.tlsListen) == 0 {
			return nil
		}
		return p.tlsListen[0].Addr()

	case ProtoHTTPS:
		if len(p.httpsListen) == 0 {
			return nil
		}
		return p.httpsListen[0].Addr()

	case ProtoUDP:
		if len(p.udpListen) == 0 {
			return nil
		}
		return p.udpListen[0].LocalAddr()

	case ProtoQUIC:
		if len(p.quicListen) == 0 {
			return nil
		}
		return p.quicListen[0].Addr()

	case ProtoDNSCrypt:
		if len(p.dnsCryptUDPListen) == 0 {
			return nil
		}
		return p.dnsCryptUDPListen[0].LocalAddr()
	default:
		panic("proto must be 'tcp', 'tls', 'https', 'quic', 'dnscrypt' or 'udp'")
	}
}

// selectUpstreams returns the upstreams to use for the specified host.  It
// firstly considers custom upstreams if those aren't empty and then the
// configured ones.  The returned slice may be empty or nil.
func (p *Proxy) selectUpstreams(d *DNSContext) (upstreams []upstream.Upstream, isPrivate bool) {
	q := d.Req.Question[0]
	host := q.Name

	if d.RequestedPrivateRDNS != (netip.Prefix{}) || p.shouldStripDNS64(d.Req) {
		// Use private upstreams.
		private := p.PrivateRDNSUpstreamConfig
		if p.UsePrivateRDNS && d.IsPrivateClient && private != nil {
			// This may only be a PTR, SOA, and NS request.
			upstreams = private.getUpstreamsForDomain(host)
		}

		return upstreams, true
	}

	getUpstreams := (*UpstreamConfig).getUpstreamsForDomain
	if q.Qtype == dns.TypeDS {
		getUpstreams = (*UpstreamConfig).getUpstreamsForDS
	}

	if custom := d.CustomUpstreamConfig; custom != nil {
		// Try to use custom.
		upstreams = getUpstreams(custom.upstream, host)
		if len(upstreams) > 0 {
			return upstreams, false
		}
	}

	// Use configured.
	upstreams = getUpstreams(p.UpstreamConfig, host)

	// TODO (rafal): use random upstream server if flag in configuration set
	//////////////////////////////////////////////////////////////////////////
	if upstreams != nil && len(upstreams) > 0 {
		randomIndex, _ := utils.GetRandomValue(0, int64(len(upstreams)))
		upstreams = upstreams[randomIndex : randomIndex+1]
	}
	////////////////////////////////////////////////////////////////////////

	return upstreams, false
}

// replyFromUpstream tries to resolve the request via configured upstream
// servers.  It returns true if the response actually came from an upstream.
func (p *Proxy) replyFromUpstream(d *DNSContext) (ok bool, err error) {
	req := d.Req

	upstreams, isPrivate := p.selectUpstreams(d)
	if len(upstreams) == 0 {
		d.Res = p.messages.NewMsgNXDOMAIN(req)

		return false, fmt.Errorf("selecting upstream: %w", upstream.ErrNoUpstreams)
	}

	if isPrivate {
		p.recDetector.add(d.Req)
	}

	start := time.Now()
	//src := "upstream"	// rafal

	// Perform the DNS request.
	resp, u, err := p.exchangeUpstreams(req, upstreams)
	if dns64Ups := p.performDNS64(req, resp, upstreams); dns64Ups != nil {
		u = dns64Ups
	} else if p.isBogusNXDomain(resp) {
		log.Debug("dnsproxy: replying from upstream: response contains bogus-nxdomain ip")
		resp = p.messages.NewMsgNXDOMAIN(req)
	}

	if err != nil && !isPrivate && p.Fallbacks != nil {
		log.Debug("dnsproxy: replying from upstream: using fallback due to %s", err)
		if err != nil && p.Fallbacks != nil {
			// rafal
			//log.Debug("proxy: replying from upstream: using fallback due to %s", err)

			// Reset the timer.
			start = time.Now()
			//src = "fallback"	// rafal

			// upstreams mustn't appear empty since they have been validated when
			// creating proxy.
			upstreams = p.Fallbacks.getUpstreamsForDomain(req.Question[0].Name)

			resp, u, err = upstream.ExchangeParallel(upstreams, req)
		}
	}

	if err != nil {
		// rafal
		//log.Debug("proxy: replying from %s: %s", src, err)
	}

	if resp != nil {
		d.QueryDuration = time.Since(start)
		//log.Debug("dnsproxy: replying from %s: rtt is %s", src, d.QueryDuration)
		rtt := time.Since(start)
		// rafal
		//log.Debug("proxy: replying from %s: rtt is %s", src, rtt)

		d.QueryDuration = rtt
	}

	p.handleExchangeResult(d, req, resp, u)

	return resp != nil, err
}

// handleExchangeResult handles the result after the upstream exchange.  It sets
// the response to d and sets the upstream that have resolved the request.  If
// the response is nil, it generates a server failure response.
func (p *Proxy) handleExchangeResult(d *DNSContext, req, resp *dns.Msg, u upstream.Upstream) {
	if resp == nil {
		d.Res = p.messages.NewMsgSERVFAIL(req)
		d.hasEDNS0 = false

		return
	}

	// TODO (rafal): print only if configured
	//log.Info("reply from %s for %s", u.Address(), resp.Question[0].Name)
	d.Upstream = u
	d.Res = resp

	p.setMinMaxTTL(resp)
	if len(req.Question) > 0 && len(resp.Question) == 0 {
		// Explicitly construct the question section since some upstreams may
		// respond with invalidly constructed messages which cause out-of-range
		// panics afterwards.
		//
		// See https://github.com/AdguardTeam/AdGuardHome/issues/3551.
		resp.Question = []dns.Question{req.Question[0]}
	}
}

// addDO adds EDNS0 RR if needed and sets DO bit of msg to true.
func addDO(msg *dns.Msg) {
	if o := msg.IsEdns0(); o != nil {
		if !o.Do() {
			o.SetDo()
		}

		return
	}

	msg.SetEdns0(defaultUDPBufSize, true)
}

// defaultUDPBufSize defines the default size of UDP buffer for EDNS0 RRs.
const defaultUDPBufSize = 2048

// Resolve is the default resolving method used by the DNS proxy to query
// upstream servers.  It expects dctx is filled with the request, the client's
func (p *Proxy) Resolve(dctx *DNSContext) (err error) {
	if p.EnableEDNSClientSubnet {
		dctx.processECS(p.EDNSAddr)
	}

	dctx.calcFlagsAndSize()

	//for _, rr := range dctx.Req.Extra {
	//	if rr.Header().Rrtype == dns.TypeOPT {
	//		opt := rr.(*dns.OPT)
	//		for _, e := range opt.Option {
	//			//log.Info(e.String())
	//		}
	//	}
	//}

	replyFromUpstream := true
	var queryDomain string
	// rafal code
	////////////////////////////////////////////////////////////////////////////////
	for _, rr := range dctx.Req.Question {

		if t := rr.Qtype; t == dns.TypeA || t == dns.TypeAAAA {
			queryDomain = ""
			queryDomain = strings.Trim(rr.Name, "\n ")
			queryDomain = strings.TrimSuffix(rr.Name, ".")
			ok, blockedDomain := Bdm.checkDomain(queryDomain)
			if ok == true {
				if SM.Exists("blocked_domains::blocked_responses") {
					SM.Set("blocked_domains::blocked_responses", SM.Get("blocked_domains::blocked_responses").(uint64)+1)
				} else {
					SM.Set("blocked_domains::blocked_responses", uint64(1))
				}

				listName := Bdm.getDomainListName(blockedDomain)
				if SM.Exists("blocked_domains::domains::" + listName + "::" + queryDomain) {
					SM.Set("blocked_domains::domains::"+listName+"::"+queryDomain, SM.Get("blocked_domains::domains::"+listName+"::"+queryDomain).(uint64)+1)
				} else {
					SM.Set("blocked_domains::domains::"+listName+"::"+queryDomain, uint64(1))
				}

				r := GenEmptyMessage(dctx.Req, dns.RcodeSuccess, retryNoError)
				r.Id = dctx.Req.Id
				if t == dns.TypeA {
					ra := new(dns.A)
					ra.Hdr = dns.RR_Header{Name: queryDomain + ".", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 3600}
					ra.A = net.ParseIP("0.0.0.0")
					r.Answer = make([]dns.RR, 1)
					r.Answer[0] = ra
				} else {
					ra := new(dns.AAAA)
					ra.Hdr = dns.RR_Header{Name: queryDomain + ".", Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 3600}
					ra.AAAA = net.ParseIP("::")
					r.Answer = make([]dns.RR, 1)
					r.Answer[0] = ra
				}
				r.Question = dctx.Req.Question
				dctx.Res = r
				dctx.Upstream = nil
				replyFromUpstream = false
				ok = true
				err = nil
			}
		}
	}
	////////////////////////////////////////////////////////////////////////////////
	// end rafal code

	if replyFromUpstream {
		// Use cache only if it's enabled and the query doesn't use custom upstream.
		// Also don't lookup the cache for responses with DNSSEC checking disabled
		// since only validated responses are cached and those may be not the
		// desired result for user specifying CD flag.
		cacheWorks := p.cacheWorks(dctx)
		if cacheWorks {
			if p.replyFromCache(dctx) {
				// Complete the response from cache.
				dctx.scrub()

				return nil
			}

			// On cache miss request for DNSSEC from the upstream to cache it
			// afterwards.
			addDO(dctx.Req)
		}

		var ok bool
		ok, err = p.replyFromUpstream(dctx)

		// Don't cache the responses having CD flag, just like Dnsmasq does.  It
		// prevents the cache from being poisoned with unvalidated answers which may
		// differ from validated ones.
		//
		// See https://github.com/imp/dnsmasq/blob/770bce967cfc9967273d0acfb3ea018fb7b17522/src/forward.c#L1169-L1172.
		//

		// TODO (rafal)
		////////////////////////////////////////////////////////////////////////////////
		if cacheWorks && ok && !dctx.Res.CheckingDisabled {
			ok, queryDomain = Efcm.checkDomain(queryDomain)
			if !ok {
				// Cache the response with DNSSEC RRs.
				p.cacheResp(dctx)
			}
		}
		///////////////////////////////////////////////////////////////////////////////
	}

	// It is possible that the response is nil if the upstream hasn't been
	// chosen.
	if dctx.Res != nil {
		filterMsg(dctx.Res, dctx.Res, dctx.adBit, dctx.doBit, 0)
	}

	// Complete the response.
	dctx.scrub()

	if p.ResponseHandler != nil {
		p.ResponseHandler(dctx, err)
	}

	return err
}

// cacheWorks returns true if the cache works for the given context.  If not, it
// returns false and logs the reason why.
func (p *Proxy) cacheWorks(dctx *DNSContext) (ok bool) {
	var reason string
	switch {
	case p.cache == nil:
		reason = "disabled"
	case dctx.RequestedPrivateRDNS != netip.Prefix{}:
		// Don't cache the requests intended for local upstream servers, those
		// should be fast enough as is.
		reason = "requested address is private"
	case dctx.CustomUpstreamConfig != nil && dctx.CustomUpstreamConfig.cache == nil:
		// In case of custom upstream cache is not configured, the global proxy
		// cache cannot be used because different upstreams can return different
		// results.
		//
		// See https://github.com/AdguardTeam/dnsproxy/issues/169.
		//
		// TODO(e.burkov):  It probably should be decided after resolve.
		reason = "custom upstreams cache is not configured"
	case dctx.Req.CheckingDisabled:
		reason = "dnssec check disabled"
	default:
		return true
	}

	log.Debug("dnsproxy: cache: %s; not caching", reason)

	return false
}

// processECS adds EDNS Client Subnet data into the request from d.
func (dctx *DNSContext) processECS(cliIP net.IP) {
	if ecs, _ := ecsFromMsg(dctx.Req); ecs != nil {
		if ones, _ := ecs.Mask.Size(); ones != 0 {
			dctx.ReqECS = ecs

			// rafal
			//log.Debug("dnsproxy: passing through ecs: %s", dctx.ReqECS)

			return
		}
	}

	var cliAddr netip.Addr
	if cliIP == nil {
		cliAddr = dctx.Addr.Addr()
		cliIP = cliAddr.AsSlice()
	} else {
		cliAddr, _ = netip.AddrFromSlice(cliIP)
	}

	if !netutil.IsSpecialPurpose(cliAddr) {
		// A Stub Resolver MUST set SCOPE PREFIX-LENGTH to 0.  See RFC 7871
		// Section 6.
		dctx.ReqECS = setECS(dctx.Req, cliIP, 0)

		// rafal
		//log.Debug("dnsproxy: setting ecs: %s", dctx.ReqECS)
	}
}
