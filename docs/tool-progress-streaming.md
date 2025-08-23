# Tool progress streaming (generic) — Bash as an example

Status: Implemented (Bash example; generic tool progress in place)

This document extends the Bash streaming plan with a generalized mechanism for tools to report visible progress in the UI, particularly when they are waiting on diagnostics or other asynchronous work.

## Scope

This document now describes the generic tool progress (Intermediate Tool State, ITS) as implemented, using Bash as one example.

- Bash tool: streams stdout/stderr to final result (not a live stream to the UI yet), and publishes structured live state via ITS.
- Generic: any tool may publish structured state to update the UI while executing (e.g., waiting for diagnostics).
- Non-streaming compatibility: tools that don’t opt-in still work; they simply won’t update live state.

## Goals

- Show visible progress for long-running tools (Bash, Edit/MultiEdit/Write, Diagnostics).
- Provide a standard, lightweight way for tools to surface progress states to the UI while running, including:
  - "starting …"
  - "working …"
  - "waiting for diagnostics …"
  - "finalizing …"
- Keep backward compatibility with non-streaming tools.

## Structured Intermediate Tool State (ITS)

Replace ad-hoc status strings with a structured, typed state model that tools emit while running. This enables consistent rendering across TUI/clients and richer context (phase, subject, progress, diagnostics info).

### Types (proposed)

```go
// internal/llm/tools/tools.go

type ToolPhase string

const (
    PhaseStarting   ToolPhase = "starting"
    PhaseRunning    ToolPhase = "running"
    PhaseWaiting    ToolPhase = "waiting"
    PhaseFinalizing ToolPhase = "finalizing"
    PhaseDone       ToolPhase = "done"
    PhaseError      ToolPhase = "error"
)

type ProgressKind string

const (
    ProgressIndeterminate ProgressKind = "indeterminate"
    ProgressPercent       ProgressKind = "percent"     // use Percent
    ProgressCounter       ProgressKind = "counter"     // use Current/Total
)

type ToolProgress struct {
    Kind    ProgressKind `json:"kind"`
    Percent float64      `json:"percent,omitempty"`  // 0–100
    Current int          `json:"current,omitempty"`
    Total   int          `json:"total,omitempty"`
}

type ToolSubject struct {
    Kind  string `json:"kind"`  // e.g. "file", "command", "uri"
    Value string `json:"value"` // absolute path, shell line, url, etc.
}

type DiagnosticsWait struct {
    FilePath string   `json:"file_path"`
    Clients  []string `json:"clients,omitempty"`
}

type ToolState struct {
    Phase       ToolPhase        `json:"phase"`
    Title       string           `json:"title,omitempty"`   // short summary
    Detail      string           `json:"detail,omitempty"`  // optional extra context
    Subject     *ToolSubject     `json:"subject,omitempty"`
    Progress    *ToolProgress    `json:"progress,omitempty"`
    Diagnostics *DiagnosticsWait `json:"diagnostics,omitempty"`
    StartedAtMs int64            `json:"started_at_ms,omitempty"`
    UpdatedAtMs int64            `json:"updated_at_ms,omitempty"`
    Meta        map[string]any   `json:"meta,omitempty"`
}

// The sink replaces string-based progress with a structured state update.
type ToolStateSink interface {
    SetState(state ToolState)
}

// Streamable tools receive a ToolResultSink that embeds ToolStateSink; non-streaming tools get a minimal state sink.
```

### Metadata wiring

- Tool messages store the latest ITS as ToolResult.Metadata.intermediate_state (JSON object)
- Optionally keep a short ring-buffer of past states (intermediate_history) with timestamps for debugging/telemetry
- UI reads the ITS to render phase, title, subject, and progress bar/indicator

### Example (waiting for diagnostics)

```json
{
  "intermediate_state": {
    "phase": "waiting",
    "title": "Waiting for diagnostics…",
    "subject": {"kind": "file", "value": "/path/to/foo.go"},
    "diagnostics": {"file_path": "/path/to/foo.go", "clients": ["gopls"]},
    "updated_at_ms": 1754957000123
  }
}
```

### Agent integration

- When executing a tool, the agent constructs a ToolStateSink bound to tool_call_id
- Coalesce SetState updates (100–200ms) to avoid DB thrash
- Persist only the latest state in metadata; optionally retain the last N in a separate history field

### Tool authoring guidelines

- Bash:
  - starting → running (indeterminate progress; subject = command)
  - finalizing → done (attach exit code/cwd in final ToolResult metadata)
- Edit/Write/MultiEdit:
  - running (writing file) → waiting (diagnostics {file_path, clients}) → done
- Diagnostics tool:
  - starting → waiting (diagnostics {file_path}) → done

### UI rendering

- Show Title beneath the tool tile header; if Progress present, render a progress bar or spinner
- If Subject.Kind == "file", render the basename and truncate path; if "command", render a muted code snippet
- For Waiting with Diagnostics, display a subtle "waiting for diagnostics" line with client names

This ITS replaces the earlier string-only progress idea and gives a future-proof, consistent way to visualize tool activity.

## Agent integration

- When executing a tool (streaming or not), the agent will construct a progress sink bound to the tool_call_id.
- Progress updates are coalesced (e.g., 100–200ms) to avoid DB thrash.
- Status is stored in ToolResult.Metadata, e.g.:

```json
{
  "status": {
    "text": "waiting for diagnostics…",
    "detail": "go/gopls",
    "updated_at": 1754957000
  }
}
```

- The TUI renders status in the tool tile footer or a subtle line beneath the title.

## Diagnostics-specific progress

Tools that trigger diagnostics (edit, write, multiedit, diagnostics itself) should:

- SetStatus("waiting for diagnostics…") immediately after file changes are written (or when an explicit diagnostics fetch starts)
- Clear the status when lsp.WaitForDiagnostics returns or context is done
- Optionally include the LSP name(s) in SetDetail when available

Example (pseudocode within a tool):

```go
sink.SetStatus("waiting for diagnostics…")
lsp.WaitForDiagnostics(ctx, filePath, clients)
sink.SetStatus("") // clear
```

For non-streaming tools today, the agent will pass a minimal progress sink; later we can retrofit tools to call SetStatus directly.

## UI

- Tool tiles: add an optional, muted status line beneath the tool name or result header.
- Header: when a tool is running and publishes a status, optionally surface the most recent one in the header’s activity area (future polish).

## Backward compatibility

- Tools that do not use the progress sink continue to work.
- Streaming remains opt-in; ProgressSink works with or without streaming.

## Future work

- Add a progress timeline to show transitions (started → running → waiting for diagnostics → done)
- Include percentage when meaningful (downloads, batch operations)
- Expose status changes over SSE for external clients

## Acceptance criteria

- Bash tool streams output as designed
- Edit/Write/MultiEdit/Diagnostics show "waiting for diagnostics…" while polling
- Status appears and clears appropriately in the UI without flicker
- No regressions for non-streaming tools
