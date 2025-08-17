package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"gopkg.in/natefinch/lumberjack.v2"
)

type mcpWireLogger struct {
	name string
	lj   *lumberjack.Logger
	mu   sync.Mutex
}

var (
	mcpLoggersMu sync.RWMutex
	mcpLoggers   = map[string]*mcpWireLogger{}
)

func perMCPLogger(mcp string) *mcpWireLogger {
	mcpLoggersMu.RLock()
	l := mcpLoggers[mcp]
	mcpLoggersMu.RUnlock()
	if l != nil {
		return l
	}
	mcpLoggersMu.Lock()
	defer mcpLoggersMu.Unlock()
	if l = mcpLoggers[mcp]; l != nil {
		return l
	}
	l = newMCPLogger(mcp)
	mcpLoggers[mcp] = l
	return l
}

type mcpWireEntry struct {
	TS        string         `json:"ts"`
	UnixMS    int64          `json:"unix_ms"`
	Direction string         `json:"direction,omitempty"`
	MCP       string         `json:"mcp"`
	Tool      string         `json:"tool,omitempty"`
	ToolCall  string         `json:"tool_call_id,omitempty"`
	Payload   any            `json:"payload,omitempty"`
	Error     string         `json:"error,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

func mcpWireEnabled() bool {
	cfg := config.Get()
	if cfg.Options == nil || cfg.Options.Wire == nil {
		return cfg.Options != nil && cfg.Options.DebugProviderWire
	}
	if cfg.Options.Wire.DebugMCPWire != nil {
		return *cfg.Options.Wire.DebugMCPWire
	}
	return cfg.Options.DebugProviderWire
}

func newMCPLogger(mcp string) *mcpWireLogger {
	cfg := config.Get()
	dir := filepath.Join(cfg.Options.DataDirectory, "logs", "mcp")
	_ = os.MkdirAll(dir, 0o755)
	maxSize := 250
	maxBackups := 10
	maxAge := 30
	compress := true
	mode := "single"
	filename := "mcp.log"
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
		if cfg.Options.Wire.MCPLogMode != "" {
			mode = cfg.Options.Wire.MCPLogMode
		}
		if cfg.Options.Wire.MCPFilename != "" && mode == "single" {
			filename = cfg.Options.Wire.MCPFilename
		}
	}
	if mode == "per_server" {
		safe := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(mcp, "-")
		if safe == "" {
			safe = "mcp"
		}
		filename = safe + ".log"
	}
	key := filepath.Join(dir, filename)
	return &mcpWireLogger{name: mcp, lj: &lumberjack.Logger{Filename: key, MaxSize: maxSize, MaxBackups: maxBackups, MaxAge: maxAge, Compress: compress}}
}

func (w *mcpWireLogger) logJSONL(e mcpWireEntry) {
	b, _ := json.Marshal(e)
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.lj.Write(append(b, '\n'))
}

func mcpWireNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func mcpUnixNow() int64  { return time.Now().UTC().UnixMilli() }

// MCPWireLogger implementation (per-MCP owned)
func (w *mcpWireLogger) Enabled() bool { return mcpWireEnabled() }

func (w *mcpWireLogger) LogStdio(_mcp, stream, line string) {
	if !w.Enabled() {
		return
	}
	w.logJSONL(mcpWireEntry{TS: mcpWireNow(), UnixMS: mcpUnixNow(), Direction: stream, MCP: w.name, Payload: map[string]any{"line": line}, Extra: map[string]any{"transport": stream}})
}

func (w *mcpWireLogger) Out(_mcp, tool, callID, input string) {
	if !w.Enabled() {
		return
	}
	w.logJSONL(mcpWireEntry{TS: mcpWireNow(), UnixMS: mcpUnixNow(), Direction: "out", MCP: w.name, Tool: tool, ToolCall: callID, Payload: map[string]any{"input": input}})
}

func (w *mcpWireLogger) In(_mcp, tool, callID string, payload any, dur time.Duration) {
	if !w.Enabled() {
		return
	}
	w.logJSONL(mcpWireEntry{TS: mcpWireNow(), UnixMS: mcpUnixNow(), Direction: "in", MCP: w.name, Tool: tool, ToolCall: callID, Payload: payload, Extra: map[string]any{"duration_ms": dur.Milliseconds()}})
}

func (w *mcpWireLogger) Err(_mcp, tool, callID string, dur time.Duration, err error) {
	if !w.Enabled() {
		return
	}
	w.logJSONL(mcpWireEntry{TS: mcpWireNow(), UnixMS: mcpUnixNow(), Direction: "in", MCP: w.name, Tool: tool, ToolCall: callID, Error: err.Error(), Extra: map[string]any{"duration_ms": dur.Milliseconds()}})
}

func (w *mcpWireLogger) Event(_mcp string, extra map[string]any) {
	if !w.Enabled() {
		return
	}
	w.logJSONL(mcpWireEntry{TS: mcpWireNow(), UnixMS: mcpUnixNow(), MCP: w.name, Extra: extra})
}
