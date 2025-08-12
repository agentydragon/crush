package agent

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"gopkg.in/natefinch/lumberjack.v2"
)

type mcpWireLogger struct {
	lj *lumberjack.Logger
	mu sync.Mutex
}

type mcpWireEntry struct {
	TS        string         `json:"ts"`
	Channel   string         `json:"channel"`
	Direction string         `json:"direction"`
	MCP       string         `json:"mcp"`
	Tool      string         `json:"tool"`
	ToolCall  string         `json:"tool_call_id,omitempty"`
	Payload   any            `json:"payload,omitempty"`
	Error     string         `json:"error,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

var (
	mcpWireLoggers  sync.Map // key: absolute filename -> *mcpWireLogger
	mcpStdioLoggers sync.Map // key: absolute filename -> *mcpWireLogger
)

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

func getMCPWireLoggerFor(mcp string) *mcpWireLogger {
	cfg := config.Get()
	dir := filepath.Join(cfg.Options.DataDirectory, "logs")
	maxSize := 250
	maxBackups := 10
	maxAge := 30
	compress := true
	mode := "single"
	filename := "mcp-wire.log"
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
		if cfg.Options.Wire.MCPFilename != "" {
			filename = cfg.Options.Wire.MCPFilename
		}
	}
	if mode == "per_server" {
		safe := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(mcp, "-")
		if safe == "" {
			safe = "mcp"
		}
		filename = "mcp-" + safe + "-wire.log"
	}
	key := filepath.Join(dir, filename)
	if v, ok := mcpWireLoggers.Load(key); ok {
		return v.(*mcpWireLogger)
	}
	logger := &mcpWireLogger{lj: &lumberjack.Logger{
		Filename:   key,
		MaxSize:    maxSize,
		MaxBackups: maxBackups,
		MaxAge:     maxAge,
		Compress:   compress,
	}}
	actual, _ := mcpWireLoggers.LoadOrStore(key, logger)
	return actual.(*mcpWireLogger)
}

func (w *mcpWireLogger) logJSONL(e mcpWireEntry) {
	b, _ := json.Marshal(e)
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.lj.Write(append(b, '\n'))
}

func mcpWireNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func getMCPStdioLoggerFor(mcp string) *mcpWireLogger {
	cfg := config.Get()
	dir := filepath.Join(cfg.Options.DataDirectory, "logs")
	maxSize := 250
	maxBackups := 10
	maxAge := 30
	compress := true
	mode := "single"
	filename := "mcp-stdio.log"
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
	}
	if mode == "per_server" {
		safe := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(mcp, "-")
		if safe == "" {
			safe = "mcp"
		}
		filename = "mcp-" + safe + "-stdio.log"
	}
	key := filepath.Join(dir, filename)
	if v, ok := mcpStdioLoggers.Load(key); ok {
		return v.(*mcpWireLogger)
	}
	logger := &mcpWireLogger{lj: &lumberjack.Logger{
		Filename:   key,
		MaxSize:    maxSize,
		MaxBackups: maxBackups,
		MaxAge:     maxAge,
		Compress:   compress,
	}}
	actual, _ := mcpStdioLoggers.LoadOrStore(key, logger)
	return actual.(*mcpWireLogger)
}

func mcpWireLogStdio(mcp, stream, line string) {
	if !mcpWireEnabled() {
		return
	}
	getMCPStdioLoggerFor(mcp).logJSONL(mcpWireEntry{TS: mcpWireNow(), Channel: "mcp:" + mcp, Direction: stream, MCP: mcp, Payload: map[string]any{"line": line}})
}

func mcpWireLogOut(mcp, tool, callID, input string) {
	if !mcpWireEnabled() {
		return
	}
	getMCPWireLoggerFor(mcp).logJSONL(mcpWireEntry{TS: mcpWireNow(), Channel: "mcp:" + mcp, Direction: "out", MCP: mcp, Tool: tool, ToolCall: callID, Payload: map[string]any{"input": input}})
}

func mcpWireLogIn(mcp, tool, callID string, payload any, dur time.Duration) {
	if !mcpWireEnabled() {
		return
	}
	getMCPWireLoggerFor(mcp).logJSONL(mcpWireEntry{TS: mcpWireNow(), Channel: "mcp:" + mcp, Direction: "in", MCP: mcp, Tool: tool, ToolCall: callID, Payload: payload, Extra: map[string]any{"duration_ms": dur.Milliseconds()}})
}

func mcpWireLogErr(mcp, tool, callID string, dur time.Duration, err error) {
	if !mcpWireEnabled() {
		return
	}
	getMCPWireLoggerFor(mcp).logJSONL(mcpWireEntry{TS: mcpWireNow(), Channel: "mcp:" + mcp, Direction: "in", MCP: mcp, Tool: tool, ToolCall: callID, Error: err.Error(), Extra: map[string]any{"duration_ms": dur.Milliseconds()}})
}
