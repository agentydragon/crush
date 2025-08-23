package logging

import (
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"time"
)

// RecoverPanic logs and writes a panic snapshot, then runs optional cleanup.
func RecoverPanic(name string, cleanup func()) {
	if r := recover(); r != nil {
		slog.Error("panic", "where", name, "error", r)
		timestamp := time.Now().Format("20060102-150405")
		filename := fmt.Sprintf("crush-panic-%s-%s.log", name, timestamp)
		if file, err := os.Create(filename); err == nil {
			defer file.Close()
			fmt.Fprintf(file, "Panic in %s: %v\n\n", name, r)
			fmt.Fprintf(file, "Time: %s\n\n", time.Now().Format(time.RFC3339))
			fmt.Fprintf(file, "Stack Trace:\n%s\n", debug.Stack())
		}
		if cleanup != nil { cleanup() }
	}
}
