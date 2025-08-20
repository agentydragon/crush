# Sequence Transformer API (v1 — Post-sample Replace Hook)

This document specifies the contract between Crush and the MCP transformer server, and the internal Go-facing types for invoking it.

Tool name (MCP):
- crush_hook.on_sampling
- Timeout: from config (default 2500 ms)
- Call pattern: unary (no streaming)

Phase:
- post_sample (single phase in v1)

## MCP request/response (JSON)

Request:
```json
{
  "session_id": "<uuid>",
  "phase": "post_sample",
  "committed": {
    "history": {
      "messages": [
        { "id": "<uuid>", "role": "user|assistant|tool|system", "model": "<string>", "provider": "<string>", "created_at": 1724000000, "parts": [
          { "type": "text", "data": { "text": "..." } },
          { "type": "tool_call", "data": { "id": "tc_…", "name": "bash", "input": "{…}", "type": "function", "finished": true } },
          { "type": "tool_result", "data": { "tool_call_id": "tc_…", "content": "...", "is_error": false } },
          { "type": "finish", "data": { "reason": "end_turn", "time": 1724000000 } }
        ]}
      ]}
    }
  },
  "sampled": {
    "message": { /* the just-persisted assistant message; same shape as above */ }
  }
}
```
Notes
- history.messages parts use the same storage marshalling as internal/message (type+data wrappers).
- sampled.message is included and also present as the last item in committed.history.messages.

Response:
```json
{
  "plan": {
    "replace_with": [
      { "role": "assistant", "parts": [ { "type": "text", "data": { "text": "…" } } ] },
      { "role": "system",    "parts": [ { "type": "text", "data": { "text": "…" } } ] }
      // Allowed v1 parts: text, tool_call
    ],
    "next": "assistant_sampling" | "yield_to_user",
    "ui_info": "hook ran successfully"
  },
  "reason": "optional short explanation"
}
```
Rules
- replace_with: required, len>=1; roles allowed: system, assistant; parts allowed: text, tool_call.
- If any replace_with message includes tool_call parts, those tools execute next (with normal permission prompts); only after completion is `next` applied.
- On invalid response or timeout → identity (keep sampled message).

## Go DTOs (internal invocation)

These types describe the minimal request/response mapping used by the internal transformer client. The parts mirror internal/message marshalling (type+data wrappers) to avoid bespoke schemas.

```go
package transform

import (
    "context"
)

type Phase string

const (
    PhasePostSample Phase = "post_sample"
)

type PartType string

const (
    PartText     PartType = "text"
    PartToolCall PartType = "tool_call"
    PartToolRes  PartType = "tool_result" // present in history; not allowed in replace_with
    PartFinish   PartType = "finish"      // present in history; not allowed in replace_with
)

type PartWrapper struct {
    Type PartType    `json:"type"`
    Data any         `json:"data"`
}

type MessageDTO struct {
    ID        string       `json:"id,omitempty"`
    Role      string       `json:"role"` // "assistant"|"system"|"user"|"tool"
    Model     string       `json:"model,omitempty"`
    Provider  string       `json:"provider,omitempty"`
    CreatedAt int64        `json:"created_at,omitempty"`
    Parts     []PartWrapper `json:"parts"`
}

type HistoryDTO struct {
    Messages []MessageDTO `json:"messages"`
}

type Request struct {
    SessionID string `json:"session_id"`
    Phase     Phase  `json:"phase"`
    Committed struct {
        History HistoryDTO `json:"history"`
    } `json:"committed"`
    Sampled struct {
        Message MessageDTO `json:"message"`
    } `json:"sampled"`
}

type Plan struct {
    ReplaceWith []MessageDTO `json:"replace_with"`
    Next        string       `json:"next,omitempty"`   // "assistant_sampling"|"yield_to_user"
    UIInfo      string       `json:"ui_info,omitempty"`
}

type Response struct {
    Plan   Plan   `json:"plan"`
    Reason string `json:"reason,omitempty"`
}

// Client is the internal entrypoint the agent will use.
type Client interface {
    OnTransform(ctx context.Context, req Request) (Response, error)
}
```

Notes
- MessageDTO.Parts in responses must only contain text and tool_call; others will be ignored with warnings.
- When applying Plan.ReplaceWith, Crush replaces the sampled assistant message atomically with the given sequence.

## Agent integration points (high-level)

- After provider EventComplete and message persisted: build Request and call Client.OnTransform.
- While in-flight: set a UI status flag ("HOOK running").
- Apply Plan.ReplaceWith; if any message includes tool_call → execute tools; then honor Plan.Next.
- Emit UI ephemeral notices for Plan.UIInfo.

Refer to docs/PROPOSAL-sequence-transformer.md for behavior, examples, and UI/observability details.
