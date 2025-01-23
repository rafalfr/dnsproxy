package main

// https://stackoverflow.com/questions/74371890/post-context-deadline-exceeded-client-timeout-exceeded-while-awaiting-headers
// rafal code
// key places in the code
// proxy.go: Resolve
// cmd.go: runProxy
// server.go: logDNSMessage
// cache.go: const optimisticTTL, const defaultCacheSize
// config.go: additional parameters
// end of rafal code
// finish parked domains hosting

import (
	"github.com/AdguardTeam/dnsproxy/internal/cmd"
)

func main() {
	cmd.Main()
}
