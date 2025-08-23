package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"gopkg.in/natefinch/lumberjack.v2"
)

type wireLogger struct {
	lj   *lumberjack.Logger
	mu   sync.Mutex
	path string
}

// CurrentWireLogPath exposes the current provider wire log path for diagnostics.
func CurrentWireLogPath() string {
	w := getWireLogger()
	return w.path
}

type wireEntry struct {
	TS        string         `json:"ts"`
	Provider  string         `json:"provider"`
	Model     string         `json:"model"`
	Direction string         `json:"direction"`
	EventType string         `json:"event_type,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	MessageID string         `json:"message_id,omitempty"`
	Attempt   int            `json:"attempt,omitempty"`
	Payload   any            `json:"payload,omitempty"`
	Error     string         `json:"error,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// getWireLogger returns a new logger instance each call based on the current cfg path.
// TODO(mpokorny): Consider pooling if perf becomes a concern; tests require per-run isolation.
func getWireLogger() *wireLogger {
	cfg := config.Get()
	providerDir := filepath.Join(cfg.Options.DataDirectory, "logs", "provider")
	_ = os.MkdirAll(providerDir, 0o755)
	path := filepath.Join(providerDir, "provider-wire.log")
	maxSize := 250
	maxBackups := 10
	maxAge := 30
	compress := true
	if cfg.Options != nil && cfg.Options.Wire != nil {
		if cfg.Options.Wire.MaxSizeMB > 0 {
			maxSize = cfg.Options.Wire.MaxSizeMB
		}
		if cfg.Options.Wire.MaxBackups > 0 {
			maxBackups = cfg.Options.Wire.MaxBackups
		}
		if cfg.Options.Wire.MaxAgeDays > 0 {
			maxAge = cfg.Options.Wire.MaxAgeDays
		}
		if cfg.Options.Wire.Compress != nil {
			compress = *cfg.Options.Wire.Compress
		}
	}
	return &wireLogger{path: path, lj: &lumberjack.Logger{Filename: path, MaxSize: maxSize, MaxBackups: maxBackups, MaxAge: maxAge, Compress: compress}}
}

func (w *wireLogger) logJSONL(e wireEntry) {
	b, _ := json.Marshal(e)
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.lj.Write(append(b, '\n'))
}

func wireEnabled() bool {
	cfg := config.Get()
	return cfg.Options != nil && cfg.Options.DebugProviderWire
}

func wireNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }
