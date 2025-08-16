# TUI performance optimizations (chat list + editor)

This doc outlines practical, low‑risk optimizations to keep typing responsive and avoid unnecessary reflows when sessions contain many assistant/user messages, reasoning blocks, and tool calls.

## Where time goes today
- Rendering the whole chat list frequently (even when only the editor changes).
- Markdown/rendering work for long assistant messages and reasoning blocks.
- Item updates (e.g., tool state, animations) that can trigger reflow of off‑screen items.

## Goals
- Keep typing latency low (no perceived lag while editing).
- Preserve existing UI/scroll/selection behavior.
- Avoid regressions in list movement semantics (backwards/forwards, offsets, selection).

## Optimizations

### 1) Targeted re-rendering (safe viewport strategy)
- Keep the current full‑render path for all structural changes:
  - Item height can change (message content/width changes, tool state updates, new messages, deletes).
  - Selection/movement/navigation.
- Add a fast path used only on non‑structural events (e.g., editor keypresses) to avoid touching the list:
  - Do not call SetItems/UpdateItem.
  - Do not reflow the list; the chat list component is left as‑is.
  - The editor updates its own view only (no chat list rerender). This yields most of the typing win without touching list internals.

Rationale: A previous attempt to partially re-render the list caused flicker/truncation in edge cases. The safe alternative is: during typing, don’t ask the list to re-render at all.

Implementation notes:
- Editor (internal/tui/components/chat/editor/editor.go)
  - Ensure keypress handling does not trigger upstream chat list rerenders.
  - Avoid emitting layout/size changes during typing.
- Chat page
  - Only trigger list renders for message/tool events.

### 2) Strengthen message rendering cache (MessageCmp)
- File: internal/tui/components/chat/messages/messages.go
- Current cache keys: content + width. Keep them, but:
  - Bust cache only on SetMessage (content actually changed) or on width change.
  - Don’t clear caches on animation ticks or unrelated state.
  - Reasoning summary viewport: only recompute when summary text or width changes; the viewport height is already clamped.

Benefits: avoids re‑markdown of long assistant messages while typing elsewhere.

### 3) Keep updates localized; avoid SetItems when possible
- Prefer UpdateItem for tool state/result changes.
- Already done: anim.StepMsg only re-renders visible items when spinning (list.go). Keep that behavior and avoid background reflow for off‑screen items.
- Ensure tool state propagation targets the specific ToolCallCmp; skip redundant list‑wide render calls.

### 4) Diff/markdown highlight costs
- DiffView: auto‑unified for new/deleted files (no pointless split). Already implemented.
- Markdown: reuse the renderer instance keyed by width (styles.GetMarkdownRenderer(width)). Ensure no global renderer rebuild on small updates.

## Non-goals / Optional
- Frame‑throttling/coalescing: We expect the above changes to be sufficient; throttling is optional. If needed later, add a 16–33ms debounce for editor-triggered chat redraws (off by default).
- Truncating assistant content for rendering: Not planned.

## Acceptance criteria
- Typing feels instant in a session with many messages and tool calls; no visible lag on standard hardware.
- No flicker/truncation; list movement tests stay green:
  - `go test ./internal/tui/exp/list -run TestListMovement`
- No regressions in tool/live state updates or permissions prompts.

## Rollout & testing
- Unit tests: existing list tests must pass.
- Manual: open a large session and type in the editor; verify no lag and no diff in chat rendering aside from expected live updates.

## Implementation checklist
- [ ] Editor: audit Update to ensure typing doesn’t trigger list renders.
- [ ] MessageCmp: verify cache busting only on content/width change (content and reasoning).
- [ ] Tool state/result updates: Verify UpdateItem usage; no SetItems unless session reload.
- [ ] Confirm DiffView unified‑on‑single‑sided behavior is kept (done).
- [ ] Run list movement tests.

## High-coverage rendering tests (near E2E)

These tests assert not only logical state but the actual rendered strings for the viewport, close to what a user sees. They avoid manual keystrokes by simulating Tea messages.

1) Large chat session render stability
- Seed the DB with a session containing:
  - 50+ messages: alternating user/assistant; assistant messages with long markdown and reasoning summaries; several tool calls (some nested).
- Start the TUI model tree without the real program loop; call Update with a sequence mimicking app startup and session selection.
- Assert: the chat list’s List.View() produces a stable, non-empty multi-line string; no panics.

2) Typing does not re-render chat list
- Focus the editor component and send a series of tea.KeyPressMsg events (letters, spaces, backspace).
- Spy hook: wrap the chat list’s SetItems/UpdateItem/SetSize to increment counters.
- Assert: during typing bursts, counters remain zero; only the editor’s View changes (you can snapshot before/after of the chat list View string to ensure it doesn’t change).

3) Tool state localized updates
- Create an assistant message with 10 tool calls; render once; record list View.
- Emit an AgentEventTypeToolState for one tool_call_id.
- Assert: chat list re-renders, but the rendered string diff is limited to the lines of the target tool item (compare rendered lines with a small hunk around the tool call’s region).

4) MessageCmp caching correctness
- Build a MessageCmp with a long assistant message; set width to W1, render; set width to W1 again and render; measure elapsed time and ensure cache hits (or count renderer calls via a test stub).
- Change width to W2; render; assert cache misses exactly when width changes.
- Set new content via SetMessage; render; assert cache bust.

5) Reasoning viewport invariants
- Seed reasoning summary with many lines; render; assert height is clamped (<= 10) and scrolling (GotoBottom) is applied.
- Update with same text and width; render again; assert no re-markdown occurs (e.g., by stubbing styles.GetMarkdownRenderer to count calls).

6) List movement invariants regression
- Run existing golden tests:
  - `go test ./internal/tui/exp/list -run TestListMovement -v`
- Ensure no golden diffs.

Implementation scaffolding tips
- Use in-memory DB and services (internal/db + internal/message/session services).
- Compose chat model components directly; feed Tea msgs (WindowSizeMsg, SessionSelectedMsg, pubsub events).
- For renderer call counting, inject test doubles into styles.GetMarkdownRenderer and DiffView where appropriate.
- Snapshot helpers: split rendered strings by lines; assert contains/equals on slices for targeted diffs; keep fixtures under internal/tui/components/chat/testdata/.

## Files of interest
- internal/tui/components/chat/editor/editor.go
- internal/tui/components/chat/messages/messages.go
- internal/tui/exp/list/list.go
- internal/tui/exp/diffview/*
- internal/tui/components/chat/chat.go (event wiring)
