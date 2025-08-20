# MCP Sequence Transformer — Post-sample Replace Hook (v1)

Status: Draft (proposal)
Owner: mpokorny

## Summary
A single MCP “Sequence Transformer” runs immediately after we sample an assistant message from the LLM provider (post-sample), before any tools execute. It can replace the sampled message with one or more messages and steer who goes next (assistant vs user). Optionally it can emit UI-only info lines (not persisted; not fed back to the model) and we show a visible “Hook running” status in the UI while it executes.

v1 keeps the surface area small:
- One phase: post_sample (after provider complete; before tool execution).
- Replace-only: you may replace the sampled assistant message with 1+ messages.
- Next-step control: next is assistant_sampling or yield_to_user; next applies after tool execution if the replacement includes tool calls.
- Full history is always sent (no truncation).
- On any error/timeout: identity (keep the sampled message).

## Why this shape
This covers the common “guardrail/nudge/fix-up” cases while avoiding complex edit semantics and execution-time races. It also enables starting tool execution by placing ToolCall parts into the replacement messages.

## Configuration (crush.json)
```json
{
  "mcp": {
    "servers": [
      {
        "name": "policy-transformer",
        "command": "./bin/mcp-transformer",
        "handles_sequence_transform": true,
        "transform": {
          "timeout_ms": 2500
        }
      }
    ]
  }
}
```
Semantics
- Exactly one server may set handles_sequence_transform=true.
- timeout_ms is per-call; on timeout we act as identity.

## Transformer MCP contract
Tool: sequence.on_transform
- Phase: "post_sample" (always).

Input (JSON)
```json
{
  "session_id": "<uuid>",
  "phase": "post_sample",
  "committed": {
    "history": { "messages": [/* FULL session history; same shape as storage (type+data parts) */] }
  },
  "sampled": {
    "message": {
      "id": "<uuid>",
      "role": "assistant",
      "model": "<string>",
      "provider": "<string>",
      "created_at": 1724000000,
      "parts": [
        { "type": "text", "data": { "text": "…" } },
        { "type": "tool_call", "data": { "id": "tc_…", "name": "bash", "input": "{…}", "type": "function", "finished": true } }
      ]
    }
  }
}
```
Notes
- committed.history is the full persisted session (including the sampled message).
- sampled.message is the exact assistant message we just persisted from the provider.

Output (JSON)
```json
{
  "plan": {
    "replace_with": [
      { "role": "assistant", "parts": [ { "type": "text", "data": { "text": "…" } } ] },
      { "role": "system",    "parts": [ { "type": "text", "data": { "text": "…" } } ] }
      // Allowed parts in v1: text and tool_call (to initiate tool execution).
    ],
    "next": "assistant_sampling" | "yield_to_user",
    "ui_info": ["hook ran successfully", "…"]
  },
  "reason": "optional short explanation"
}
```
Semantics
- replace_with: required; length >= 1. These messages replace the sampled assistant message atomically. Allowed roles: assistant, system. Allowed parts (v1): text, tool_call. If any replacement contains tool_call parts, we will execute them next (permission prompts still apply) and only then honor `next`.
- next: explicit instruction for who goes next after tool execution (if any). If omitted: default follow provider’s normal control flow.
- ui_info: ephemeral lines displayed in UI; not persisted and not included in future model context.

Identity
- Always transform. Identity is returning `replace_with` as a single assistant message identical to `sampled.message` and omitting next/ui_info.

Validation rules (v1)
- replace_with must be non-empty; if invalid or empty we treat as identity.
- Roles allowed: assistant, system only.
- Part types allowed: text, tool_call only; others are ignored with a warning.
- ToolCall IDs in replacements must be unique within the replacement set.
- Any tool_call introduces a normal tool execution cycle with standard permission prompts.

## Runtime wiring in Crush
Where to hook: internal/llm/agent/agent.go

Flow
1) Provider completes; we persist the sampled assistant message.
2) Display a "HOOK: running…" status in the UI and call sequence.on_transform with {phase: post_sample, committed.history, sampled.message}.
3) On success: atomically replace the sampled message in DB with `plan.replace_with` in order (preserve created_at ordering by inserting at the same position). For UI, emit an event that the message was replaced (so the spinner/overlay updates rather than flicker-delete).
4) If any replacement contains tool_call parts, proceed to execute tools as usual; permission prompts apply. After tool execution, honor `plan.next` if provided (assistant_sampling vs yield_to_user), else normal flow.
5) Emit `ui_info` lines to the UI as ephemeral notices.
6) On timeout/error: keep the sampled message and proceed normally (identity).

Observability & UI
- UI status: show a transient "HOOK running" indicator while the MCP call is in-flight; on completion, show a short success/failure toast using ui_info/reason.
- Logs (slog): session_id, phase, sampled_message_id, replace_count, has_tools (bool), next, latency_ms; wire logs for MCP call present under .crush/logs/mcp/…

Backwards compatibility
- Off by default; enabled only when exactly one server has handles_sequence_transform=true.
- Full history is already sent to providers; sending it to this hook does not change privacy posture in this project.

## Examples
Example A — Identity
```json
{
  "plan": {
    "replace_with": [
      { "role": "assistant", "parts": [ { "type": "text", "data": { "text": "…(same as sampled)…" } } ] }
    ]
  }
}
```

Example B — Guardrail rewrite with tool
- Replace the sampled message with a safer tool invocation and a system reminder; then keep control with the assistant afterwards:
```json
{
  "plan": {
    "replace_with": [
      { "role": "system",    "parts": [ { "type": "text", "data": { "text": "Do not run destructive shell commands. Propose safer alternatives and ask for explicit confirmation." } } ] },
      { "role": "assistant", "parts": [
        { "type": "text", "data": { "text": "Acknowledged. I’ll use a non-destructive check first." } },
        { "type": "tool_call", "data": { "id": "tc_check", "name": "bash", "input": "{\"cmd\":\"echo 'dry run'\"}", "type": "function", "finished": true } }
      ]}
    ],
    "next": "assistant_sampling",
    "ui_info": ["policy-transformer applied rewrite"]
  }
}
```
Effect: we replace the message, execute the bash tool (permission prompt included), then resume with assistant sampling with the system reminder in context.

Example C — Replace-with two assistant messages (no tools) and yield
```json
{
  "plan": {
    "replace_with": [
      { "role": "assistant", "parts": [ { "type": "text", "data": { "text": "Quick correction: the prior suggestion had an issue…" } } ] },
      { "role": "assistant", "parts": [ { "type": "text", "data": { "text": "Here’s the corrected plan in 3 steps…" } } ] }
    ],
    "next": "yield_to_user"
  }
}
```

## Acceptance criteria
- After provider sampling and before tool execution, we invoke the transformer; when it responds, the sampled message is replaced with N≥1 messages per plan.
- Replacements may contain tool_call parts; those tools execute with normal permission prompts. After tool completion, `next` is respected.
- UI shows a short-lived “HOOK running” status; `ui_info` is displayed as ephemeral notices.
- On transformer error/timeout we preserve the sampled message and continue normally (identity).
