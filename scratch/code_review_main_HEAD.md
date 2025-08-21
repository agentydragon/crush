# Code review (main..HEAD) — prioritized findings

Date: 2025-08-20

Scope: Changes under internal/llm/agent (MCP), TUI MCP panel, pubsub, DB connect, config, provider wirelog, chat/messages rendering, CI/workflows, docs.

Legend: file_path:line_number → issue + recommendation.

---

## P0 — Correctness, data races, resource handling

1) internal/llm/agent/mcp_manager.go:36–44,120–125,127–132
   - Issue: defaultMCPManager.conns is a plain map accessed from multiple goroutines (writer in doGetMCPTools via mgr.conns[name] = ..., readers via GetClient and CloseAll). No mutex/atomic protection ⇒ potential data race.
   - Recommendation: replace conns with csync.Map[string,mcpConnection] or guard reads/writes with RWMutex. Mirror the pattern used for states (csync.Map).

2) internal/llm/agent/mcp_manager.go:42–43,60–73
   - Issue: defaultMCPManager.bundles is also a plain map accessed from multiple goroutines (bundle() is called during concurrent init and later). Potential race.
   - Recommendation: protect bundles with RWMutex or move to csync.Map.

3) internal/llm/agent/mcp_connection.go:154–157
   - Issue: newMCPBundle is dead/unused.
   - Recommendation: remove or use via manager to avoid bit-rot.

4) internal/llm/agent/mcp_wirelog.go:138–143
   - Issue: Err() logs Direction:"in" for error cases. This conflates normal inbound payloads and error events → ambiguous analysis downstream.
   - Recommendation: set Direction:"err" (and keep Error populated). Adjust any consumers/tests accordingly.

5) internal/llm/agent/mcp-tools.go:439–455
   - Issue: ctx := context.WithTimeout(ctx, cfgTimeout) shadowing the parent ctx variable (function param) is fine, but passing that ctx to Initialize + ListTools while calling c.Start(context.Background()) means Start’s goroutines outlive the timeout; OK by design. Ensure Close paths always execute on error (they do). No action needed, logging here for awareness.

6) internal/llm/agent/mcp_manager.go:84–116
   - Issue: In StartAll loop, you defer cancel() inside the loop; all defers run at function return. While benign for small N, this holds timers longer than needed.
   - Recommendation: replace `defer cancel()` with `cancel()` once the last use of cctx is done (after Initialize or error path) to promptly release timers.

7) internal/db/connect.go:56–62
   - Good: Added db.Close() on migration/dialect error paths → fixes resource leak.

## P1 — Logging semantics, performance/noise

8) internal/llm/agent/mcp-tools.go:560–586
   - Issue: mcpLogger.Infof/Errorf forward to both slog and wire logs. This likely duplicates raw transport lines into app logs and JSONL, increasing noise and IO.
   - Recommendation: Gate slog.* with config.Options.Debug (or remove for wire-only); alternatively, lower slog level or sample.

9) internal/pubsub/broker.go:118–171
   - Issue: reflect-based best‑effort topic derivation on every drop (hot path) adds overhead and is brittle (pointer vs value struct). Also aggregates into global maps under lock.
   - Recommendation: allow Publish to accept optional topic string (or have callers include topic in Event) to avoid reflect. If keeping reflect, handle pointer to struct and add fast-path for known types.

10) internal/llm/agent/mcp_wirelog.go:117–123
    - Note: LogStdio writes both Direction and Extra.transport with the same value; Direction now carries stdout/stderr/http/sse, so Extra.transport may be redundant. Consider keeping only one to reduce JSON size.

11) internal/tui/components/chat/messages/messages.go:121–176,196–233,233–281,348–376
    - Good: Markdown render caching with width invalidation; remember to invalidate on theme renderer changes if those can happen at runtime (low risk today).

## P2 — UX/telemetry and minor correctness

12) internal/tui/components/mcp/mcp.go:86–91
    - Fix: Now shows drops per server with topic "mcp:<server_name>"; aligns with pubsub drop attribution.

13) internal/llm/agent/mcp-tools.go:137–168
    - Fix: Tool call now propagates result.IsError to ToolResponse.IsError — previously could misclassify errors as success text.

14) internal/config/config.go:153–182
    - Note: EffectiveReasoningSummary logic is clear; ensure docs mention precedence (ReasoningSummary overrides ShowReasoningSummaries).

15) internal/config/config.go:263–279,281–292
    - Note: ResolvedEnv/ResolvedHeaders mutate the config maps in place. If immutability is desired for cfg snapshots, consider returning copies instead of modifying cfg.

## P3 — Cleanups and consistency

16) go.mod/go.sum
    - Action: You ran `go mod tidy` (good). Ensure commit includes these changes.

17) internal/llm/agent/mcp_manager.go vs existing getDefaultMCPManager/doGetMCPTools
    - Observation: Two code paths exist for MCP lifecycle (legacy doGetMCPTools + new StartAll/manager). Ensure callers converge to a single path to avoid drift.

18) internal/llm/agent/mcp_wirelog.go
    - Suggestion: Include distinct channels for stdin captures once write-path is hooked (planned). Name them stdin/stdout/stderr, and reserve Direction for channel; use Event for higher-level lifecycle.

---

## Quick wins (suggested patches)

- Protect maps with concurrency:
  - Change defaultMCPManager.conns to `csync.NewMap[string, mcpConnection]()` and update GetClient/CloseAll/assignment accordingly.
  - Add an RWMutex around defaultMCPManager.bundles or switch to csync.Map.

- Change Err() direction:
  - internal/llm/agent/mcp_wirelog.go:142 → set `Direction: "err"`.

- Defer-in-loop:
  - internal/llm/agent/mcp_manager.go:84–116 → call `cancel()` explicitly instead of deferring.

- Gate slog transports:
  - internal/llm/agent/mcp-tools.go:563, 578 → if not in debug mode, skip slog to avoid duplication; keep wire logs as the source of truth.

---

All tests: green on local run (`go test ./...`).

If you want, I can prepare a PR with the P0 fixes (map concurrency, Err direction) and P1 logging gate next.
