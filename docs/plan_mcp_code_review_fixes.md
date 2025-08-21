# Plan: Address code review findings (main..HEAD)

Owner: mpokorny (Rai)
Date: 2025-08-20
Scope: MCP manager/connection/wirelog, pubsub drops, logging, minor cleanups.

## Goals (acceptance criteria)
- No data races under `-race` in MCP lifecycle (manager/maps/bundles).
- Wire log error entries use `direction: "err"` (not conflated with `in`).
- No deferred timeouts held longer than necessary in MCP startup loop.
- Tests pass: `go test ./...` and `go test -race ./...`.
- TUI shows per-server drops correctly; pubsub drop accounting remains accurate.
- Lint/vet clean; `go mod tidy` committed.

## Workstream P0 (must-do)
1) Concurrency fixes in MCP manager
   - Replace `defaultMCPManager.conns map[string]mcpConnection` with `*csync.Map[string, mcpConnection]`.
   - Replace `defaultMCPManager.bundles map[string]*mcpBundle` with `*csync.Map[string, *mcpBundle]` (or add RWMutex).
   - Update:
     - Setter: `m.conns.Set(name, conn)`
     - Getter: `if c, ok := m.conns.Get(name) {...}`
     - CloseAll: iterate with `m.conns.Seq2()`.
     - bundle(): use `GetOrSet` to avoid races.
2) Wire log semantics
   - internal/llm/agent/mcp_wirelog.go: Err() should log `Direction: "err"`.
3) Cancel handling in StartAll loop
   - internal/llm/agent/mcp_manager.go: replace `defer cancel()` with explicit `cancel()` after last use of `cctx` per iteration.

## Workstream P1 (nice-to-have, quick)
4) Reduce duplicate transport logging noise
   - Gate `slog.Info/Error` inside `mcpLogger.Infof/Errorf` on `config.Get().Options.Debug`; always keep wire JSONL.
5) Pubsub drop attribution performance/accuracy
   - Handle pointer payloads in reflect path; add fast-path for known struct name field.
   - Follow-up (optional): plumb topic into Publish/IncDrop callers to avoid reflect (separate PR if API changes are nontrivial).
6) Wire log extra fields
   - Consider dropping redundant `Extra.transport` when `Direction` already indicates the channel.

## Workstream P2 (cleanup)
7) Remove dead code
   - internal/llm/agent/mcp_connection.go: remove `newMCPBundle` if unused.
8) Config mutation note
   - (Deferred) If we want immutable config snapshots, switch `ResolvedEnv/ResolvedHeaders` to return copies instead of mutating; not required for this PR.

## Rollout
- Branch: `fix/mcp-race-wirelog`
- Commits:
  - P0 fixes first; run `go test -race ./...` and spot-check MCP UI.
  - P1 items; re-run tests.
  - `go mod tidy` if needed.
- Verification:
  - Run with `--debug` and trigger an MCP startup + tool call.
  - Confirm `.crush/logs/mcp/<server>.log` has `direction=stdout/stderr/http/sse/err` as expected.
  - Confirm TUI MCP sidebar shows `DROPS: N` per-server when drops occur.
- Rollback: revert branch or individual commits; no migrations involved.

## Next steps
- Implement P0 (conns/bundles → csync.Map; Err direction; cancel handling), then run `go test -race ./...`.
- Proceed to P1 if green.
