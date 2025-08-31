package profile

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	_ "net/http/pprof" // register pprof handlers on default mux
)

var started atomic.Bool

// MaybeStart starts the pprof HTTP server if either debug is true or CRUSH_PROFILE is set.
// It respects CRUSH_PPROF_PORT and CRUSH_PROFILE port overrides. Safe to call multiple times.
func MaybeStart(debug bool) {
	if Addr() != "" {
		return
	}
	profileEnv := strings.TrimSpace(os.Getenv("CRUSH_PROFILE"))
	if !debug && profileEnv == "" {
		return
	}
	if !started.CompareAndSwap(false, true) {
		return
	}
	go func() {
		port := 6060
		if pprofPortEnv := strings.TrimSpace(os.Getenv("CRUSH_PPROF_PORT")); pprofPortEnv != "" {
			if pprofPortEnv == "auto" || pprofPortEnv == "0" {
				port = 0
			} else if n, err := strconv.Atoi(pprofPortEnv); err == nil && n >= 0 && n <= 65535 {
				port = n
			}
		} else if n, err := strconv.Atoi(profileEnv); err == nil && n > 0 && n <= 65535 {
			// Allow CRUSH_PROFILE to directly carry the port when numeric
			port = n
		}

		addr := fmt.Sprintf("localhost:%d", port)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			if !errors.Is(err, syscall.EADDRINUSE) {
				slog.Error("Failed to start pprof listener", "addr", addr, "error", err)
				return
			}
			l, err = net.Listen("tcp", "localhost:0")
			if err != nil {
				slog.Error("Failed to start pprof listener on ephemeral port", "error", err)
				return
			}
			slog.Warn("pprof port in use, falling back to ephemeral", "wanted", addr, "actual", l.Addr().String())
		} else {
			slog.Info("Serving pprof", "addr", l.Addr().String())
		}
		SetAddr(l.Addr().String())
		if serveErr := http.Serve(l, nil); serveErr != nil {
			slog.Error("pprof server exited", "error", serveErr)
		}
	}()
}
