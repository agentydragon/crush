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

var (
	wireInst *wireLogger
)

func getWireLogger() *wireLogger {
	cfg := config.Get()
	dir := filepath.Join(cfg.Options.DataDirectory, "logs")
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "provider-wire.log")
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
	if wireInst == nil || wireInst.path != path {
		wireInst = &wireLogger{
			path: path,
			lj: &lumberjack.Logger{
				Filename:   path,
				MaxSize:    maxSize,
				MaxBackups: maxBackups,
				MaxAge:     maxAge,
				Compress:   compress,
			},
		}
	}
	return wireInst
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
