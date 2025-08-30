# ADR: UI Reconciliation via DB Delta Polling (No Event Log)

Status: Proposed
Created: 2025-08-29
Authors: Rai (@agentydragon)
Supersedes (scope-wise): The broader “Durable Event Stream + UI Tailer” ADR draft for this problem

## Context
- We already persist domain objects correctly (messages, tool results, etc.).
- The UI can show a tool call as “Working…” indefinitely if it misses the live event that carries the ToolResult, even though the ToolResult is in the DB.
- Cause: transient in-proc pubsub/forwarder can drop under backpressure; the UI relies on those for immediacy.
- Goal: Eliminate “pending forever” without introducing a durable event log or a large architectural change.

## Decision
Add a small, in-process, DB-backed “changes since” query and a lightweight UI reconciliation loop that derives correctness from persisted state. Live events remain only a hint.

## Design Summary
- UI correctness comes from the DB: if a ToolResult is committed, the UI will observe it within ≤1s and stop the spinner.
- Implement a “changes since” query per session keyed by stable watermarks (timestamp + id tie-breaker) on messages and tool-role messages (ToolResult carriers).
- The chat page runs a short-lived poller only when there are pending tool calls (backing off when none). Pubsub events may wake the poller, but are not required for correctness.

## Schema and Time Units (current state)
- messages table columns (from migrations): id TEXT, session_id TEXT, role TEXT, parts TEXT, model TEXT, created_at INTEGER, updated_at INTEGER, finished_at INTEGER, provider TEXT.
- Triggers and SQL set created_at/updated_at using `strftime('%s','now')` (SECONDS, not milliseconds). Existing code and indexes therefore operate on seconds.

## Service Shape (in-process, SQLite)
- Function (example):
  - ListSessionChanges(ctx context.Context, sessionID string, wm Watermarks, limit int) (Changes, Watermarks, error)
  - Watermarks = { Messages: (UpdatedAtSec, ID), ToolMsgs: (CreatedAtSec|UpdatedAtSec, ID) }
  - Changes = { Messages []Message, ToolMsgs []Message }
- Selection predicates (SQLite):
  - Messages (and analogously ToolMsgs):
    WHERE session_id = :sid AND (
      updated_at > :ts OR (updated_at = :ts AND id > :id)
    )
    ORDER BY updated_at ASC, id ASC
    LIMIT :page
  - For tool results: since tool results are Role="tool" messages created once, prefer created_at for the delta watermark:
    WHERE session_id = :sid AND role = 'tool' AND (
      created_at > :ts OR (created_at = :ts AND id > :id)
    )
    ORDER BY created_at ASC, id ASC
    LIMIT :page
- Watermarks handling:
  - The UI stores last (ts, id) per stream and updates them to the highest seen each response.
  - Ties are broken by id (TEXT UUID) to avoid missing rows with identical timestamps; lexicographic is fine because it’s only a tie-breaker.

## Indexes (live DB vs. recommended)
- Live DB (confirmed via sqlite_master on ~/.crush/crush.db):
  - idx_messages_session_id ON messages(session_id)
  - idx_messages_created_at ON messages(created_at)
  - No index on messages(updated_at)
  - No composite indexes on (session_id, created_at/updated_at, id)
- Recommended additions (small migration):
  - `CREATE INDEX IF NOT EXISTS idx_messages_session_created_id ON messages(session_id, created_at, id);`
  - `CREATE INDEX IF NOT EXISTS idx_messages_session_updated_id ON messages(session_id, updated_at, id);`
- Rationale: Enables efficient ORDER BY … WHERE session_id … AND (ts,id) predicates used by the poller. Without them, SQLite will sort/filter more and may scan more rows on large sessions.

## UI Behavior
- When the chat page sees any pending tool calls, it starts a poll loop (Tea Cmd) every 250–500ms:
  - Call ListSessionChanges with the current watermarks for the active session.
  - Apply deltas to the in-memory model; if a ToolResult (Role="tool" message) exists for a pending tool_call_id, call SetToolResult on that item and stop spinning.
  - Update watermarks and repeat until no pendings; then stop polling and back off.
- Pubsub/live events (if present) simply trigger an immediate poll; if they drop, the periodic poll still converges.

## Observability
- Counters: ui.reconcile.polls, ui.reconcile.rows_applied, ui.pending.count, ui.pending.max_age_sec, ui.pending.recovered_via_db.
- Logs: on each reconcile, log session_id, row counts, and any tool_call_ids that transitioned to “result applied (recovered)”.

## Acceptance Criteria
- No tool call remains pending > 1s after its ToolResult commit, even under drops or UI stalls.
- Restarting the UI mid-run yields a correct final state for the session by reading from DB (without relying on missed events).
- DROPS can increase without producing stuck spinners.

## Test Plan
- Unit (chat/message components):
  - Given pending items, mock Changes with a ToolResult; assert SetToolResult is applied and spinner stops; mark as “recovered” in debug meta.
- Integration (model-level):
  - Induce broker/forwarder drops of message events while writing a ToolResult; assert the periodic poll picks it up ≤1s and the spinner stops.
  - Simulate UI stall (sleep in Update); ensure poll still applies results and clears pendings.
- E2E:
  - Run a scenario with noisy ToolState to raise DROPS; ensure the final transcript contains the ToolResult and no pending spinners remain. Verify recovered_via_db counters are reasonable.

## Migration Steps
1) Add/confirm indexes:
   - idx_messages_session_id (exists)
   - idx_messages_created_at (exists)
   - ADD: idx_messages_session_created_id, idx_messages_session_updated_id
2) Implement ListSessionChanges in the appropriate service (e.g., internal/message service) using the predicates above.
3) Add a chat-page poller that:
   - Activates only while pendings exist.
   - Applies deltas idempotently; updates watermarks.
4) Optionally wire a pubsub “poke” to trigger an immediate poll on message/tool topics.
5) Add metrics/logs; gate noisy logs behind Debug.

## Notes & Non-Goals
- ToolState remains ephemeral and is not part of ListSessionChanges. Missing ToolState does not affect correctness; it is UI sugar.
- This ADR does not add an HTTP server, durable event log, or replayable stream; it focuses on eliminating the stuck-pending failure mode with minimal change.
