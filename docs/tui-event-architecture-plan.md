# TUI Event Architecture — Reliability and Performance Plan

Owner: @agentydragon (Rai)
Status: Draft
Updated: 2025-08-18

## Problem statement

Users experience: UI stalls, missed updates (especially final ToolResult), spinners stuck, and sluggish typing/scroll. Root causes span: heavy frames in the TUI, bursty pubsub traffic (MCP ToolState), and lossy transport semantics (broker drops on backpressure).

## Current findings (repo state)

- Transport: internal/pubsub/broker.go uses buffered channels (default 64) and drops on full; we now made buffer size configurable and set at app startup.
- UI: status bar already shows global drops; debug mode logs each event to .crush/logs/ui/ui.log; slow-frame warnings exist.
- Message pipeline: writes are serialized per session and debounced (30ms). ToolState already deduplicates repeated (phase,title,detail) in agent toolStateSink.
- Spinner stops strictly on ToolResult (by design), not on ToolState.PhaseDone.
- Tool outputs are truncated to a default ~100KB.

## Goals

1) Never get stuck: ensure final states are observed by the UI even under load.
2) Keep UI snappy: sub-100ms keystroke latency; smooth scroll; no list-wide reflows on small changes.
3) Predictable semantics: streaming updates can drop/coalesce, but completion cannot.
4) Support heavy outputs without choking the TUI.

## Strategy (phased)

### Phase 0 — Config + Observability (fast)
- [x] Configurable broker buffer size; set early at app start (done).
- [ ] Expose per-topic drop counters and last-drop timestamps in a debug panel; keep status-bar total.
- [x] Increase app.events channel capacity (100 → 1000) and log queue depth in debug (done).

Acceptance: In a stress test with MCP streaming, zero missed ToolResult; DROPS counters stable/low; no pending-forever spinners.

### Phase 1 — Transport hardening (no data model change)
- [ ] Prioritize “final” events (ToolResult, message.Assistant Finish):
  - Option A: dedicated broker instance with bigger buffer for finals only.
  - Option B: PublishPriority(t,payload) that blocks briefly (bounded wait + retry) before recording a drop.
- [ ] Coalesce ToolState at source: throttle emits per tool_call_id to at most 50ms and last-wins (state already dedupes content; add time gate).
- [ ] Make broker buffer configurable per-topic (default: mcp:256, messages:512, finals:1024).

Acceptance: With forced backpressure (slow UI) and flood of ToolState, finals still deliver 100%.

### Phase 2 — UI rendering discipline
- [ ] Enforce “safe viewport strategy” (docs/tui-performance-optimizations.md): typing must not re-render the list; only editor view updates.
- [ ] Strengthen MessageCmp caches; verify no markdown re-render unless content/width changes.
- [ ] Use UpdateItem over SetItems; avoid list-wide reflows on tool state changes.

Acceptance: Typing remains responsive in session with 50+ messages, long markdown, and live tools; list movement tests remain green.

### Phase 3 — Delta+Fetch for heavy payloads (on-demand content)
- [ ] Introduce “payload pointer” for oversized ToolResult: persist full content to history/files and send a small ToolResult stub (name, bytes, first/last 2KB, file path/id).
- [ ] UI renders summary; add keybind to fetch/expand full payload lazily from disk (not over the event bus).

Acceptance: Large tool outputs do not cause slow frames; expanding is explicit and fast.

### Phase 4 — Self-healing and reconciliation
- [ ] Periodic reconciliation loop for spinning tool calls: if spinner > N seconds without update, query DB for ToolResult; apply if present.
- [ ] On session focus/restore, refresh visible tool calls from DB to correct any missed finals.

Acceptance: Even if a final was missed transiently, UI heals within a few seconds or on navigation.

### Phase 5 — Optional client/server split (future)
- [ ] Add an HTTP/SSE (or gRPC) server that owns the DB and eventing. The CLI TUI becomes a thin client subscribing over SSE/WebSocket.
- [ ] Server handles coalescing, priority, and persistence; clients request heavy payloads via separate HTTP endpoints.

Pros: clean QoS boundaries; multiple clients; easier perf profiling. Cons: scope increase, packaging, local networking.

Acceptance: Feature parity with local mode; equal or better reliability under stress; documented migration path.

## Implementation notes

- Final-event prioritization: Keep API: pubsub.Broker; add a FavorFinal bool or separate FinalsBroker. Start with separate broker for finals to de-risk.
- App wiring: app.setupEvents subscribes separate topics; wire finals broker into messages and agent services for ToolResult/Finish.
- Coalescing: in agent.toolStateSink.Update, add time-based throttle per tool_call_id.
- UI: messages/tool_pending tests cover spinner stop; extend tests to assert UpdateItem locality and cache hits.

## Rollout

1) Land Phase 0/1 (low risk), behind config flags as needed.
2) Add tests: broker drop simulation with tiny buffers; storm tests for ToolState; E2E verifying finals are always reflected.
3) Phase 2 improvements; validate with existing list/diff tests.
4) Phase 3 payload pointers gated behind options.max_tool_output_bytes and options.ui.fetch_on_expand.

## Metrics & diagnostics

- pubsub: DropsTotal, DropsByType/Topic, LastDropUnixMS (already present); expose via debug UI.
- UI: counters of tool_state applied, tool_result applied; last update per tool_call_id.
- Perf: log Update/View > 500ms with msg type; add frame time histogram in debug build.

## Backlog (tracked)

- [ ] app: increase events channel to 1000; debug queue depth
- [ ] pubsub: FinalsBroker or PublishPriority
- [ ] agent: ToolState throttle 50ms
- [ ] UI: debug panel for drops and tool-call counters
- [ ] UI: reconcile loop for spinning > N sec
- [ ] History-backed payload pointers for large ToolResult
- [ ] Optional HTTP/SSE server (design doc)

## Kill switches

- options.broker_buffer_size (global)
- options.max_tool_output_bytes (already exists)
- options.debug (enables wire and UI logging)
