# Postmortem: MCP SSE stream closed (broken pipe)

## Summary
- Symptom: During MCP tool use, UI showed a permission dialog but no tool execution card; the input stayed “Working…”.
- Root cause: The MCP SSE client connection was started with a short‑lived context (the initialization timeout). After initialize completed, that context was canceled, which immediately closed the SSE stream. The server then attempted to deliver messages into a closed writer, and the client never received a tool response.
- Fix: Start the MCP client with a long‑lived context so SSE remains connected; keep the short timeout only for `Initialize` and `ListTools`. Improve wire visibility and apply buffered ToolState in the UI so live status appears as soon as the tool card exists.

## Impact
- Affects any MCP server when used over SSE transport
- User experience: Permission prompt appears, but no tool card/result; chat input remains “Working…”

## Evidence
- MCP wire log (client): `~/.crush/logs/mcp/<name>.log`
  - Repeated SSE init followed by immediate stream close:
    - `{"event":"init","transport":"sse"}`
    - `{"direction":"sse","payload":{"line":"SSE stream error: context canceled"}}`
  - Tool call breadcrumbs:
    - `{"event":"call_start","tool":"...","tool_call_id":"call_..."}`

- MCP server logs (generic)
  - The server attempted to post messages into a closed SSE session. Example signature:
    - Python/Starlette: `anyio.ClosedResourceError` in `mcp/server/sse.py: handle_post_message`
  - Where to check depends on how your server is run (supervisor, container logs, process stderr).

- Crush app logs: `~/.crush/logs/crush-*.log`
  - Model planned tool use: `Tool call started` for `mcp_<server>_<tool>`
  - No subsequent tool result persisted while UI stayed pending

## Root cause
- We reused the same (timeout‑bounded) context for connection, initialization, and listing tools. For SSE transports, the underlying stream must stay alive beyond initialization; canceling the context after init terminated the stream (“context canceled”), leaving the server with a dead writer and the client waiting indefinitely.
- MCP lifecycle reference (Initialize + notifications/initialized):
  https://modelcontextprotocol.io/specification/2024-11-05/basic/lifecycle

## Fixes
1) Keep SSE connection alive
- Code: `internal/llm/agent/mcp-tools.go`
- Start the MCP client with a long‑lived context so SSE remains connected beyond the init timeout:
  - `c.Start(context.Background())`
- Continue using the short timeout only for `Initialize` and `ListTools`.
- Shutdown: Crush calls `agent.CloseMCPClients()` during app shutdown, closing clients and streams cleanly.

2) Improve wire visibility
- Ensure per‑MCP wire logger is used at the transport (stdio/http/sse) and tool layers so logs always include:
  - init/list/state/call_start events
  - raw server lines (for stdio)
  - incoming tool response payloads ("in" entries)

3) UI live status resilience
- Code: `internal/tui/components/chat/chat.go`
- Apply any buffered ToolState to tool cards created during message updates (not just initial render), including nested Agent tool children.
- Log when ToolState is buffered to help diagnose event timing.

## Validation steps
1) Ensure Crush is configured for your MCP over SSE, for example:
- `~/.config/crush/crush.json`
  ```json
  {
    "mcp": {
      "<name>": { "type": "sse", "url": "http://127.0.0.1:8000/sse" }
    }
  }
  ```

2) Run a simple tool call in a new session
- Example: `mcp_<name>_<tool> {"param":"value"}`

3) Observe
- UI: a tool card appears under the assistant turn; under the spinner, see a live line such as “Waiting for MCP server response…”.
- Wire log: `~/.crush/logs/mcp/<name>.log`
  - `{"event":"init","transport":"sse"}` without an immediate `"SSE stream error: context canceled"`
  - `{"event":"call_start","tool":"...","tool_call_id":"..."}` followed by an "in" entry with the tool response
- Server logs: No `ClosedResourceError` (or equivalent) for this session

## Troubleshooting
- No tool card but live logs show `call_start`:
  - Check app log for `buffering tool state` lines; if present but no card, verify the assistant message did update with `tool_use` (UI adds the card on message update).
- No tool response after `call_start`:
  - Inspect the MCP server for exceptions; confirm the SSE session is connected and stable.
- stdio transport
  - If switching to stdio, ensure the MCP process environment has all required dependencies; otherwise the process may exit and lead to broken pipes.

## Files touched
- `internal/llm/agent/mcp-tools.go`
  - Start with a long‑lived context for `Start()` (SSE keepalive)
  - Ensure per‑MCP wire logger is provided to tools
- `internal/tui/components/chat/chat.go`
  - Apply buffered ToolState for new/updated tool calls (including nested)
  - Add a debug log for buffered ToolState

All tests pass (`go test ./...`).
