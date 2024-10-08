// Package main is responsible for command-line interface of dnsproxy.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/ameshkov/dnscrypt/v2"
	"github.com/barweiss/go-tuple"
	"github.com/gin-gonic/gin"
	"github.com/go-co-op/gocron"
	"gopkg.in/yaml.v3"
	"net"
	"net/http"
	"runtime/pprof"
	//"net/http/pprof"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	proxynetutil "github.com/AdguardTeam/dnsproxy/internal/netutil"
	"github.com/AdguardTeam/dnsproxy/internal/version"
	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/AdguardTeam/golibs/log"
	"github.com/AdguardTeam/golibs/netutil"
	"github.com/AdguardTeam/golibs/osutil"
	"github.com/AdguardTeam/golibs/timeutil"
	goFlags "github.com/jessevdk/go-flags"
)

// Options represents console arguments.  For further additions, please do not
// use the default option since it will cause some problems when config files
// are used.
//
// TODO(a.garipov): Consider extracting conf blocks for better fieldalignment.
type Options struct {
	// Configuration file path (yaml), the config path should be read without
	// using goFlags in order not to have default values overriding yaml
	// options.
	ConfigPath string `long:"config-path" description:"yaml configuration file. Minimal working configuration in config.yaml.dist. Options passed through command line will override the ones from this file." default:""`

	// LogOutput is the path to the log file.
	LogOutput string `yaml:"output" short:"o" long:"output" description:"Path to the log file. If not set, write to stdout."`

	// TLSCertPath is the path to the .crt with the certificate chain.
	TLSCertPath string `yaml:"tls-crt" short:"c" long:"tls-crt" description:"Path to a file with the certificate chain"`

	// TLSKeyPath is the path to the file with the private key.
	TLSKeyPath string `yaml:"tls-key" short:"k" long:"tls-key" description:"Path to a file with the private key"`

	// HTTPSServerName sets Server header for the HTTPS server.
	HTTPSServerName string `yaml:"https-server-name" long:"https-server-name" description:"Set the Server header for the responses from the HTTPS server." default:"dnsproxy"`

	// HTTPSUserinfo is the sole permitted userinfo for the DoH basic
	// authentication.  If it is set, all DoH queries are required to have this
	// basic authentication information.
	HTTPSUserinfo string `yaml:"https-userinfo" long:"https-userinfo" description:"If set, all DoH queries are required to have this basic authentication information."`

	// DNSCryptConfigPath is the path to the DNSCrypt configuration file.
	DNSCryptConfigPath string `yaml:"dnscrypt-config" short:"g" long:"dnscrypt-config" description:"Path to a file with DNSCrypt configuration. You can generate one using https://github.com/ameshkov/dnscrypt"`

	// EDNSAddr is the custom EDNS Client Address to send.
	EDNSAddr string `yaml:"edns-addr" long:"edns-addr" description:"Send EDNS Client Address"`

	// ListenAddrs is the list of server's listen addresses.
	ListenAddrs []string `yaml:"listen-addrs" short:"l" long:"listen" description:"Listening addresses"`

	// ListenPorts are the ports server listens on.
	ListenPorts []int `yaml:"listen-ports" short:"p" long:"port" description:"Listening ports. Zero value disables TCP and UDP listeners"`

	// HTTPSListenPorts are the ports server listens on for DNS-over-HTTPS.
	HTTPSListenPorts []int `yaml:"https-port" short:"s" long:"https-port" description:"Listening ports for DNS-over-HTTPS"`

	// TLSListenPorts are the ports server listens on for DNS-over-TLS.
	TLSListenPorts []int `yaml:"tls-port" short:"t" long:"tls-port" description:"Listening ports for DNS-over-TLS"`

	// QUICListenPorts are the ports server listens on for DNS-over-QUIC.
	QUICListenPorts []int `yaml:"quic-port" short:"q" long:"quic-port" description:"Listening ports for DNS-over-QUIC"`

	// DNSCryptListenPorts are the ports server listens on for DNSCrypt.
	DNSCryptListenPorts []int `yaml:"dnscrypt-port" short:"y" long:"dnscrypt-port" description:"Listening ports for DNSCrypt"`

	// Upstreams is the list of DNS upstream servers.
	Upstreams []string `yaml:"upstream" short:"u" long:"upstream" description:"An upstream to be used (can be specified multiple times). You can also specify path to a file with the list of servers" optional:"false"`

	// BootstrapDNS is the list of bootstrap DNS upstream servers.
	BootstrapDNS []string `yaml:"bootstrap" short:"b" long:"bootstrap" description:"Bootstrap DNS for DoH and DoT, can be specified multiple times (default: use system-provided)"`

	// Fallbacks is the list of fallback DNS upstream servers.
	Fallbacks []string `yaml:"fallback" short:"f" long:"fallback" description:"Fallback resolvers to use when regular ones are unavailable, can be specified multiple times. You can also specify path to a file with the list of servers"`

	// PrivateRDNSUpstreams are upstreams to use for reverse DNS lookups of
	// private addresses, including the requests for authority records, such as
	// SOA and NS.
	PrivateRDNSUpstreams []string `yaml:"private-rdns-upstream" long:"private-rdns-upstream" description:"Private DNS upstreams to use for reverse DNS lookups of private addresses, can be specified multiple times"`

	// DNS64Prefix defines the DNS64 prefixes that dnsproxy should use when it
	// acts as a DNS64 server.  If not specified, dnsproxy uses the default
	// Well-Known Prefix.  This option can be specified multiple times.
	DNS64Prefix []string `yaml:"dns64-prefix" long:"dns64-prefix" description:"Prefix used to handle DNS64. If not specified, dnsproxy uses the 'Well-Known Prefix' 64:ff9b::.  Can be specified multiple times" required:"false"`

	// PrivateSubnets is the list of private subnets to determine private
	// addresses.
	PrivateSubnets []string `yaml:"private-subnets" long:"private-subnets" description:"Private subnets to use for reverse DNS lookups of private addresses" required:"false"`

	// BogusNXDomain transforms responses that contain at least one of the given
	// IP addresses into NXDOMAIN.
	//
	// TODO(a.garipov): Find a way to use [netutil.Prefix].  Currently, package
	// go-flags doesn't support text unmarshalers.
	BogusNXDomain []string `yaml:"bogus-nxdomain" long:"bogus-nxdomain" description:"Transform the responses containing at least a single IP that matches specified addresses and CIDRs into NXDOMAIN.  Can be specified multiple times."`

	// Timeout for outbound DNS queries to remote upstream servers in a
	// human-readable form.  Default is 10s.
	Timeout timeutil.Duration `yaml:"timeout" long:"timeout" description:"Timeout for outbound DNS queries to remote upstream servers in a human-readable form" default:"10s"`

	// CacheMinTTL is the minimum TTL value for caching DNS entries, in seconds.
	// It overrides the TTL value from the upstream server, if the one is less.
	CacheMinTTL uint32 `yaml:"cache-min-ttl" long:"cache-min-ttl" description:"Minimum TTL value for DNS entries, in seconds. Capped at 3600. Artificially extending TTLs should only be done with careful consideration."`

	// CacheMaxTTL is the maximum TTL value for caching DNS entries, in seconds.
	// It overrides the TTL value from the upstream server, if the one is
	// greater.
	CacheMaxTTL uint32 `yaml:"cache-max-ttl" long:"cache-max-ttl" description:"Maximum TTL value for DNS entries, in seconds."`

	// CacheSizeBytes is the cache size in bytes.  Default is 64k.
	CacheSizeBytes int `yaml:"cache-size" long:"cache-size" description:"Cache size (in bytes). Default: 64k"`

	// Ratelimit is the maximum number of requests per second.
	Ratelimit int `yaml:"ratelimit" short:"r" long:"ratelimit" description:"Ratelimit (requests per second)"`

	// RatelimitSubnetLenIPv4 is a subnet length for IPv4 addresses used for
	// rate limiting requests.
	RatelimitSubnetLenIPv4 int `yaml:"ratelimit-subnet-len-ipv4" long:"ratelimit-subnet-len-ipv4" description:"Ratelimit subnet length for IPv4." default:"24"`

	// RatelimitSubnetLenIPv6 is a subnet length for IPv6 addresses used for
	// rate limiting requests.
	RatelimitSubnetLenIPv6 int `yaml:"ratelimit-subnet-len-ipv6" long:"ratelimit-subnet-len-ipv6" description:"Ratelimit subnet length for IPv6." default:"56"`

	// UDPBufferSize is the size of the UDP buffer in bytes.  A value <= 0 will
	// use the system default.
	UDPBufferSize int `yaml:"udp-buf-size" long:"udp-buf-size" description:"Set the size of the UDP buffer in bytes. A value <= 0 will use the system default."`

	// MaxGoRoutines is the maximum number of goroutines.
	MaxGoRoutines uint `yaml:"max-go-routines" long:"max-go-routines" description:"Set the maximum number of go routines. A zero value will not not set a maximum."`

	// TLSMinVersion is the minimum allowed version of TLS.
	TLSMinVersion float32 `yaml:"tls-min-version" long:"tls-min-version" description:"Minimum TLS version, for example 1.0" optional:"yes"`

	// TLSMaxVersion is the maximum allowed version of TLS.
	TLSMaxVersion float32 `yaml:"tls-max-version" long:"tls-max-version" description:"Maximum TLS version, for example 1.3" optional:"yes"`

	// Pprof defines whether the pprof information needs to be exposed via
	// localhost:6060 or not.
	Pprof bool `yaml:"pprof" long:"pprof" description:"If present, exposes pprof information on localhost:6060." optional:"yes" optional-value:"true"`

	// Version, if true, prints the program version, and exits.
	Version bool `yaml:"version" long:"version" description:"Prints the program version"`

	// rafal code
	///////////////////////////////////////////////////////////////////////////////
	StatsPort int `yaml:"stats_port" long:"stats_port" description:"Port on which to expose statistics." default:"9999"`

	BlockedDomainsLists []string `yaml:"blocked_domains_lists" long:"blocked_domains_lists" description:"The blocked domains list to be used (can be specified multiple times)."`

	DomainsExcludedFromBlockingLists []string `yaml:"domains_excluded_from_blocking" long:"domains_excluded_from_blocking" description:"A list of domains to be excluded from blocking lists (can be specified multiple times)."`

	ExcludedFromCachingLists []string `yaml:"domains_excluded_from_caching" long:"domains_excluded_from_caching" description:"The list of domains to be excluded from caching (can be specified multiple times)."`
	///////////////////////////////////////////////////////////////////////////////
	// end rafal code

	// Verbose controls the verbosity of the output.
	Verbose bool `yaml:"verbose" short:"v" long:"verbose" description:"Verbose output (optional)" optional:"yes" optional-value:"true"`

	// Insecure disables upstream servers TLS certificate verification.
	Insecure bool `yaml:"insecure" long:"insecure" description:"Disable secure TLS certificate validation" optional:"yes" optional-value:"false"`

	// IPv6Disabled makes the server to respond with NODATA to all AAAA queries.
	IPv6Disabled bool `yaml:"ipv6-disabled" long:"ipv6-disabled" description:"If specified, all AAAA requests will be replied with NoError RCode and empty answer" optional:"yes" optional-value:"true"`

	// HTTP3 controls whether HTTP/3 is enabled for this instance of dnsproxy.
	// It enables HTTP/3 support for both the DoH upstreams and the DoH server.
	HTTP3 bool `yaml:"http3" long:"http3" description:"Enable HTTP/3 support" optional:"yes" optional-value:"false"`

	// AllServers makes server to query all configured upstream servers in
	// parallel.
	AllServers bool `yaml:"all-servers" long:"all-servers" description:"If specified, parallel queries to all configured upstream servers are enabled" optional:"yes" optional-value:"true"`

	// FastestAddress controls whether the server should respond to A or AAAA
	// requests only with the fastest IP address detected by ICMP response time
	// or TCP connection time.
	FastestAddress bool `yaml:"fastest-addr" long:"fastest-addr" description:"Respond to A or AAAA requests only with the fastest IP address" optional:"yes" optional-value:"true"`

	// CacheOptimistic, if set to true, enables the optimistic DNS cache. That
	// means that cached results will be served even if their cache TTL has
	// already expired.
	CacheOptimistic bool `yaml:"cache-optimistic" long:"cache-optimistic" description:"If specified, optimistic DNS cache is enabled" optional:"yes" optional-value:"true"`

	// Cache controls whether DNS responses are cached or not.
	Cache bool `yaml:"cache" long:"cache" description:"If specified, DNS cache is enabled" optional:"yes" optional-value:"true"`

	// RefuseAny makes the server to refuse requests of type ANY.
	RefuseAny bool `yaml:"refuse-any" long:"refuse-any" description:"If specified, refuse ANY requests" optional:"yes" optional-value:"true"`

	// EnableEDNSSubnet uses EDNS Client Subnet extension.
	EnableEDNSSubnet bool `yaml:"edns" long:"edns" description:"Use EDNS Client Subnet extension" optional:"yes" optional-value:"true"`

	// DNS64 defines whether DNS64 functionality is enabled or not.
	DNS64 bool `yaml:"dns64" long:"dns64" description:"If specified, dnsproxy will act as a DNS64 server" optional:"yes" optional-value:"true"`

	// UsePrivateRDNS makes the server to use private upstreams for reverse DNS
	// lookups of private addresses, including the requests for authority
	// records, such as SOA and NS.
	UsePrivateRDNS bool `yaml:"use-private-rdns" long:"use-private-rdns" description:"If specified, use private upstreams for reverse DNS lookups of private addresses" optional:"yes" optional-value:"true"`
}

const (
	defaultLocalTimeout = 1 * time.Second
)

func main() {
	options := &Options{}

	for _, arg := range os.Args {
		if arg == "--version" {
			fmt.Printf("dnsproxy version: %s\n", version.Version())

			os.Exit(0)
		}

		// TODO(e.burkov, a.garipov):  Use flag package and remove the manual
		// options parsing.
		//
		// See https://github.com/AdguardTeam/dnsproxy/issues/182.
		if len(arg) > 13 {
			if arg[:13] == "--config-path" {
				fmt.Printf("Path: %s\n", arg[14:])
				b, err := os.ReadFile(arg[14:])
				if err != nil {
					log.Fatalf("failed to read the config file %s: %v", arg[14:], err)
				}
				err = yaml.Unmarshal(b, options)
				if err != nil {
					log.Fatalf("failed to unmarshal the config file %s: %v", arg[14:], err)
				}
			}
		}
	}

	parser := goFlags.NewParser(options, goFlags.Default)
	_, err := parser.Parse()
	if err != nil {
		if flagsErr, ok := err.(*goFlags.Error); ok && flagsErr.Type == goFlags.ErrHelp {
			os.Exit(0)
		}

		os.Exit(1)
	}

	run(options)
}

func run(options *Options) {
	if options.Verbose {
		log.SetLevel(log.DEBUG)
	}
	if options.LogOutput != "" {
		// #nosec G302 -- Trust the file path that is given in the
		// configuration.
		file, err := os.OpenFile(options.LogOutput, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			//log.Fatalf("cannot create a log file: %s", err)
			fmt.Printf("cannot create a log file: %s\n", err)
		}

		defer func() { _ = file.Close() }()
		log.SetOutput(file)
	}

	// rafal code
	runPprof(options)

	log.Info("Starting dnsproxy %s", version.Version())

	// Prepare the proxy server and its configuration.
	conf := createProxyConfig(options)

	dnsProxy, err := proxy.New(conf)
	if err != nil {
		log.Fatalf("creating proxy: %s", err)
	}

	// Add extra handler if needed.
	if options.IPv6Disabled {
		ipv6Configuration := ipv6Configuration{ipv6Disabled: options.IPv6Disabled}
		dnsProxy.RequestHandler = ipv6Configuration.handleDNSRequest
	}

	// rafal code
	///////////////////////////////////////////////////////////////////////////////
	proxy.SM.LoadStats("stats.json")

	dnsProxy.PreferIPv6 = false
	ctx := context.Background()

	err = dnsProxy.Start(ctx)
	if err != nil {
		log.Fatalf("cannot start the DNS proxy due to %s", err)
	}

	for _, domain := range options.DomainsExcludedFromBlockingLists {
		proxy.Edm.AddDomain(domain)
	}

	for _, domain := range options.ExcludedFromCachingLists {
		proxy.Efcm.AddDomain(tuple.New2(domain, ""))
	}

	s := gocron.NewScheduler(time.UTC)
	_, err = s.Every(1).Day().At("02:01").Do(func() { proxy.UpdateBlockedDomains(proxy.Bdm, options.BlockedDomainsLists) })
	if err != nil {
		log.Error("Can't start blocked domains updater.")
	}
	_, err = s.Every(1).Minute().Do(func() { proxy.MonitorLogFile(options.LogOutput) })
	if err != nil {
		log.Error("Can't start log file monitor.")
	}
	_, err = s.Every(1).Hour().Do(func() { proxy.SM.SaveStats("stats.json") })
	if err != nil {
		log.Error("Can't start stats periodic save.")
	}
	_, err = s.Every(1).Day().At("02:15").Do(func() { proxy.SM.SaveStats("stats.json") })
	if err != nil {
		log.Error("Can't start stats periodic save at 02:15.")
	}

	//_, err = s.Every(1).Day().At("02:20").Do(func() { proxy.FinishSignal <- true })
	//if err != nil {
	//	log.Error("Can't start FinishSignal at 02:20.")
	//}
	//err = exec.Command("shutdown", "-h", "02:25").Run()
	//if err != nil {
	//	log.Error("Can't start shutdown at 02:25.")
	//}

	s.StartAsync()
	s.RunAll()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.GET("/stats", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"stats": proxy.SM.GetStats()})
	})
	err = r.Run("0.0.0.0:" + strconv.Itoa(options.StatsPort))
	if err != nil {
		log.Fatalf("cannot start the stats server due to %s", err)
		return
	}

	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGKILL, syscall.SIGSTOP, syscall.SIGSEGV)
	go func() {
		<-c
		log.Info("Shutting down...")
		proxy.SM.SaveStats("stats.json")
	}()
	///////////////////////////////////////////////////////////////////////////////
	// end of rafal code
	<-proxy.FinishSignal

	s.Stop()
	// Stopping the proxy.
	err = dnsProxy.Shutdown(ctx)
	if err != nil {
		log.Fatalf("cannot stop the DNS proxy due to %s", err)
	}
}

// runPprof runs pprof server on localhost:6060 if it's enabled in the options.
func runPprof(options *Options) {
	if !options.Pprof {
		return
	}

	//mux := http.NewServeMux()
	//mux.HandleFunc("/debug/pprof/", pprof.Index)
	//mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	//mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	//mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	//mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	//mux.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
	//mux.Handle("/debug/pprof/block", pprof.Handler("block"))
	//mux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	//mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	//mux.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	//mux.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
	//
	//go func() {
	//	log.Info("pprof: listening on localhost:6060")
	//	srv := &http.Server{
	//		Addr:        "0.0.0.0:6060",
	//		ReadTimeout: 60 * time.Second,
	//		Handler:     mux,
	//	}
	//	err := srv.ListenAndServe()
	//	log.Error("error while running the pprof server: %s", err)
	//}()

	f, err := os.Create("default.pgo")
	if err != nil {
		log.Error("error while creating the pprof file: %s", err)
	}

	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			log.Error("error while closing the pprof file: %s", err)
		}
	}(f)

	if err := pprof.StartCPUProfile(f); err != nil {
		log.Error("error while running the pprof server: %s", err)
	}
	defer pprof.StopCPUProfile()
}

// createProxyConfig creates proxy.Config from the command line arguments
func createProxyConfig(options *Options) (conf *proxy.Config) {
	conf = &proxy.Config{
		RatelimitSubnetLenIPv4: options.RatelimitSubnetLenIPv4,
		RatelimitSubnetLenIPv6: options.RatelimitSubnetLenIPv6,

		Ratelimit:       options.Ratelimit,
		CacheEnabled:    options.Cache,
		CacheSizeBytes:  options.CacheSizeBytes,
		CacheMinTTL:     options.CacheMinTTL,
		CacheMaxTTL:     options.CacheMaxTTL,
		CacheOptimistic: options.CacheOptimistic,
		RefuseAny:       options.RefuseAny,
		HTTP3:           options.HTTP3,
		// TODO(e.burkov):  The following CIDRs are aimed to match any address.
		// This is not quite proper approach to be used by default so think
		// about configuring it.
		TrustedProxies: netutil.SliceSubnetSet{
			netip.MustParsePrefix("0.0.0.0/0"),
			netip.MustParsePrefix("::0/0"),
		},
		EnableEDNSClientSubnet: options.EnableEDNSSubnet,
		UDPBufferSize:          options.UDPBufferSize,
		HTTPSServerName:        options.HTTPSServerName,
		MaxGoroutines:          options.MaxGoRoutines,
		UsePrivateRDNS:         options.UsePrivateRDNS,
		PrivateSubnets:         netutil.SubnetSetFunc(netutil.IsLocallyServed),
	}

	if uiStr := options.HTTPSUserinfo; uiStr != "" {
		user, pass, ok := strings.Cut(uiStr, ":")
		if ok {
			conf.Userinfo = url.UserPassword(user, pass)
		} else {
			conf.Userinfo = url.User(user)
		}
	}

	// TODO(e.burkov):  Make these methods of [Options].
	initUpstreams(conf, options)
	initEDNS(conf, options)
	initBogusNXDomain(conf, options)
	initTLSConfig(conf, options)
	initDNSCryptConfig(conf, options)
	initListenAddrs(conf, options)
	initSubnets(conf, options)

	return conf
}

// isEmpty returns false if uc contains at least a single upstream.  uc must not
// be nil.
//
// TODO(e.burkov):  Think of a better way to validate the config.  Perhaps,
// return an error from [ParseUpstreamsConfig] if no upstreams were initialized.
func isEmpty(uc *proxy.UpstreamConfig) (ok bool) {
	return len(uc.Upstreams) == 0 &&
		len(uc.DomainReservedUpstreams) == 0 &&
		len(uc.SpecifiedDomainUpstreams) == 0
}

// initUpstreams inits upstream-related config
func initUpstreams(config *proxy.Config, options *Options) {
	// Init upstreams

	httpVersions := upstream.DefaultHTTPVersions
	if options.HTTP3 {
		httpVersions = []upstream.HTTPVersion{
			upstream.HTTPVersion3,
			upstream.HTTPVersion2,
			upstream.HTTPVersion11,
		}
	}

	timeout := options.Timeout.Duration
	bootOpts := &upstream.Options{
		HTTPVersions:       httpVersions,
		InsecureSkipVerify: options.Insecure,
		Timeout:            timeout,
	}
	boot, err := initBootstrap(options.BootstrapDNS, bootOpts)
	if err != nil {
		log.Fatalf("error while initializing bootstrap: %s", err)
	}

	upsOpts := &upstream.Options{
		HTTPVersions:       httpVersions,
		InsecureSkipVerify: options.Insecure,
		Bootstrap:          boot,
		Timeout:            timeout,
	}
	upstreams := loadServersList(options.Upstreams)

	config.UpstreamConfig, err = proxy.ParseUpstreamsConfig(upstreams, upsOpts)
	if err != nil {
		log.Fatalf("error while parsing upstreams configuration: %s", err)
	}

	privUpsOpts := &upstream.Options{
		HTTPVersions: httpVersions,
		Bootstrap:    boot,
		Timeout:      min(defaultLocalTimeout, timeout),
	}
	privUpstreams := loadServersList(options.PrivateRDNSUpstreams)

	private, err := proxy.ParseUpstreamsConfig(privUpstreams, privUpsOpts)
	if err != nil {
		log.Fatalf("error while parsing private rdns upstreams configuration: %s", err)
	}
	if !isEmpty(private) {
		config.PrivateRDNSUpstreamConfig = private
	}

	fallbackUpstreams := loadServersList(options.Fallbacks)
	fallbacks, err := proxy.ParseUpstreamsConfig(fallbackUpstreams, upsOpts)
	if err != nil {
		log.Fatalf("error while parsing fallback upstreams configuration: %s", err)
	}

	if !isEmpty(fallbacks) {
		config.Fallbacks = fallbacks
	}

	if options.AllServers {
		config.UpstreamMode = proxy.UModeParallel
	} else if options.FastestAddress {
		config.UpstreamMode = proxy.UModeFastestAddr
	} else {
		config.UpstreamMode = proxy.UModeLoadBalance
	}
}

// initBootstrap initializes the [upstream.Resolver] for bootstrapping upstream
// servers.  It returns the default resolver if no bootstraps were specified.
// The returned resolver will also use system hosts files first.
func initBootstrap(bootstraps []string, opts *upstream.Options) (r upstream.Resolver, err error) {
	var resolvers []upstream.Resolver

	for i, b := range bootstraps {
		var ur *upstream.UpstreamResolver
		ur, err = upstream.NewUpstreamResolver(b, opts)
		if err != nil {
			return nil, fmt.Errorf("creating bootstrap resolver at index %d: %w", i, err)
		}

		resolvers = append(resolvers, upstream.NewCachingResolver(ur))
	}

	switch len(resolvers) {
	case 0:
		etcHosts, hostsErr := upstream.NewDefaultHostsResolver(osutil.RootDirFS())
		if hostsErr != nil {
			log.Error("creating default hosts resolver: %s", hostsErr)

			return net.DefaultResolver, nil
		}

		return upstream.ConsequentResolver{etcHosts, net.DefaultResolver}, nil
	case 1:
		return resolvers[0], nil
	default:
		return upstream.ParallelResolver(resolvers), nil
	}
}

// initEDNS inits EDNS-related config
func initEDNS(config *proxy.Config, options *Options) {
	if options.EDNSAddr != "" {
		if options.EnableEDNSSubnet {
			ednsIP := net.ParseIP(options.EDNSAddr)
			if ednsIP == nil {
				log.Fatalf("cannot parse %s", options.EDNSAddr)
			}
			config.EDNSAddr = ednsIP
		} else {
			log.Printf("--edns-addr=%s need --edns to work", options.EDNSAddr)
		}
	}
}

// initBogusNXDomain inits BogusNXDomain structure
func initBogusNXDomain(config *proxy.Config, options *Options) {
	if len(options.BogusNXDomain) == 0 {
		return
	}

	for i, s := range options.BogusNXDomain {
		p, err := proxynetutil.ParseSubnet(s)
		if err != nil {
			log.Error("parsing bogus nxdomain subnet at index %d: %s", i, err)
		} else {
			config.BogusNXDomain = append(config.BogusNXDomain, p)
		}
	}
}

// initTLSConfig inits the TLS config
func initTLSConfig(config *proxy.Config, options *Options) {
	if options.TLSCertPath != "" && options.TLSKeyPath != "" {
		tlsConfig, err := newTLSConfig(options)
		if err != nil {
			log.Fatalf("failed to load TLS config: %s", err)
		}
		config.TLSConfig = tlsConfig
	}
}

// initDNSCryptConfig inits the DNSCrypt config
func initDNSCryptConfig(config *proxy.Config, options *Options) {
	if options.DNSCryptConfigPath == "" {
		return
	}

	b, err := os.ReadFile(options.DNSCryptConfigPath)
	if err != nil {
		log.Fatalf("failed to read DNSCrypt config %s: %v", options.DNSCryptConfigPath, err)
	}

	rc := &dnscrypt.ResolverConfig{}
	err = yaml.Unmarshal(b, rc)
	if err != nil {
		log.Fatalf("failed to unmarshal DNSCrypt config: %v", err)
	}

	cert, err := rc.CreateCert()
	if err != nil {
		log.Fatalf("failed to create DNSCrypt certificate: %v", err)
	}

	config.DNSCryptResolverCert = cert
	config.DNSCryptProviderName = rc.ProviderName
}

// initListenAddrs inits listen addrs
func initListenAddrs(config *proxy.Config, options *Options) {
	listenIPs := []netip.Addr{}

	if len(options.ListenAddrs) == 0 {
		// If ListenAddrs has not been parsed through config file nor command
		// line we set it to "0.0.0.0".
		options.ListenAddrs = []string{"0.0.0.0"}
	}

	if len(options.ListenPorts) == 0 {
		// If ListenPorts has not been parsed through config file nor command
		// line we set it to 53.
		options.ListenPorts = []int{53}
	}

	for i, a := range options.ListenAddrs {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			log.Fatalf("parsing listen address at index %d: %s", i, a)
		}

		listenIPs = append(listenIPs, ip)
	}

	if len(options.ListenPorts) != 0 && options.ListenPorts[0] != 0 {
		for _, port := range options.ListenPorts {
			for _, ip := range listenIPs {
				p := uint16(port)

				ua := net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, p))
				config.UDPListenAddr = append(config.UDPListenAddr, ua)

				ta := net.TCPAddrFromAddrPort(netip.AddrPortFrom(ip, p))
				config.TCPListenAddr = append(config.TCPListenAddr, ta)
			}
		}
	}

	if config.TLSConfig != nil {
		for _, port := range options.TLSListenPorts {
			for _, ip := range listenIPs {
				a := net.TCPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port)))
				config.TLSListenAddr = append(config.TLSListenAddr, a)
			}
		}

		for _, port := range options.HTTPSListenPorts {
			for _, ip := range listenIPs {
				a := net.TCPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port)))
				config.HTTPSListenAddr = append(config.HTTPSListenAddr, a)
			}
		}

		for _, port := range options.QUICListenPorts {
			for _, ip := range listenIPs {
				a := net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port)))
				config.QUICListenAddr = append(config.QUICListenAddr, a)
			}
		}
	}

	if config.DNSCryptResolverCert != nil && config.DNSCryptProviderName != "" {
		for _, port := range options.DNSCryptListenPorts {
			for _, ip := range listenIPs {
				tcp := net.TCPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port)))
				config.DNSCryptTCPListenAddr = append(config.DNSCryptTCPListenAddr, tcp)

				udp := net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port)))
				config.DNSCryptUDPListenAddr = append(config.DNSCryptUDPListenAddr, udp)
			}
		}
	}
}

// mustParsePrefixes parses prefixes and considers any error as fatal, logging
// it with the entity name.
func mustParsePrefixes(prefixes []string, entity string) (prefs []netip.Prefix) {
	for i, p := range prefixes {
		pref, err := netip.ParsePrefix(p)
		if err != nil {
			log.Fatalf("parsing %s at index %d: %v", entity, i, err)
		}

		prefs = append(prefs, pref)
	}

	return prefs
}

// initSubnets sets the DNS64 configuration into conf.
func initSubnets(conf *proxy.Config, options *Options) {
	if conf.UseDNS64 = options.DNS64; conf.UseDNS64 {
		conf.DNS64Prefs = mustParsePrefixes(options.DNS64Prefix, "dns64 prefix")
	}

	if options.UsePrivateRDNS {
		private := mustParsePrefixes(options.PrivateSubnets, "private subnet")
		if len(private) > 0 {
			conf.PrivateSubnets = netutil.SliceSubnetSet(private)
		}
	}
}

// IPv6 configuration
type ipv6Configuration struct {
	ipv6Disabled bool // If true, all AAAA requests will be replied with NoError RCode and empty answer
}

// handleDNSRequest checks IPv6 configuration for current session before resolve
func (c *ipv6Configuration) handleDNSRequest(p *proxy.Proxy, ctx *proxy.DNSContext) error {
	if proxy.CheckDisabledAAAARequest(ctx, c.ipv6Disabled) {
		return nil
	}

	return p.Resolve(ctx)
}

// NewTLSConfig returns a TLS config that includes a certificate
// Use for server TLS config or when using a client certificate
// If caPath is empty, system CAs will be used
func newTLSConfig(options *Options) (*tls.Config, error) {
	// Set default TLS min/max versions
	tlsMinVersion := tls.VersionTLS10 // Default for crypto/tls
	tlsMaxVersion := tls.VersionTLS13 // Default for crypto/tls
	switch options.TLSMinVersion {
	case 1.1:
		tlsMinVersion = tls.VersionTLS11
	case 1.2:
		tlsMinVersion = tls.VersionTLS12
	case 1.3:
		tlsMinVersion = tls.VersionTLS13
	}
	switch options.TLSMaxVersion {
	case 1.0:
		tlsMaxVersion = tls.VersionTLS10
	case 1.1:
		tlsMaxVersion = tls.VersionTLS11
	case 1.2:
		tlsMaxVersion = tls.VersionTLS12
	}

	cert, err := loadX509KeyPair(options.TLSCertPath, options.TLSKeyPath)
	if err != nil {
		return nil, fmt.Errorf("could not load TLS cert: %s", err)
	}

	// #nosec G402 -- TLS MinVersion is configured by user.
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   uint16(tlsMinVersion),
		MaxVersion:   uint16(tlsMaxVersion),
	}, nil
}

// loadX509KeyPair reads and parses a public/private key pair from a pair of
// files.  The files must contain PEM encoded data.  The certificate file may
// contain intermediate certificates following the leaf certificate to form a
// certificate chain.  On successful return, Certificate.Leaf will be nil
// because the parsed form of the certificate is not retained.
func loadX509KeyPair(certFile, keyFile string) (crt tls.Certificate, err error) {
	// #nosec G304 -- Trust the file path that is given in the configuration.
	certPEMBlock, err := os.ReadFile(certFile)
	if err != nil {
		return tls.Certificate{}, err
	}

	// #nosec G304 -- Trust the file path that is given in the configuration.
	keyPEMBlock, err := os.ReadFile(keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}

	return tls.X509KeyPair(certPEMBlock, keyPEMBlock)
}

// loadServersList loads a list of DNS servers from the specified list.  The
// thing is that the user may specify either a server address or the path to a
// file with a list of addresses.  This method takes care of it, it reads the
// file and loads servers from this file if needed.
func loadServersList(sources []string) []string {
	var servers []string

	for _, source := range sources {
		// #nosec G304 -- Trust the file path that is given in the
		// configuration.
		data, err := os.ReadFile(source)
		if err != nil {
			// Ignore errors, just consider it a server address and not a file.
			servers = append(servers, source)
		}

		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)

			// Ignore comments in the file.
			if line == "" ||
				strings.HasPrefix(line, "!") ||
				strings.HasPrefix(line, "#") {
				continue
			}

			servers = append(servers, line)
		}
	}

	return servers
}
