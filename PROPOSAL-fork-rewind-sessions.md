# Proposal: Conversation Forking and Rewind

## Goal
Enable users to branch a conversation from any earlier point ("Fork from here") and continue in a new session, while preserving provenance and keeping tool-call results and attachments intact. Provide a clear UI affordance to navigate between branches.

## Non‑goals (first iteration)
- True streaming resume of an in-flight Responses stream after process restart.
- Cross-branch de‑duplication of token/cost accounting beyond what is already recorded per session.

## Motivation
- Recover from wrong turns without losing current work.
- Explore multiple approaches in parallel from the same prompt/state.
- Maintain provenance of tool results and attachments when branching.

## Data Model
We already have:
- Sessions table with `parent_session_id` (nullable) supporting ancestry.
- Messages table storing serialized parts (text, tool_call, tool_result, binary, image_url, finish) per session.

This allows branching by creating a new session with `parent_session_id = <source session id>` and copying messages up to a chosen message boundary.

## User Experience
- Message action: "Fork from here"
  - Available on any message in a session transcript.
- When invoked:
  - Prompt for a branch title (default: "Fork of <source title> @ <short message id>"), or generate automatically.
  - Create the new session and navigate to it, with the transcript truncated at the selected message (inclusive).
- Sessions list shows lineage
  - Child sessions grouped under parents with a chevron/indentation.
  - Breadcrumb on chat header: `<Root Title> › <Child Title>`

## Semantics / Copy Rules
- Messages copied: all messages from the start of the source session up to and including the selected message.
- Parts integrity: copy the serialized parts verbatim to preserve:
  - tool_call parts (id, name, input)
  - tool_result parts (tool_call_id linkage, content)
  - binary/image_url parts
  - reasoning summaries and finish parts
- Provider/model metadata: maintain original `model` and `provider` fields for copied historical messages; new messages in the fork can use current model selection.
- Do not mutate the original session.

## Backend API/Service
Add to session/message services:

- SessionService
  - ForkFromMessage(ctx, sourceSessionID, messageID, titleOpt) (Session, error)
    - Validate message belongs to sourceSessionID
    - Create new session with `ParentSessionID = sourceSessionID`
    - Copy messages <= messageID into the new session

- Message copy implementation
  - List messages for source session; iterate until messageID hit and create new messages via existing `MessageService.Create` using the original parts/model/provider from `Get` results.
  - Preserve relative order and timestamps where feasible:
    - Use current creation time for the new rows; retain original `FinishedAt` semantics via finish part already embedded in parts.

Idempotency:
- If ForkFromMessage is called twice with the same inputs, return the already-created session if it exists (optional in v1).

## UI Changes (TUI)
- Add "Fork from here" to message contextual actions (keybinding + menu).
- After fork creation, switch the active session to the new one.
- Sessions panel displays ancestry (indent child sessions; show a small branch icon).

## Edge Cases
- Selected message is the last one: fork is effectively a duplicate of the current state (valid).
- Selected message contains tool calls/results: copying parts verbatim preserves tool_call_id and result content; this is fine because the new conversation history is a snapshot, not a live resumption.
- Binary payload size: copying large binary parts duplicates storage; acceptable for v1. Consider dedup in a future pass.

## Telemetry / Accounting
- Token and cost numbers in the new session restart from zero; historical numbers remain with the original session. We do not recompute or backfill costs on the fork (v1 simplicity).

## Testing
- Unit: ensure ForkFromMessage copies the correct subset of messages and parts; verify tool_call and tool_result linkage is intact.
- Integration: user flow creates a new session, navigation switches, transcript matches source prefix.

## Rollout Plan
- Phase 1: Backend-only service + basic TUI action without sessions lineage UI.
- Phase 2: Sessions list ancestry visualization and breadcrumbs.

## Future Work
- Add snapshot/restore labels on messages to mark good checkpoints.
- Optional: stream-resume support via Responses.GetStreaming(response_id, starting_after) for crash recovery (separate proposal).
