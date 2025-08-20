package transform

import (
	"context"
)

// Phase denotes the lifecycle interception point. v1: post_sample only.
type Phase string

const (
	PhasePostSample Phase = "post_sample"
)

// PartType mirrors internal/message part wrappers. Only a subset is allowed in outputs.
type PartType string

const (
	PartText     PartType = "text"
	PartToolCall PartType = "tool_call"
	PartToolRes  PartType = "tool_result" // history only; not allowed in ReplaceWith
	PartFinish   PartType = "finish"      // history only; not allowed in ReplaceWith
)

// PartWrapper is a generic wrapper used for history/message parts.
type PartWrapper struct {
	Type PartType `json:"type"`
	Data any      `json:"data"`
}

// MessageDTO is a wire-friendly message shape (subset of internal/message.Message).
type MessageDTO struct {
	ID        string        `json:"id,omitempty"`
	Role      string        `json:"role"` // "assistant"|"system"|"user"|"tool"
	Model     string        `json:"model,omitempty"`
	Provider  string        `json:"provider,omitempty"`
	CreatedAt int64         `json:"created_at,omitempty"`
	Parts     []PartWrapper `json:"parts"`
}

// HistoryDTO is the full conversation view as seen by the assistant.
type HistoryDTO struct {
	Messages []MessageDTO `json:"messages"`
}

// Request is sent to the transformer MCP hook (crush_hook.on_sampling).
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

// Plan is the action to apply.
type Plan struct {
	ReplaceWith []MessageDTO `json:"replace_with"`
	Next        string       `json:"next,omitempty"`   // "assistant_sampling"|"yield_to_user"
	UIInfo      string       `json:"ui_info,omitempty"` // ephemeral UI notice
}

// Response is the transformer output.
type Response struct {
	Plan   Plan   `json:"plan"`
	Reason string `json:"reason,omitempty"`
}

// Client defines the transformer API.
type Client interface {
	// OnSampling is invoked after provider sampling (post_sample) and before tool execution.
	OnSampling(ctx context.Context, req Request) (Response, error)
}

// NoopClient returns identity plans; useful as a default when no transformer is configured.
type NoopClient struct{}

func NewNoopClient() *NoopClient { return &NoopClient{} }

func (c *NoopClient) OnSampling(_ context.Context, req Request) (Response, error) {
	// Identity: keep the sampled assistant message.
	return Response{
		Plan: Plan{ReplaceWith: []MessageDTO{req.Sampled.Message}},
	}, nil
}
