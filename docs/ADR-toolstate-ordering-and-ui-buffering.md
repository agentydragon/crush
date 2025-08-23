# ADR: ToolState Ordering, UI Buffering, and Event Flow

- Status: Proposed
- Date: 2025-08-22
- Owners: mpokorny (+ contributors)

## Context / Background

Crush ingests OpenAI Responses SSE for assistant messages and tool calls, then executes tools locally and streams live ToolState updates (title/detail) to the UI, before finally persisting ToolResult. The current UI shows a pending tool call with a spinner and a live “tail” (e.g., Bash output) while a tool runs.

Current pipeline (simplified):

1. Provider SSE (single ordered stream per request):
   - `response.created` → `response.in_progress` → `response.output_item.added` (function_call) →
     `response.function_call_arguments.delta/done` → `response.completed` with `reason=tool_use`.
2. Agent maps SSE → assistant message + ToolCall (id = `call_id`), persists, then executes tool.
3. Tools publish `ToolState` via `toolStateSink`, which debounces (50ms) and emits an AgentEvent.
4. UI receives AgentEvents and Message events and renders the list (pending tool with spinner + live detail; final result when complete).

## Problem

- The UI buffers `ToolState` when the corresponding tool UI item doesn’t yet exist. This is compensating for out-of-order events at the UI layer.
- In practice, we see tests that time out with the UI showing the pending box but never the live tail (`1`, `1\n2`, ...). Artifacts show the ToolCall item exists but there are no visible live updates.
- Root cause candidates:
  - The agent publishes `ToolState` before the tool item (MessageUpdated) is delivered; UI buffers, but buffer may not drain if ordering stays unfavorable.
  - ToolCall is marked finished too early, shortening the pending window.
  - Logs are not consistently captured; diagnosing ordering before is hard.

Even though SSE is linear and single-threaded, our internal processing introduces layers (agent, UI) that can reorder emissions relative to each other, creating “races” at the wrong abstraction level.

## Forces / Constraints

- SSE is a single ordered stream per request, but local execution is concurrent: multiple tool calls can run in parallel.
- We need debounced `ToolState` to avoid UI thrash (50ms is reasonable).
- We want the UI simple and unambiguous: no ordering repair at the view layer.
- DB persistence is desirable at meaningful boundaries (tool call created, tool result) but not required for every intermediate `ToolState`.

## Decision

Move the ordering responsibility into the agent/service layer and remove UI-level ordering repair (buffering). Concretely:

1) Agent MUST publish `MessageUpdated` that creates the ToolCall item BEFORE publishing any `ToolState` for that ToolCallID.

2) If a `ToolState` is ready before the ToolCall UI/message exists, buffer it inside the agent’s per-session dispatcher and flush immediately after the tool call creation event is emitted.

3) UI will no longer buffer `ToolState`. It simply applies what arrives; if no tool item is present, the event is considered a violation upstream.

4) Maintain the 50ms `ToolState` debounce in the agent’s sink; tests that assert streaming should sleep ~2× debounce to be robust. Keep a test-only `FlushPending()` hook for deterministic unit tests (avoid in scenario e2e tests).

5) Keep IDs consistent: OpenAI `call_id` == `ToolCall.ID` in DB and events; agent sink and UI lookups use exactly that ID.

## Rationale / Why

- Single authoritative dispatcher per session ensures ordering across message updates and tool states without pushing complexity into the UI.
- Eliminating UI buffering removes a class of “race-like” behavior where the UI has to guess intent and repair ordering.
- Agent-side gating is straightforward: “toolcall-created” precedes “toolstate.*” precedes “tool_result”.
- Debounce stays where it belongs (tool execution feedback), not in the rendering pipeline.

## Alternatives Considered

- Keep UI buffering (current):
  - Pro: masks some upstream ordering issues.
  - Con: Wrong layer to fix ordering; leads to sticky states when buffer doesn’t drain; harder to reason about correctness.

- Persist every `ToolState` to DB and reconstruct in UI:
  - Pro: durable visibility.
  - Con: heavier writes; still need correct emission ordering; doesn’t solve live UI race if events are misordered.

- Remove debounce entirely:
  - Pro: simpler event flow.
  - Con: UI thrash, poor perf with high-frequency updates.

## Consequences

- Positive:
  - UI becomes passive and more reliable; fewer “pending forever” states.
  - Logs and state transitions become easier to reason about (single dispatcher invariants).
- Negative / Costs:
  - Small refactor in agent/service to gate emissions and remove UI buffering.
  - Tests that relied on UI buffering must be updated to wait for tool item creation before asserting live state.

## Implementation Sketch

1) Agent/service (internal/llm/agent):
   - Ensure upon function_call detection:
     - Persist assistant message update with ToolCall, publish `MessageUpdated`.
     - Create tool sink; DO NOT publish `ToolState` until after `MessageUpdated` is out.
   - Implement a per-session dispatcher (or reuse the Broker with a thin gating layer) that sequences:
     - `toolcall-created` → `toolstate` → `tool_result` order per ToolCallID.
   - If a `ToolState` arrives early, buffer within the agent (map by ToolCallID) and flush just after toolcall-created emission.

2) UI (internal/tui/components/chat):
   - Remove `pendingToolStates` map from chat.go; delete buffering + draining logic.
   - Keep only immediate application: if item exists, apply; otherwise the agent violated the contract (consider logging a warning for debugging).

3) Tests (e2e):
   - In streaming tests, gate the first tool step until the pending tool item is visible (`id=<call_id> state=pending`). Then proceed with marker files (go1..go5) and sleeps ~2× debounce.
   - Replace ad-hoc stdout prints with slog; ensure logs go to per-scenario artifacts.

4) Observability:
   - Keep `toolstate.update` logs in agent sink and add `ui.toolstate.in` logs in chat.Update (already added).
   - Ensure logger is initialized early in test setup so `crush.log` is produced under artifacts/logs.

## Migration Plan

- Step 1: Add agent-side gating (buffer-if-early, flush-after-toolcall-created). Keep UI buffering temporarily.
- Step 2: Update tests to wait for tool item before sending the first “go” marker.
- Step 3: Remove UI buffering and re-run tests; verify no regressions.
- Step 4: Cleanup, document invariants in code comments and docs.

## Risks

- If developer tools emit `ToolState` with wrong ToolCallID, UI will not reflect it; logs should surface this immediately.
- Over-gating in the agent might delay the very first `ToolState` by a few ms while ensuring creation ordering; acceptable trade-off.

## Acceptance Criteria

- Streaming e2e tests reliably show incremental tails (1, 1\n2, ...), without relying on UI buffering.
- No “pending forever” states when tool result has been persisted.
- Logs show coherent sequence per ToolCallID: `toolcall-created` → `toolstate.update` (0..N) → `tool_result`.

## References

- UI: internal/tui/components/chat/chat.go (message and tool call handling).
- ToolCall UI: internal/tui/components/chat/messages/tool.go.
- Agent sink: internal/llm/agent/agent.go (`toolStateSink`, debounce + publish).
- OpenAI Responses mapping: internal/llm/provider/openai_responses.go (tool call mapping, finish_reason).
- Current test: e2e/scenario_bash_streaming_fake_test.go (debounce-aware streaming test).
