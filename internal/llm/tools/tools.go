package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/permission"
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

// WrapTextWithMeta creates a text ToolResponse and attaches metadata.
func WrapTextWithMeta(text string, meta any) (ToolResponse, error) {
	return WithResponseMetadata(NewTextResponse(text), meta), nil
}

// resolveAbs converts a potentially relative path to an absolute one using workingDir.
func resolveAbs(workingDir, p string) string {
	if filepath.IsAbs(p) { return p }
	return filepath.Join(workingDir, p)
}

// outsideWorkingDir reports whether absPath lies outside absWorkingDir.
func outsideWorkingDir(absWorkingDir, absPath string) bool {
	rel, err := filepath.Rel(absWorkingDir, absPath)
	if err != nil { return true }
	return strings.HasPrefix(rel, "..")
}

// requestPathPermission centralizes permission prompts for path access.
func requestPathPermission(ctx context.Context, svc permission.Service, call ToolCall, toolName, action, path, description string, params any) (bool, error) {
	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return false, fmt.Errorf("session ID and message ID are required for permission request")
	}
	ok := svc.Request(permission.CreatePermissionRequest{
		SessionID:   sessionID,
		Path:        path,
		ToolCallID:  call.ID,
		ToolName:    toolName,
		Action:      action,
		Description: description,
		Params:      params,
	})
	return ok, nil
}

// recordHistory stores a new version and intermediate version if needed.
func recordHistory(ctx context.Context, files history.Service, sessionID, filePath, oldContent, newContent string) error {
	file, err := files.GetByPathAndSession(ctx, filePath, sessionID)
	if err != nil {
		if _, err := files.Create(ctx, sessionID, filePath, oldContent); err != nil {
			return fmt.Errorf("error creating file history: %w", err)
		}
	} else if file.Content != oldContent {
		_, _ = files.CreateVersion(ctx, sessionID, filePath, oldContent)
	}
	_, _ = files.CreateVersion(ctx, sessionID, filePath, newContent)
	return nil
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
