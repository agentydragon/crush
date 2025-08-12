# OpenAI Responses API: Tool Call ID Mapping

Context: When using the OpenAI Responses API with tool/function calls, there are two distinct identifiers in play for each tool use, and mixing them causes UI inconsistencies (e.g., a stuck "Waiting for tool response…" entry and a duplicate resolved entry).

## The two IDs

- response.output_item.id ("item.id")
  - Identifier of a single output item in the streaming response.
  - Used by streaming delta/done events to refer back to a particular item.
  - Transient and provider-internal; not stable across turns.

- function_call.call_id ("call_id", e.g., fc_…)
  - Stable identifier of a specific function/tool call.
  - Must be echoed in function_call_output on the next turn.
  - This is the ID that should be persisted and matched with tool results.

## Internal Crush model

- message.ToolCall.ID is used to correlate the tool invocation with its result.
- message.ToolResult.ToolCallID must equal the corresponding ToolCall.ID.
- Therefore, for the Responses API, ToolCall.ID must be set to function_call.call_id (NOT item.id).

If we emit ToolUseStart/Delta/Stop events keyed by item.id, but later save ToolResult with ToolCallID = call_id, the UI will render one pending tool (item.id) that never resolves and another resolved tool (call_id) created later. This is the source of the duplicate/stuck tool entries.

## Correct mapping from Responses → internal events

Stream events to internal ProviderEvent:

- response.output_item.added (function_call)
  - Obtain call_id from item.function_call.call_id.
  - Emit ToolUseStart with ID = call_id (fallback to item.id only if call_id genuinely absent).
  - Record mapping: item.id → call_id.

- response.function_call_arguments.delta
  - These events reference the item.id. Route to the stable call_id via the item.id → call_id mapping.
  - If the delta arrives before output_item.added provided the call_id, buffer deltas per item.id and flush once call_id is known.

- response.function_call_arguments.done
  - Same as delta: use item.id → call_id mapping; if call_id not yet known, mark done-pending until output_item.added arrives.

- response.completed
  - Build ProviderResponse.ToolCalls with ToolCall.ID = call_id, Name, and the final Arguments.
  - FinishReason mapping: incomplete.reason=tool_use → tool_use; incomplete.reason=max_output_tokens → max_tokens; else if tool calls present → tool_use; else → end_turn.
  - For any started but not finished call_id, emit a final ToolUseStop to close the loop.

## Parallel tool calls

- Multiple function calls appear as multiple output items; deltas from different items may interleave.
- Keep per-item buffers and the global mapping item.id → call_id.
- All internal events (start/delta/stop) must ultimately be keyed by call_id to ensure a single coherent tool call entry in the UI per function invocation.

## Failure modes we avoid

- Start using item.id, stop/results using call_id → UI shows:
  1) A pending tool entry keyed by item.id that never gets a result.
  2) A second entry keyed by call_id with the result → apparent duplication.

## Proposed next step: Explicit translation layer (TODO)

We should introduce an explicit, well-tested translation layer that:

- Treats OpenAI Responses stream as provider-internal state.
- Deliberately separates provider IDs from internal IDs. Consider intentionally scrambling or namespacing IDs at the boundary to make any leakage obvious in tests.
- Provides a single, well-specified mapping contract:
  - Only call_id is exposed to the app as ToolCall.ID.
  - All streaming events are translated to the same call_id, regardless of event order.
  - item.id is never persisted or exposed outside the provider adapter.

Suggested acceptance tests (parallel and ordering edge cases):

- Single tool call: added → deltas → done → completed.
- Delta arrives before added: buffer and flush after added.
- Done arrives before added: hold and emit stop after added.
- Parallel calls interleaving: A/B added, mixed deltas/done, both complete; ensure no cross-wiring.
- Missing call_id (fallback to item.id): start/delta/done/complete remain consistent.
- Completion without explicit .done: auto-stop any started-but-unfinished calls.

This documentation captures the current observed discrepancy and the intended mapping rules. A follow-up change should implement the explicit translation layer and tests to enforce this contract.
