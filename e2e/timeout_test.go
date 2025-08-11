package e2e

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Hard cap all tests in this package to 30s wall time
	timer := time.AfterFunc(30*time.Second, func() {
		fmt.Fprintln(os.Stderr, "global e2e test timeout (30s)")
		os.Exit(2)
	})
	code := m.Run()
	timer.Stop()
	os.Exit(code)
}
