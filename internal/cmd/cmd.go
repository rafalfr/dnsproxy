package cmd

// Package cmd is the dnsproxy CLI entry point.

import (
	"context"
	"fmt"
	"github.com/AdguardTeam/golibs/log"
	"github.com/barweiss/go-tuple"
	"github.com/gin-gonic/gin"
	"github.com/go-co-op/gocron"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/AdguardTeam/dnsproxy/internal/version"
	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/golibs/errors"
	"github.com/AdguardTeam/golibs/logutil/slogutil"
	"github.com/AdguardTeam/golibs/osutil"
)

// Main is the entrypoint of dnsproxy CLI.  Main may accept arguments, such as
// embedded assets and command-line arguments.
func Main() {
	conf, exitCode, err := parseConfig()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, fmt.Errorf("parsing options: %w", err))
	}

	if conf == nil {
		os.Exit(exitCode)
	}

	logOutput := os.Stdout
	if conf.LogOutput != "" {
		// #nosec G302 -- Trust the file path that is given in the
		// configuration.
		logOutput, err = os.OpenFile(conf.LogOutput, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, fmt.Errorf("cannot create a log file: %s", err))

			os.Exit(osutil.ExitCodeArgumentError)
		}

		defer func() { _ = logOutput.Close() }()
	}

	lvl := slog.LevelInfo
	if conf.Verbose {
		lvl = slog.LevelDebug
	}

	//l := slog.New(th)
	l := slogutil.New(&slogutil.Config{
		Output: logOutput,
		Format: slogutil.FormatDefault,
		Level:  lvl,
		// TODO(d.kolyshev): Consider making configurable.
		AddTimestamp: false, // rafal code
	})
	l.InfoContext(context.Background(), "dnsproxy starting", "version", version.Version())

	ctx := context.Background()

	if conf.Pprof {
		runPprof(l)
	}

	err = runProxy(ctx, l, conf)
	if err != nil {
		l.ErrorContext(ctx, "running dnsproxy", slogutil.KeyError, err)

		// As defers are skipped in case of os.Exit, close logOutput manually.
		//
		// TODO(a.garipov): Consider making logger.Close method.
		if logOutput != os.Stdout {
			_ = logOutput.Close()
		}

		os.Exit(osutil.ExitCodeFailure)
	}
}

// runProxy starts and runs the proxy.  l must not be nil.
//
// TODO(e.burkov):  Move into separate dnssvc package.
func runProxy(ctx context.Context, l *slog.Logger, conf *configuration) (err error) {
	var (
		buildVersion = version.Version()
		revision     = version.Revision()
		branch       = version.Branch()
		commitTime   = version.CommitTime()
	)

	l.InfoContext(
		ctx,
		"dnsproxy starting",
		"version", buildVersion,
		"revision", revision,
		"branch", branch,
		"commit_time", commitTime,
	)

	// Prepare the proxy server and its configuration.
	proxyConf, err := createProxyConfig(ctx, l, conf)
	if err != nil {
		return fmt.Errorf("configuring proxy: %w", err)
	}

	dnsProxy, err := proxy.New(proxyConf)
	if err != nil {
		return fmt.Errorf("creating proxy: %w", err)
	}

	// Start the proxy server.
	err = dnsProxy.Start(ctx)
	if err != nil {
		return fmt.Errorf("starting dnsproxy: %w", err)
	}

	// rafal code
	///////////////////////////////////////////////////////////////////////////////
	proxy.SM.LoadStats("stats.json")

	dnsProxy.PreferIPv6 = true
	getGatewayIPs()

	for _, domain := range conf.DomainsExcludedFromBlockingLists {
		proxy.Edm.AddDomain(domain)
	}

	for _, domain := range conf.ExcludedFromCachingLists {
		proxy.Efcm.AddDomain(tuple.New2(domain, ""))
	}

	s := gocron.NewScheduler(time.UTC)
	_, err = s.Every(1).Day().At("02:01").Do(func() { proxy.UpdateBlockedDomains(proxy.Bdm, conf.BlockedDomainsLists) })
	if err != nil {
		log.Error("Can't start blocked domains updater.")
	}
	_, err = s.Every(1).Minute().Do(func() { proxy.MonitorLogFile(conf.LogOutput) })
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
	_, err = s.Every(1).Hour().Do(func() { getGatewayIPs() })
	if err != nil {
		log.Error("Can't start getGatewayIPs.")
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
	err = r.Run("0.0.0.0:" + strconv.Itoa(conf.StatsPort))
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

	// TODO(e.burkov):  Use [service.SignalHandler].
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGINT, syscall.SIGTERM)
	<-signalChannel

	// Stopping the proxy.
	err = dnsProxy.Shutdown(ctx)
	if err != nil {
		return fmt.Errorf("stopping dnsproxy: %w", err)
	}

	return nil
}

// runPprof runs pprof server on localhost:6060.
//
// TODO(e.burkov):  Use [httputil.RoutePprof].
func runPprof(l *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
	mux.Handle("/debug/pprof/block", pprof.Handler("block"))
	mux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	mux.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))

	go func() {
		// TODO(d.kolyshev): Consider making configurable.
		pprofAddr := "localhost:6060"
		l.Info("starting pprof", "addr", pprofAddr)

		srv := &http.Server{
			Addr:        pprofAddr,
			ReadTimeout: 60 * time.Second,
			Handler:     mux,
		}

		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.Error("pprof failed to listen %v", "addr", pprofAddr, slogutil.KeyError, err)
		}
	}()
}

// rafal code
// getGatewayIPs runs the `ip route get` command for the IPv4 and IPv6 address
// families to determine the gateway IP addresses of the system.  It is called
// by the `main` function.
func getGatewayIPs() {

	out, err := exec.Command("/bin/ip", "route", "get", "1.1.1.1").Output()
	if err != nil {
		proxy.GatewayIPv4 = ""
	} else {
		parts := strings.Split(string(out), " ")
		if len(parts) > 6 {
			ip := strings.Trim(parts[2], " \n")
			if net.ParseIP(ip) != nil {
				proxy.GatewayIPv4 = net.ParseIP(ip).String()
			} else {
				proxy.GatewayIPv4 = ""
			}
		} else {
			proxy.GatewayIPv4 = ""
		}
	}

	out, err = exec.Command("/bin/ip", "route", "get", "2620:fe::fe").Output()
	if err != nil {
		proxy.GatewayIPv6 = ""
	} else {
		parts := strings.Split(string(out), " ")
		if len(parts) > 6 {
			ip := strings.Trim(parts[4], " \n")
			interfaceName := strings.Trim(parts[6], " \n")
			if net.ParseIP(ip) != nil {
				proxy.GatewayIPv6 = net.ParseIP(ip).String() + "%" + interfaceName
			} else {
				proxy.GatewayIPv6 = ""
			}
		} else {
			proxy.GatewayIPv6 = ""
		}
	}
}

// end of rafal code
