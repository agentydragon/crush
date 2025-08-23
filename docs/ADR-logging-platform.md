# ADR: Centralized Logging Platform and Single-Init Lifecycle

- Status: Implemented
- Date: 2025-08-23
- Owner: mpokorny

## Context / Problem

Logging is currently initialized in multiple places (e.g., `config.Load`, e2e setup helpers, scenario harness), which leads to:

- Hidden side effects: config loading re-initializes logging unexpectedly.
- Race/order dependence: the final log destination depends on which module calls Setup last.
- Tests writing logs to non-artifact paths, making debugging difficult.
- Inconsistent handling of specialized sinks (provider wire logs, MCP logs, UI logs).

This has caused real issues diagnosing streaming ToolState delivery: agent logs and UI logs were not reliably in the per-scenario artifact paths.

## Decision

Adopt a single, centralized logging platform with explicit initialization at application start (and in test harnesses), with no logging side effects in configuration code. The platform exposes:

- A root app logger (structured, file + optional console).
- Named wire loggers with independent rotation (provider/MCP/UI wire logs).
- A clean lifecycle: exactly one initialization per process.

Key rules:

1) Exactly one place initializes the logging platform for a process.
2) `config.Load` MUST NOT (re)initialize logging; it may accept a `*slog.Logger` for internal messages but has no side effects.
3) Subsystems (agent, provider, MCP, TUI) receive a logger (or factory) via DI; they do not call Setup.
4) Test harness determines data directory first, then initializes the logging platform pointing to artifact paths.
5) Separate sinks (wire logs) get their own `*slog.Logger` with rotation; share the same level controls.

## Architecture / API

```go
// Package logging provides a builder for the process-wide logging platform.

// LoggingConfig configures all process loggers.
type LoggingConfig struct {
    Level      slog.Level
    AppLogPath string
    Console    bool
    JSON       bool
    WireLogs   []WireSinkConfig
}

type WireSinkConfig struct {
    Name   string           // "provider", "mcp", "ui", ...
    Path   string           // e.g., <data_dir>/logs/mcp/mcp-wire.log
    Rotate RotationConfig
}

type RotationConfig struct {
    MaxSizeMB  int
    MaxBackups int
    MaxAgeDays int
    Compress   bool
}

// LoggerPlatform holds constructed loggers for use by the app.
type LoggerPlatform struct {
    Root  *slog.Logger            // App root logger
    Sinks map[string]*slog.Logger // Named wire loggers
    Close func() error            // If needed (flush/close)
}

func NewLoggerPlatform(cfg LoggingConfig) (*LoggerPlatform, error)
```

Usage:

```go
// main.go

// 1) Decide dataDirectory (CLI/env) before logging.
dataDir := resolveDataDirFromFlags()

// 2) Initialize logging once.
platform, _ := logging.NewLoggerPlatform(logging.LoggingConfig{
    Level:      slog.LevelInfo,
    AppLogPath: filepath.Join(dataDir, "logs", "crush.log"),
    Console:    true,  // also log to stderr in dev/tests
    JSON:       true,  // structured logs
    WireLogs: []logging.WireSinkConfig{
        {Name: "mcp", Path: filepath.Join(dataDir, "logs", "mcp", "mcp-wire.log"),
         Rotate: logging.RotationConfig{MaxSizeMB: 250, MaxBackups: 10, MaxAgeDays: 30, Compress: true}},
        {Name: "provider", Path: filepath.Join(dataDir, "logs", "provider", "provider-wire.log"),
         Rotate: logging.RotationConfig{MaxSizeMB: 250, MaxBackups: 10, MaxAgeDays: 30, Compress: true}},
    },
})

defer platform.Close()

// 3) Inject loggers into app/services.
app := NewApp(AppOptions{
    Logger:       platform.Root,
    WireLoggers:  platform.Sinks, // map["mcp"], map["provider"], ...
    DataDirectory: dataDir,
})
app.Run()
```

## Out of Scope / Non-Goals

- No runtime log path switching. If the data directory must change, restart the process.
- No multiple competing global loggers. Maintain a single platform per process.
- Do not rebuild handlers on-the-fly; use `slog.LevelVar` if dynamic level changes are needed.

## Rationale

- Determinism: A single init avoids order-dependent bugs and accidentally writing to the wrong directory.
- Testability: Tests can pick artifact directories and get all logs there, reliably.
- Separation: Wire logs should not pollute app logs; they require different rotation and volume handling.
- Maintainability: No hidden logging side effects in config or service constructors.

## Alternatives Considered

- Keep current pattern (multiple Setup calls):
  - Rejected: brittle, hard to reason about, already causing production issues.
- Per-package loggers without a root platform:
  - Rejected: still need a coordination point for rotation, level, and destinations.
- Third-party logging frameworks: not necessary; `slog` with custom handlers (e.g., lumberjack) suffices.

## Migration Plan

1) Introduce `internal/logging` package with `NewLoggerPlatform(LoggingConfig)`.
2) Remove `internal/log.Setup` calls from everywhere except the process bootstrap (app main) and testing harness.
3) Update `config.Load` to accept a logger (optional) and remove any logging side effects. Config may log to the provided logger but does not reconfigure logging.
4) Modify e2e `SetupServices` to:
   - Compute artifact data dir.
   - Build logging platform with artifact paths.
   - Pass platform.Root and named wire loggers into services.
5) Provider/MCP wire logs: create loggers off `platform.Sinks["provider"]` and `platform.Sinks["mcp"]`; stop ad hoc JSONL writers scattered across the codebase.
6) Add a small `logging.NewTestingPlatform(t *testing.T, opts ...)` for unit tests that routes to `t.Logf` and/or temp files.
7) Remove all remaining `log.Setup` re-initializations.

## Acceptance Criteria

- Exactly one logging initialization per process (or per test process) verified by search (no stray `Setup` calls).
- e2e artifacts consistently contain `logs/crush.log`, and separate `logs/mcp`/`logs/provider` folders for wire logs.
- No logging side effects remain in `config.Load` or similar; tests can choose artifact directories before initializing logging.
- Wire logs are independently rotated and sized.

## Risks

- Refactoring churn across multiple packages (agent, provider, MCP, e2e harness).
- Temporary duplication while migrating; mitigate by feature flagging the new platform behind a branch/PR.

## References

- Go `slog` package as the base abstraction.
- lumberjack.v2 for rotation.
- Prior ADR: ToolState ordering (agent-enforced) — consistent logs will make related diagnoses far easier.
