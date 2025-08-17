package tools

import (
	"context"
	"encoding/json"
	"time"
)

type ToolInfo struct {
	Name        string
	Description string
	Parameters  map[string]any
	Required    []string
}

type toolResponseType string

type (
	sessionIDContextKey string
	messageIDContextKey string
)

const (
	ToolResponseTypeText  toolResponseType = "text"
	ToolResponseTypeImage toolResponseType = "image"

	SessionIDContextKey sessionIDContextKey = "session_id"
	MessageIDContextKey messageIDContextKey = "message_id"
)

type ToolResponse struct {
	Type     toolResponseType `json:"type"`
	Content  string           `json:"content"`
	Metadata string           `json:"metadata,omitempty"`
	IsError  bool             `json:"is_error"`
}

// Streaming tool state primitives (minimal model)

type ToolPhase string

const (
	PhaseStarting   ToolPhase = "starting"
	PhaseRunning    ToolPhase = "running"
	PhaseWaiting    ToolPhase = "waiting"
	PhaseFinalizing ToolPhase = "finalizing"
	PhaseDone       ToolPhase = "done"
	PhaseError      ToolPhase = "error"
)

type ToolState struct {
	Phase     ToolPhase      `json:"phase"`
	Title     string         `json:"title,omitempty"`
	Detail    string         `json:"detail,omitempty"`
	StartedAt int64          `json:"started_at_ms,omitempty"`
	UpdatedAt int64          `json:"updated_at_ms,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

type ToolSink interface {
	Update(state ToolState)
	Final(result ToolResponse)
	Error(err error)
}

// Context plumbing for giving tools access to the sink without changing BaseTool.Run

type toolSinkContextKey string

const ToolSinkContextKey toolSinkContextKey = "tool_sink"

func WithSink(ctx context.Context, sink ToolSink) context.Context {
	return context.WithValue(ctx, ToolSinkContextKey, sink)
}

func SinkFromContext(ctx context.Context) ToolSink {
	if v := ctx.Value(ToolSinkContextKey); v != nil {
		if s, ok := v.(ToolSink); ok {
			return s
		}
	}
	return &noopSink{}
}

type noopSink struct{}

func (n *noopSink) Update(state ToolState)    {}
func (n *noopSink) Final(result ToolResponse) {}
func (n *noopSink) Error(err error)           {}

func NowMillis() int64 { return time.Now().UnixMilli() }

func NewTextResponse(content string) ToolResponse {
	return ToolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
	}
}

func WithResponseMetadata(response ToolResponse, metadata any) ToolResponse {
	if metadata != nil {
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			return response
		}
		response.Metadata = string(metadataBytes)
	}
	return response
}

func NewTextErrorResponse(content string) ToolResponse {
	return ToolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
		IsError: true,
	}
}

type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

type BaseTool interface {
	Info() ToolInfo
	Name() string
	Run(ctx context.Context, params ToolCall) (ToolResponse, error)
}

type StreamTool interface {
	Info() ToolInfo
	Name() string
	RunStream(ctx context.Context, params ToolCall, sink ToolSink) error
}

func GetContextValues(ctx context.Context) (string, string) {
	sessionID := ctx.Value(SessionIDContextKey)
	messageID := ctx.Value(MessageIDContextKey)
	if sessionID == nil {
		return "", ""
	}
	if messageID == nil {
		return sessionID.(string), ""
	}
	return sessionID.(string), messageID.(string)
}
