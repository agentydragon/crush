# UI Weird States — Tool Calls and Streaming Updates

This document captures plausible ways the TUI can end up in inconsistent or confusing states, how to detect them, how to diagnose and test them, and how to improve visibility — with a focus on tool call lifecycle (pending → progress → result/cancel/error) and streaming tool state.


## Mental model: event/data flow

- Producer
  - Tools emit ToolResult (final) via agent → messages service → pubsub broker (Event[message.Message]).
  - Tools may also emit streaming ToolState via tools.ToolSink Update (e.g., MCP, Bash) → agent publishes AgentEventTypeToolState via pubsub.
  - MCP progress arrives as JSON-RPC notifications → defaultMCPManager → per-MCP bundle → forwarded as ToolState updates.
- Transport
  - pubsub.Broker broadcasts to subscribers via buffered channels (default buffer size 64, drops events if subscriber channel is full).
- Consumer (UI)
  - appModel.Update routes pubsub events to the current page.
  - chat.messageListCmp:
    - On assistant message updates: appends/updates ToolCall UI items.
    - On tool results: sets ToolResult on matching ToolCall, which stops the spinner.
    - On streaming ToolState: updates existing ToolCall live labels, or buffers them pending item creation.
  - messages.toolCallCmp:
    - Spinner shows while result.ToolCallID == "" and not cancelled.
    - Live label shows Title/Detail and a dynamic elapsed/remaining label derived from ToolState.StartedAt + Meta.deadline_unix_ms.


## Symptom catalog (what “weird” looks like)

- Pending forever
  - Tool call UI shows spinner/“Working …” even though the tool has finished (result exists). Often indicates the ToolResult UI update did not apply.
- Missing progress
  - No live status under a tool call even when MCP reports progress (or progress appears sporadically).
- Stuck or sluggish UI
  - Input lag, View/Update >1s warnings; intermittent scroll/render stutter.
- Out-of-order rendering
  - Live label shows outdated detail after completion, or detail that doesn’t match current tool.
- Nested agent tools
  - Child tool calls not updating; parent stuck spinning though children completed.


## Plausible root causes (by layer)

### Producer/tool/agent layer
- Final ToolResult not published (panic/early return on producer side).
- Tool result persisted to DB but pubsub delivery was dropped (see broker drops below).
- MCP progress listener not registered or registered late; progress events not dispatched to UI.
- PhaseDone/PhaseError ToolState emitted but ToolResult dropped → spinner never stops (UI stops spinner on ToolResult, not on PhaseDone).
- Excessively large outputs cause slow render or exceed buffers; previously could stall UI; now truncated but still worth watching.

### Transport/broker layer
- Channel overflow → events dropped
  - Broker sends on buffered channels; when full, it skips the event. In bursts, especially if the UI is blocked, this can drop critical events (including final ToolResult or ToolState).
- Subscriber isn’t keeping up
  - Long frames (render/update) plus many events → channel fills.

### UI assembly (chat/message list)
- Item creation timing
  - ToolState arrives before ToolCall UI item exists; buffer should capture last state; if buffering map gets overwritten for the call (only last kept), intermittent transitions can be missed (acceptable), but spinner won’t stop unless ToolResult applied.
- ID mismatches
  - ToolCall IDs used for lookups must match DB/tool events. Any mismatch leads to updates not applied.
- Nested propagation
  - Child session routing: parent ToolCallCmp must receive nested ToolCall updates and then nested ToolResult. If lookup fails, parent remains spinning.

### UI rendering (toolCallCmp)
- Spinner stop condition is tied to result.ToolCallID != "" (or cancelled flag). If ToolResult update is lost, UI spins forever even if PhaseDone was rendered.
- Live label update is animation-timer driven; if anim stopped (shouldn’t be unless result applied), label won’t update frequency — cosmetic.

### Lifecycle/reset
- Old behavior: global “notify once” for MCP progress listeners could prevent re-registration; fixed by per-manager bundle + ResetMCPForTests.
- App init ordering
  - If the page/subscriptions not yet set when early events fire, buffering helps, but there’s still a small window where final events could be skipped if broker drops.


## Detection and visibility

- Existing warnings
  - TUI warns when Update or View exceed 1s (slog.Warn tui.update.slow / tui.view.slow).
- MCP wire logs
  - .crush/logs/mcp/mcp.log captures call_start/progress/list_tools/state/in/out; correlate tool_call_id and timing.
- E2E artifacts
  - e2e/_artifacts contain timelines and UI trace files; use them to compare event order vs UI state transitions.
- Proposed counters/metrics (additions)
  - Broker drop counters per event type (increment on select default case); expose via a debug command or status line.
  - UI event counters in chat page: total tool_state updates, total tool_results applied, last-applied timestamps per tool_call_id.
  - Heartbeat/status widget: a compact overlay toggled via keybinding showing per-tool_call_id state (pending/result/live phase, last update times, elapsed/remaining).


## Diagnosis playbook

1) Correlate by tool_call_id
- From UI (hover/log/selection), get tool_call_id. If not obvious, enable debug logging to print IDs on creation/update.
- Check mcp.log for call_start → progress → (in/err) for that ID; verify end-of-call time.

2) Compare with message events
- Inspect DB/messages (or dump via a local helper) to verify ToolResult exists for the call_call_id.
- If present in DB but UI shows pending → suspect broker drop or UI routing failure.

3) Inspect pubsub behavior
- Temporarily instrument Broker.Publish to log when an event is dropped (default branch of the send select); capture counts.
- If drops occur around final ToolResult/AgentEvent → increase buffer size or change priority/coalescing (see Guardrails).

4) UI routing checks
- Confirm messageListCmp.handleToolMessage found the ToolCallCmp by ID and called SetToolResult.
- For nested calls, confirm handleChildSession updated nestedToolCalls slice and SetToolResult on the child.
- Confirm pendingToolStates map was flushed into the ToolCallCmp once it appeared.

5) Rendering performance
- If slow-frame warnings are frequent (>1s), profile the page content size; long markdowns or huge lists can starve event processing. Validate truncation is working for large tool outputs.


## Testing strategy

- Unit-ish component tests
  - messages/tool_pending_test.go already covers pending behavior; extend to assert:
    - SetLiveToolState sets label based on StartedAt/deadline.
    - Spinner stops only on SetToolResult or SetCancelled; PhaseDone alone doesn’t stop.
  - Broker tests with tiny buffer (use NewBrokerWithOptions) to simulate drops; assert that without guardrails final ToolResult can be skipped; this codifies the risk.

- E2E scenarios
  - Happy path: run an MCP tool that streams progress and completes; assert UI transitions (pending → progress with elapsed/remaining → not spinning).
  - Storm test: rapidly publish many ToolState updates, then a ToolResult; assert the final state shows completed even if some ToolState updates are dropped.
  - Nested agent: parent tool launches child tools; assert parent spinner reflects children completion and ultimately stops when parent ToolResult arrives.
  - Large output: bash/view/grep returning huge outputs; assert truncation and that UI remains responsive.

- Chaos toggles
  - Build a test-only manager or broker with:
    - Small buffers
    - Random delays in UI Update/View
    - Optional random drops (injected) for non-final events
  - Validate final ToolResult is still reflected with proposed guardrails below.


## Guardrails and improvements

- Never drop “final” events
  - Route ToolResult and AgentEvent final states via a dedicated broker or with higher-priority buffer (or a small retry loop on publish). Alternatively, persist a “pending tool calls” set in the page and poll DB for their completion every few seconds until resolved.

- Coalesce streaming updates
  - For ToolState, per tool_call_id, store last-only in the broker (or in page state) to minimize pressure and rendering churn. The UI already overwrites last state; avoid flooding the broker by throttling at producer side, too.

- Broker visibility and tuning
  - Increase buffer for the main broker (e.g., 256) or make it configurable.
  - Add counters/metrics for dropped events and expose a lightweight debug UI panel.

- Self-healing UI
  - When a ToolCallCmp has been spinning >N seconds without any event, trigger a background refresh of that call’s state from the DB; if a ToolResult exists, apply it. This closes the loop if the final event was dropped.

- ID traceability
  - In debug mode, render tool_call_id alongside the tool name (small/subtle) to make cross-referencing easier.


## Quick triage checklist

- Is there a ToolResult in DB for the tool_call_id?
- Does mcp.log show the call finishing? Any errors?
- Did we see a broker drop around the relevant time?
- Did messageListCmp find and update the correct ToolCallCmp (parent or nested)?
- Did the spinner actually stop (result.ToolCallID set)? Is the view just stale (try a resize/key to trigger render)?
- Are slow-frame warnings frequent (>1s)? If yes, profile long content and ensure truncation.


## Practical tips / toggles

- Enable MCP wire logs: set options.wire.debug_mcp_wire (or options.debug_provider_wire) and inspect .crush/logs/mcp/mcp.log.
- Watch for TUI slow logs (tui.update.slow / tui.view.slow).
- For ad hoc debugging:
  - Add a temporary status line showing last ToolState/ToolResult counts, last drop count (if instrumented), and number of spinning tool calls.
  - Log SetLiveToolState/SetToolResult calls with tool_call_id.


## Known behaviors by design

- Spinner stops only when ToolResult arrives (or cancelled), not on ToolState.PhaseDone. This avoids stopping prematurely on a misordered ToolState.
- ToolState buffering keeps only the last state per tool_call_id; intermediate states may be lost (acceptable for UI).
- Broker drops events when subscriber is too slow; critical if it drops final events. Treat as a signal to increase buffers or add self-healing/polling.
