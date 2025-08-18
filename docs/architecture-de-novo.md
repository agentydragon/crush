# Crush — De Novo Architecture (Robust, Modern, Resilient)

Author: Rai (@agentydragon)
Date: 2025-08-18
Status: Draft

## Design principles

- Reliability first: UI must never get stuck; final states are durable and observable.
- Separation of concerns: headless engine (state + tools) vs. thin clients (TUI/GUI/CLI).
- Backpressure-aware streaming: bursty progress coalesced; finals prioritized; payloads fetched on demand.
- Idempotency + replay: clients can recover from drops/disconnects without data loss.
- Simple to run locally: single binary, default local SQLite; optional daemon mode.

## High-level architecture

- Core Server (headless):
  - Owns SQLite DB, tool execution, message assembly, and event streaming.
  - Exposes:
    - HTTP/JSON (or gRPC) query API for resources: sessions, messages, tool_calls, tool_results.
    - SSE or WebSocket event stream: typed events with offsets.
  - Runs in-process by default (embedded) or as `crush serve` daemon.

- Clients:
  - TUI (bubbletea) and future GUI/CLI clients.
  - Subscribe to SSE/WebSocket for lightweight events; fetch details via HTTP as needed.
  - Maintain minimal local state, derive UI from fetched canonical state.

## Data model (core entities)

- Session(id, title, created_at, summary_message_id, tokens, cost,...)
- Message(id, session_id, role[user|assistant|tool], parts[], created_at, provider, model,...)
- ToolCall(id, parent_message_id, name, input, started_at, finished_at, state_phase,...)
- ToolResult(id, tool_call_id, is_error, bytes, meta, stored_path?, created_at)
- ToolStateEvent(id, tool_call_id, phase, title, detail, created_at) — ephemeral stream, not required for correctness.

All entities are versioned (version int or updated_at) for conditional fetch (If-None-Match style), enabling efficient polling where needed.

## Event stream semantics

- Transport: SSE (HTTP) by default; WebSocket optional for bidirectional control; both support reconnect.
- Event envelope: { offset, topic, type, id, session_id, tool_call_id, version, summary }.
  - offset: monotonically increasing per stream; clients can request `?since=offset` for replay on reconnect.
  - summary: compact metadata only (no heavy payloads).
- Topics: messages, tool_calls, tool_results, tool_state, permissions, system.
- QoS:
  - ToolState: coalesced (last-wins) and throttled server-side per tool_call_id (e.g., ≥50ms), best-effort delivery.
  - Finals (tool_results, assistant message finish): at-least-once; persisted before emit; retrievable by replay; small payloads only.
  - Ordering: per-session ordering guaranteed; cross-session loose ordering.

## Backpressure and priority

- Internals use a priority queue (or separate channels) for events:
  - Priority 1 (highest): final ToolResult, Assistant Finish, Permission decisions.
  - Priority 2: messages created/updated (structure changes).
  - Priority 3: ToolState (progress).
- If buffers fill, drop/coalesce only Priority 3. Priority 1/2 block briefly with bounded wait-and-retry (or larger dedicated buffers).
- Producer-side throttling for ToolState; dedupe identical (phase,title,detail).

## Payload handling (heavy outputs)

- Large tool outputs are persisted to disk (history store) and referenced by path + metadata.
- Event carries only: tool_call_id, result_id, is_error, size_bytes, head/tail previews.
- Client expands on demand: fetches via HTTP file endpoint or paginated API.
- UI truncates by default; explicit expand keybind for full view.

## TUI client architecture

- Virtualized list with UpdateItem granularity; no SetItems for minor updates.
- Typing fast path: editor updates do not trigger list rerender.
- Markdown cache keyed by (message_id, content_hash, width). Bust only on content/width change.
- ToolCall components subscribe to their own state; nested tool calls reflected by parent but updated locally.
- Self-healing: if a ToolCall spins >N seconds, client fetches current result via HTTP; also refresh visible items on resume/focus.

## Failure handling & reconcilation

- Clients persist last seen offset; on reconnect, request replay since offset.
- Finals are idempotent (versioned); applying twice is safe.
- On mismatch (dropped apply), client can fetch by id + version to reconcile.

## Observability

- Server metrics: per-topic queue depth, drops (by topic/type), event latencies, tool runtimes.
- Structured logs with tool_call_id/session_id correlation.
- Debug endpoints: /metrics (Prometheus), /pprof, /events?dryrun
- TUI debug pane: drops per topic, active spinners, last update/time.

## Execution model (tools)

- Tool runner tracks state machine: Created → Running → (Progress)* → Finished|Error|Cancelled.
- Each call emits lifecycle events; ToolState throttled/coalesced.
- Sandboxed execution with permission checks; metadata includes cwd, duration, exit_code.

## APIs (sketch)

- GET /sessions, /sessions/{id}
- GET /sessions/{id}/messages?since_version=...
- GET /tool_calls/{id}, GET /tool_results/{id}
- GET /events?since=OFFSET&topics=messages,tool_results,tool_state
- POST /sessions/{id}/prompt {text, attachments}
- POST /tool_calls/{id}/cancel
- GET /files/{path-or-id}

## Packaging & modes

- Default: single binary; in-process server; TUI connects via in-memory adapter (function calls) to avoid localhost ports.
- Daemon mode: `crush serve` opens HTTP on 127.0.0.1 with token auth; `crush tui` connects over HTTP/SSE.
- Tests run against in-process server; E2E can target HTTP mode.

## Migration path

- Introduce server package and SSE stream internally; keep current TUI wired via in-process adapter.
- Gradually move event emission to server’s event log + offsets; replace local broker.
- Swap ToolResult payload to pointer + preview; keep compatibility flag until UI updated.

## Acceptance criteria

- No stuck spinners in stress tests; finals always visible within 1s.
- Keystroke latency < 50ms; smooth scroll; frame warnings negligible.
- Reconnect/replay recovers from simulated drops; zero lost finals.
- Large outputs do not stall UI; expansion is explicit and responsive.

## Open questions

- SSE vs WebSocket: SSE is simpler and sufficient; WebSocket only if we need client→server control streaming.
- gRPC vs HTTP+SSE: gRPC is nice but overkill; JSON + SSE aligns with CLI tooling and debugging.
- Event storage: lightweight persistent log table vs per-entity updated_at polling; we can start with per-entity versions and add a compact event log later for replay.

## Initial milestones

1) Build headless Server with HTTP + SSE and offseted event stream; wire in current services and DB.
2) Implement priority event dispatcher (finals vs progress) with throttled ToolState.
3) Update TUI to connect through in-process server adapter; keep current UI, add self-heal reconcile.
4) Switch ToolResult to pointer + previews; add /files endpoint.
5) Add replay on reconnect and debug metrics pane.
