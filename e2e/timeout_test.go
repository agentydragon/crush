package e2e

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Hard cap all tests in this package by default.
	// For live runs (E2E_LIVE=1), extend to 90s to accommodate network/model latency.
	dur := 30 * time.Second
	if os.Getenv("E2E_LIVE") != "" {
		dur = 90 * time.Second
	}
	timer := time.AfterFunc(dur, func() {
		fmt.Fprintf(os.Stderr, "global e2e test timeout (%s)\n", dur)
		os.Exit(2)
	})
	code := m.Run()
	timer.Stop()
	os.Exit(code)
}
