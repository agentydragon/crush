package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"

	_ "net/http/pprof" // profiling

	_ "github.com/joho/godotenv/autoload" // automatically load .env files

	"github.com/charmbracelet/crush/internal/cmd"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/profile"
)

func main() {
	defer log.RecoverPanic("main", func() {
		slog.Error("Application terminated due to unhandled panic")
	})

	if v := os.Getenv("CRUSH_PROFILE"); v != "" {
		go func() {
			port := 6060
			if pstr := strings.TrimSpace(os.Getenv("CRUSH_PPROF_PORT")); pstr != "" {
				if pstr == "auto" || pstr == "0" {
					port = 0
				} else if n, err := strconv.Atoi(pstr); err == nil && n >= 0 && n <= 65535 {
					port = n
				}
			} else if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
				// Allow CRUSH_PROFILE to directly carry the port when numeric
				port = n
			}

			addr := fmt.Sprintf("localhost:%d", port)
			l, err := net.Listen("tcp", addr)
			if err != nil {
				// If the chosen port is busy, fall back to ephemeral
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
			profile.SetAddr(l.Addr().String())
			if serveErr := http.Serve(l, nil); serveErr != nil {
				slog.Error("pprof server exited", "error", serveErr)
			}
		}()
	}

	cmd.Execute()
}
