# Bash tool streaming: current state and completion plan

Status: WIP (mid‑refactor)

This document explains what exists today, what’s missing to support streaming partial results from the Bash tool, and a concrete, incremental plan to finish.

## TL;DR

- Provider streaming is implemented and stable: the agent streams assistant text deltas, tool_call starts/deltas/stops from providers like OpenAI, Anthropic, Gemini.
- Tool execution is still synchronous: internal/llm/tools/bash.go returns a single ToolResponse only after the command finishes. No partial output reaches the UI/DB during execution.
- Shell layer buffers: internal/shell.Shell.Exec collects stdout/stderr into bytes.Buffers and only returns at the end.
- UI already supports updating tool call tiles as results arrive, via SetToolResult, but we currently emit the Tool role message only when the tool has fully finished.

Goal: stream stdout/stderr from Bash to the user as it arrives, updating the Tool result in place, with cancellation/timeout honored and final metadata (cwd, exit code, timing) consistently attached.

## What’s in place

- Persistent shell: internal/shell.GetPersistentShell provides a singleton shell across a run. It maintains working directory and environment across commands and is protected by a mutex to keep state consistent.
- Security: Bash tool applies command blocking (banned commands and subcommands) via shell.BlockFunc.
- Tool API: BaseTool.Run returns a ToolResponse (final string + metadata). No streaming hooks.
- Agent streaming: internal/llm/agent processes provider events (thinking/content deltas, tool_call start/delta/stop). Tools are invoked after an assistant message finishes with FinishReasonToolUse.

## Gaps

1) Shell does not offer a streaming execution API. Exec(ctx, cmd) buffers to completion.
2) Bash tool cannot emit partial output; it can only return a final response.
3) Agent runs tools and only creates the Tool role message after completion; there is no mechanism to update a Tool result while it’s running.
4) Parallel tool calls: tests model providers emitting multiple tool_calls concurrently, but the persistent shell is global and serialized. We must ensure we don’t run Bash commands concurrently, or we must offer an isolated shell per tool call. Today the mutex serializes Exec anyway.

## Design: minimal, compatible streaming

Add streaming without breaking existing tools by introducing optional streaming interfaces and keeping BaseTool.Run as a fallback.

### 1) Shell: ExecStream

Add a streaming variant alongside Exec:

- ExecStream(ctx, command, opts) error
- Options accept io.Writer for stdout and stderr or callbacks func([]byte) for chunk delivery.
- Chunking strategy: flush on newline or after N bytes or after a short timer (e.g., 50–100ms) to keep UI responsive but not too chatty.
- Respect context cancellation and deadlines; return context.Canceled or DeadlineExceeded; surface partial output already delivered.
- Maintain s.cwd and s.env exactly as Exec does.

Implementation sketch:

- Use io.Pipe and a custom writer that copies to both a ring buffer (for tail-on-truncation) and a callback that aggregates chunks into user-visible deltas.
- Build the mvdan runner with interp.StdIO(nil, stdoutWriter, stderrWriter).

### 2) Tool API: StreamableTool (optional)

Introduce an opt-in interface:

```go
// internal/llm/tools/tools.go

type ToolResultSink interface {
    Start(toolCallID string)
    Stdout(chunk string)
    Stderr(chunk string)
    Metadata(meta any)
    Done(exitCode int, interrupted bool, err error)
}

type StreamableTool interface {
    BaseTool
    RunStream(ctx context.Context, call ToolCall, sink ToolResultSink)
}
```

- Keep BaseTool.Run for non-streaming tools; Bash will implement RunStream and keep Run as a compatibility shim (collects chunks into a buffer and returns final content for non-streaming callers/tests).

### 3) Agent: create Tool message early and update it

In agent.streamAndHandleEvents:

- When iterating over assistantMsg.ToolCalls(), for each tool call:
  - Create (once) a Tool role message immediately with an empty ToolResult for that tool_call_id.
  - If tool implements StreamableTool, pass a sink that:
    - Appends stdout/stderr chunks to the ToolResult.Content and updates the message via messages.Update on each chunk (throttled via coalescing every ~50–150ms to avoid DB thrash).
    - Sets metadata progressively (cwd, start_time) and finally (end_time, exit_code).
    - On Done, ensure final ToolResult reflects exit status and any stderr tail, matching today’s error formatting.
  - If tool is not streamable, keep current behavior (blocking Run and then create/append ToolResult once).

Notes:
- Preserve persistent shell semantics by running Bash tool calls sequentially. Other tools can still run in parallel if/when they implement their own isolation. The Shell mutex already serializes execution; we will document that Bash streaming is serialized by design.

### 4) Truncation and formatting

- Keep MaxOutputLength enforcement. For streaming, maintain a running count and stop emitting to the UI after the limit, but still keep reading to completion to compute exit code; append a truncation notice once to the ToolResult.
- Preserve today’s combined output format: stdout first; if stderr/exit != 0, append the error block and exit code. During streaming, write stdout chunks to content; buffer a small tail for stderr (configurable bytes/lines) and attach at end, unless we decide to interleave stderr with a prefix. Document the choice.
- Always append the final <cwd>…</cwd> line after completion; include cwd and timings in ToolResult.Metadata JSON for structured consumers.

### 5) Cancellation/timeouts

- Context cancellation must immediately stop ExecStream and finalize the ToolResult with “Command was aborted before completion”.
- Timeout handling continues to use context.WithTimeout at the tool layer.

## Step-by-step implementation plan

Order is chosen to keep the build green and minimize churn.

1. Shell streaming
   - Add Shell.ExecStream(ctx, cmd, onStdout, onStderr) in internal/shell/shell.go.
   - Extract common runner creation to a helper used by Exec and ExecStream to keep behavior identical.
   - Unit tests: ensure interleaved stdout/stderr callbacks fire, cwd/env mutate, cancellation works.

2. Tool API additions
   - Add ToolResultSink and StreamableTool to internal/llm/tools/tools.go.
   - Provide a default sink implementation used by the agent (internal only).

3. Agent streaming for tools
   - In streamAndHandleEvents, before running tools, create a Tool message when the first streamable tool starts; reuse it for multiple tool calls by appending ToolResult parts for each.
   - If StreamableTool, call RunStream with a sink wired to messages.Update. Coalesce updates on a timer to avoid excessive DB writes.
   - Preserve current behavior for non-streamable tools.
   - Keep sequential execution for Bash to respect the single persistent shell.

4. Bash tool streaming
   - Implement RunStream in internal/llm/tools/bash.go using persistentShell.ExecStream.
   - Keep current permission checks, timeouts, and banned commands.
   - Emit stdout chunks via sink.Stdout; collect stderr to a bounded tail buffer; on Done, attach exit code and stderr (if any) as today; append <cwd>…</cwd>.
   - Keep Run as a thin wrapper that aggregates chunks and returns one ToolResponse to preserve existing behavior/tests.

5. UX polish and docs
   - Small rate-limit/coalescing for DB updates (e.g., 100ms) to keep UI smooth.
   - Ensure TUI tool tiles update live (they already call SetToolResult when tool messages update).
   - Document behavior, limits, and concurrency caveats (this file).

6. Tests
   - Unit tests for Shell.ExecStream (stdout/stderr interleaving, cancellation, exit codes).
   - Agent integration test: mock a StreamableTool that emits N chunks; assert intermediate DB updates happen and the final ToolResult content matches concatenation.
   - E2E: adapt or add a scenario that runs `bash -c "for i in 1 2 3; do echo $i; sleep 0.05; done"` and asserts incremental updates.

## Notes on parallel tool calls

- Provider may emit multiple tool_calls concurrently; we keep the UI responsive with separate tiles. Bash execution remains serialized due to the single persistent shell; executing two bash commands concurrently would corrupt shared state (cwd, env). We will run bash tool calls one at a time; other tools may implement their own parallelism.

## Data model and metadata

- ToolResult.Content: live stdout stream; final message includes stderr and exit code formatting aligned with current behavior.
- ToolResult.Metadata (JSON):
  - {"start_time": ms, "end_time": ms, "working_directory": "…", "exit_code": n, "stderr_bytes": n, "truncated": bool}

## Acceptance criteria

- While a long-running bash command executes, users see output stream into the tool result tile.
- Canceling the request stops the command and finalizes the tool result with an aborted message.
- Final content and metadata match the existing non-streaming semantics (including <cwd> tag), aside from interleaved partial output.
- No regressions for non-streaming tools.

## Open questions

- Interleaving stderr: interleave (with a prefix) vs tail-only at end. Proposed: tail-only (compatibility) with count in metadata; can revisit.
- Global vs per-session persistent shells: today’s singleton is shared across conversations; this doc assumes status quo.

## References

- internal/llm/tools/bash.go — current synchronous implementation
- internal/shell/shell.go — Exec buffers, no streaming hooks
- internal/llm/agent/agent.go — event processing and tool execution
- internal/tui/components/chat/messages/tool.go — UI supports SetToolResult for updates
