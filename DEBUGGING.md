# Debugging Guide (Crush)

This document summarizes all the debug signals, logs, and steps to diagnose UI “weird states” (pending forever, missing tool updates, dropped events), with special focus on MCP tool calls.

## Turn on full debug

Run Crush with all debugging signals on:

```bash
go run . --debug
```

This enables:
- Provider + MCP wire logging
- Backend→UI event logging (every pubsub event)
- UI overlays (ToolCall overlays, status bar drops badge, MCP sidebar per‑server drops)

## Where to look

- UI quick signals (visible in debug mode):
  - Status bar: red `DROPS: N` badge (global pubsub drops)
  - Status bar: `SID: <session_id>` (current session id)
  - MCP sidebar (right pane): red `DROPS: N` next to each server with drops
  - ToolCall overlay (both pending and finished):
    - `id=<tool_call_id> state=<pending|live|result|cancelled|error> [elapsed=..] [remaining=..] [drops]`

- Logs (all JSONL):
  - Backend→UI: `.crush/logs/ui/ui.log`
    - Every event routed to the TUI: `{ts, unix_ms, topic, type, payload}`
  - MCP wire: `.crush/logs/mcp/mcp.log` (single‐file mode) or per‐server rotated files depending on config
    - Key entries: `init`, `list_tools`, `call_start`, `progress`, `in`, `err`, `state`
  - Pubsub (in app log): `pubsub.drop` entries (per dropped event)

## Common causes of “pending forever”

- Final ToolResult dropped (slow consumer / buffer overflow) → spinner stops only on ToolResult/Cancelled
- Out‐of‐order or missing MCP notifications (rare after refactor)
- UI slow frames under heavy content

## How to diagnose a specific run

1) Identify the session
   - Look at the status bar: `SID: <session_id>`

2) Correlate ToolCall
   - Note `tool_call_id` from overlay
   - Grep UI log for that id:
     - `rg -n "<tool_call_id>" .crush/logs/ui/ui.log`
   - You should see:
     - `topic:"messages" type:"updated"` with `tool_result` for your `tool_call_id`
     - `topic:"coderAgent" type:"updated"` with `tool_state` updates (if streamed)

3) Check MCP wire (server side)
   - For server “Test openai_research mcp server”:
     - `rg -n "call_start|progress|in|err|state" .crush/logs/mcp/mcp.log`
     - Confirm the call finished (either `in` or `err`) for your `tool_call_id`

4) Inspect pubsub drops
   - In app log (slog output), look for `pubsub.drop` lines; cross‐check `unix_ms` against ui.log event times
   - Per‐server drops badge in the sidebar shows `DROPS: N` for `mcp:<server>`

5) Slow consumer forwarder
   - The forwarder logs `message dropped due to slow consumer` and attributes topic best‑effort:
     - For MCP events: `mcp:<server_name>`

## What to look for in the logs

- UI log: tool_result event present?
  - YES: Was there a drop after this point? If overlay still spinning, UI order/timing issue
  - NO: Look for pubsub.drop near when the result should have arrived
- MCP wire log: did call finish (`in` or `err`)?
  - YES but no tool_result → agent path lost result (rare); file a bug with timestamps
  - NO → server might have hung

## Mitigations & guardrails (already enabled)

- Global/per‑topic drop counters and warnings
- ToolCall overlays display `drops` mark if any drops occurred since creation
- MCP per‑server drops shown in sidebar

## Pitfalls

- Very large tool outputs can slow frames; we now truncate overly long outputs safely
- Spinners stop on ToolResult/Cancelled; ToolState(PhaseDone) alone does not stop the spinner by design

## Quick snippets

```bash
# Find your session id (from status bar), then:
rg -n "\"session_id\": \"<SID>\"" .crush/logs/ui/ui.log
# Find drops around a time window:
rg -n "pubsub.drop" $(fd -a "*.log" .crush/logs)
# Filter for a server:
rg -n "topic.*mcp:Test openai_research mcp server" .crush/logs/ui/ui.log
```

---

## CLI‑only operations (for LLM agents)

These commands avoid interactive UI and operate purely from the shell. Default data dir is `.crush/`; adjust if you customized `options.data_directory`.

```bash
# Set DB path (run from your project root)
DB=".crush/crush.db"
```

### Where is the DB and config, and how config is resolved

- SQLite DB path: `<data_directory>/crush.db` (defaults to `.crush/crush.db` under your current working directory)
- Logs: `<data_directory>/logs/`
- Effective config loads in this order (later wins):
  1) Global config: `$XDG_CONFIG_HOME/crush/crush.json` or `$HOME/.config/crush/crush.json` (Windows: `%LOCALAPPDATA%/crush/crush.json`)
  2) Global data override: `$XDG_DATA_HOME/crush/crush.json` or `$HOME/.local/share/crush/crush.json` (Windows: `%LOCALAPPDATA%/crush/crush.json`)
  3) Project config: `<cwd>/crush.json`
  4) Project config: `<cwd>/.crush.json`

Use the built-in command to see exactly which files were considered and which were loaded:

```bash
# Prints the layering trace to stderr and the redacted effective JSON to stdout
crush --dump-config

# Or with explicit working directory and debug
crush -c /path/to/project --debug --dump-config
```

The layering trace marks with:
- [+] path → loaded and merged at that step
- [-] path → not present

- List recent sessions

```bash
sqlite3 -header -column "$DB" \
  "SELECT id, title, datetime(created_at,'unixepoch','localtime') AS created
     FROM sessions
     ORDER BY created_at DESC
     LIMIT 20;"
```

- Look up session by title (substring match)

```bash
# Replace SEARCH_HERE with your substring
sqlite3 -header -column "$DB" \
  "SELECT id, title, datetime(created_at,'unixepoch','localtime') AS created
     FROM sessions
     WHERE title LIKE '%SEARCH_HERE%'
     ORDER BY created_at DESC;"
```

- Look up session by message snippet (search JSON `parts`)

```bash
# Replace SNIPPET_HERE with your substring
sqlite3 -header -column "$DB" \
  "SELECT DISTINCT s.id, s.title, datetime(s.created_at,'unixepoch','localtime') AS created
     FROM messages m
     JOIN sessions s ON s.id = m.session_id
     WHERE m.parts LIKE '%SNIPPET_HERE%'
     ORDER BY s.created_at DESC;"
```

- Dump a session transcript (raw roles + JSON parts)

```bash
# Replace <SID> with a session id
sqlite3 -header -column "$DB" \
  "SELECT role, parts, datetime(created_at,'unixepoch','localtime') AS created
     FROM messages
     WHERE session_id = '<SID>'
     ORDER BY created_at ASC;"
```

Tip: If you need pretty JSON for `parts`, pipe through `jq -r '. | fromjson? // .'` per row, or post‑process with a small script.

### Handy SQLite snippets

```bash
# Recent messages across sessions
sqlite3 -header -column "$DB" \
  "SELECT m.session_id, m.role, substr(m.parts,1,120) AS parts_head, datetime(m.created_at,'unixepoch','localtime') AS created
     FROM messages m
     ORDER BY m.created_at DESC
     LIMIT 50;"

# Sessions that have drops in UI log timeframe (approximate; correlate with ui.log)
# First list sessions with created time window
sqlite3 -header -column "$DB" \
  "SELECT id, title, datetime(created_at,'unixepoch','localtime') AS created
     FROM sessions
     WHERE created_at >= strftime('%s','-2 days')
     ORDER BY created_at DESC;"
```

---

## pprof quickstart (CLI)

Crush exposes `net/http/pprof` on localhost:6060 when `CRUSH_PROFILE` is set.

- Start Crush with profiling enabled

```bash
# Option A: Task helper (serves pprof at :6060)
task dev

# Option B: Manual
CRUSH_PROFILE=1 go run .
```

- CPU profile (10s) with local web UI

```bash
go tool pprof -http :6061 'http://localhost:6060/debug/pprof/profile?seconds=10'
```

- Heap / allocations profiles

```bash
# Heap snapshot
go tool pprof -http :6061 'http://localhost:6060/debug/pprof/heap'
# Allocations (cumulative)
go tool pprof -http :6061 'http://localhost:6060/debug/pprof/allocs'
```

- Terminal‑only usage

```bash
# Top offenders (heap)
go tool pprof -top 'http://localhost:6060/debug/pprof/heap'
# 30s CPU capture, then interactive shell (type 'top', 'list <func>', 'web')
go tool pprof 'http://localhost:6060/debug/pprof/profile?seconds=30'
```

- Quick goroutine dump

```bash
curl -s 'http://localhost:6060/debug/pprof/goroutine?debug=2' | sed -n '1,200p'
```

- Helpful Task shortcuts (open web UI on :6061)

```bash
task profile:cpu     # 10s CPU
task profile:heap    # heap snapshot
task profile:allocs  # allocations
```
