package logging

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// LoggingConfig describes how to initialize the process loggers.
type LoggingConfig struct {
	Level      slog.Level
	AppLogPath string
	Console    bool
	JSON       bool
	WireLogs   []WireSinkConfig
}

type WireSinkConfig struct {
	Name   string
	Path   string
	Rotate RotationConfig
}

type RotationConfig struct {
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	Compress   bool
}

type LoggerPlatform struct {
	Root  *slog.Logger
	Sinks map[string]*slog.Logger
	Close func() error
}

// multiHandler fans records to multiple handlers.
type multiHandler struct{
	hs []slog.Handler
}

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.hs { if h.Enabled(ctx, l) { return true } }
	return len(m.hs) > 0
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.hs {
		// Clone to avoid reuse issues
		rec := slog.Record{
			Time:    r.Time,
			Message: r.Message,
			Level:   r.Level,
			PC:      r.PC,
		}
		r.Attrs(func(a slog.Attr) bool { rec.AddAttrs(a); return true })
		_ = h.Handle(ctx, rec)
	}
	return nil
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(m.hs))
	for i, h := range m.hs { hs[i] = h.WithAttrs(attrs) }
	return multiHandler{hs: hs}
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(m.hs))
	for i, h := range m.hs { hs[i] = h.WithGroup(name) }
	return multiHandler{hs: hs}
}

// NewLoggerPlatform builds the root app logger and named wire sinks.
func NewLoggerPlatform(cfg LoggingConfig) (*LoggerPlatform, error) {
	// Ensure parent dirs
	if cfg.AppLogPath != "" {
		_ = os.MkdirAll(filepath.Dir(cfg.AppLogPath), 0o755)
	}
	// Build app handler(s)
	var hs []slog.Handler
	if cfg.AppLogPath != "" {
		appRot := &lumberjack.Logger{
			Filename:   cfg.AppLogPath,
			MaxSize:    10,
			MaxBackups: 0,
			MaxAge:     30,
			Compress:   false,
		}
		hs = append(hs, slog.NewJSONHandler(appRot, &slog.HandlerOptions{Level: cfg.Level, AddSource: true}))
	}
	if cfg.Console {
		// Console uses JSON too for consistency; could be TextHandler if desired
		hs = append(hs, slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.Level, AddSource: true}))
	}
	var root *slog.Logger
	switch len(hs) {
	case 0:
		root = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.Level, AddSource: true}))
	case 1:
		root = slog.New(hs[0])
	default:
		root = slog.New(multiHandler{hs: hs})
	}

	// Wire sinks
	sinks := make(map[string]*slog.Logger)
	for _, ws := range cfg.WireLogs {
		if ws.Path == "" || ws.Name == "" { continue }
		_ = os.MkdirAll(filepath.Dir(ws.Path), 0o755)
		rot := &lumberjack.Logger{
			Filename:   ws.Path,
			MaxSize:    ws.Rotate.MaxSizeMB,
			MaxBackups: ws.Rotate.MaxBackups,
			MaxAge:     ws.Rotate.MaxAgeDays,
			Compress:   ws.Rotate.Compress,
		}
		lh := slog.NewJSONHandler(rot, &slog.HandlerOptions{Level: cfg.Level, AddSource: true})
		sinks[ws.Name] = slog.New(lh)
	}

	// Set as default so existing slog calls work without invasive refactors
	slog.SetDefault(root)

	return &LoggerPlatform{Root: root, Sinks: sinks, Close: func() error { return nil }}, nil
}
