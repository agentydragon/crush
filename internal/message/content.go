package message

import (
	"encoding/base64"
	"slices"
	"time"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
)

type MessageRole string

const (
	Assistant MessageRole = "assistant"
	User      MessageRole = "user"
	System    MessageRole = "system"
	Tool      MessageRole = "tool"
)

type FinishReason string

const (
	FinishReasonEndTurn          FinishReason = "end_turn"
	FinishReasonMaxTokens        FinishReason = "max_tokens"
	FinishReasonToolUse          FinishReason = "tool_use"
	FinishReasonCanceled         FinishReason = "canceled"
	FinishReasonError            FinishReason = "error"
	FinishReasonPermissionDenied FinishReason = "permission_denied"

	// Should never happen
	FinishReasonUnknown FinishReason = "unknown"
)

type ContentPart interface {
	isPart()
}

type ReasoningContent struct {
	Thinking   string `json:"thinking"`
	Signature  string `json:"signature"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

func (tc ReasoningContent) String() string { return tc.Thinking }
func (ReasoningContent) isPart()           {}

type ReasoningSummaryContent struct {
	ID               string `json:"id,omitempty"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
	Summary          string `json:"summary"`
	StartedAt        int64  `json:"started_at,omitempty"`
	FinishedAt       int64  `json:"finished_at,omitempty"`
}

func (tc ReasoningSummaryContent) String() string { return tc.Summary }
func (ReasoningSummaryContent) isPart()           {}

type TextContent struct {
	Text string `json:"text"`
}

func (tc TextContent) String() string {
	return tc.Text
}

func (TextContent) isPart() {}

type ImageURLContent struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

func (iuc ImageURLContent) String() string {
	return iuc.URL
}

func (ImageURLContent) isPart() {}

type BinaryContent struct {
	Path     string
	MIMEType string
	Data     []byte
}

func (bc BinaryContent) String(p catwalk.InferenceProvider) string {
	base64Encoded := base64.StdEncoding.EncodeToString(bc.Data)
	if p == catwalk.InferenceProviderOpenAI {
		return "data:" + bc.MIMEType + ";base64," + base64Encoded
	}
	return base64Encoded
}

func (BinaryContent) isPart() {}

// Tool Call state machine (overview)
//
// A tool call flows through two distinct phases, driven by two subsystems:
//
// 1) Provider composition phase (LLM decides to call a tool)
//    - Start: provider emits ToolUseStart with a stable call ID (ToolCall.ID).
//      For OpenAI Responses this MUST be function_call.call_id (see docs/OPENAI-RESPONSES-TOOL-ID-MAPPING.md).
//    - Delta: provider streams arguments (AppendToolCallInput).
//    - Stop: provider signals the end of argument composition (FinishToolCall).
//    - Complete: provider finalizes the assistant message; ToolCall.Finished is set true.
//
//    Persisted: ToolCall parts are saved on the assistant message. This phase does NOT execute tooling.
//
// 2) Execution phase (Crush runs the tool implementation)
//    - Permission request: permission service may prompt; this is ephemeral (not persisted) and delivered via pub/sub.
//    - Running: the tool executes; progress is reported via tools.Sink (ToolState: Phase/Title/Detail). Ephemeral.
//    - Result: when the tool completes (success/error), a ToolResult is persisted in a separate tool-role message,
//      and correlated back using ToolResult.ToolCallID == ToolCall.ID.
//    - Cancel: if the request is canceled, remaining tool calls receive synthetic error/canceled ToolResults.
//
// Canonical state detection (single call):
//   - Pending (compose or execute): ToolResult absent → pending. UI should keep spinner until a ToolResult arrives
//     regardless of ToolCall.Finished (composition may have ended but execution not yet persisted).
//   - Permission requested: pending AND permissionRequested && !permissionGranted (ephemeral UI flag).
//   - Running with live status: pending AND live ToolState updates observed (ephemeral \"title/detail\").
//   - Succeeded: ToolResult present AND IsError == false.
//   - Failed:    ToolResult present AND IsError == true (Recovered distinguishes crash recovery).
//   - Canceled:  explicit cancel path (ephemeral flag used by UI); also persisted synthetic ToolResult for history.
//
// Storage vs in-memory:
//   - Persisted in DB: ToolCall (assistant message), ToolResult (tool message), Finish parts, text, etc.
//   - Not persisted: permissionRequested / permissionGranted, live ToolState (progress), transient \"running\".
//     These are delivered via AgentEvent (pub/sub) and only exist in the live UI session.
//
// IDs and correlation:
//   - ToolCall.ID is the cross-turn correlation key; ToolResult.ToolCallID must equal it.
//   - Providers must never leak transport-local item IDs into persistence; use function_call.call_id for OpenAI Responses.
//
// Typical transitions:
//   New → Pending (compose) → Pending (execute; permission?) → Running (live state) → Succeeded | Failed | Canceled.
//
// Notes:
//   - On reload (cold start), only persisted parts are available; pending-without-result will show as pending,
//     but live state and permission prompts won’t reappear until a new run.
//   - Repair: if a tool completed but Crush crashed before persisting the ToolResult, repairOrphanedToolCalls creates
//     a synthetic tool message (Recovered=true) to reconcile the transcript.
//
// UI canonical checks today:
//   - Pending: result.ToolCallID == "" (preferred) rather than !call.Finished.
//   - Completion: result.ToolCallID != "".
//   - Error: result.IsError.
//   - Live status: handled via AgentEventTypeToolState to the matching ToolCallID.
//   - Permission prompt: separate pub/sub notifications bound to ToolCallID.
//
// See also:
//   - internal/llm/agent/agent.go (event handling, tool execution, permissions, cancellation).
//   - internal/llm/provider/* (provider → event mapping, ID rules).
//   - docs/OPENAI-RESPONSES-TOOL-ID-MAPPING.md (ID consistency to avoid \"stuck pending\").
//
// The fields below capture the persisted composition phase; execution results live in ToolResult.
// The combination of one ToolCall (assistant) and one ToolResult (tool) forms a complete tool invocation in history.
type ToolCall struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Input    string `json:"input"`
	Type     string `json:"type"`
	Finished bool   `json:"finished"`
}

func (ToolCall) isPart() {}

type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	Metadata   string `json:"metadata"`
	IsError    bool   `json:"is_error"`
	Recovered  bool   `json:"recovered,omitempty"`
}

func (ToolResult) isPart() {}

type Finish struct {
	Reason  FinishReason `json:"reason"`
	Time    int64        `json:"time"`
	Message string       `json:"message,omitempty"`
	Details string       `json:"details,omitempty"`
}

func (Finish) isPart() {}

type Message struct {
	ID        string
	Role      MessageRole
	SessionID string
	Parts     []ContentPart
	Model     string
	Provider  string
	CreatedAt int64
	UpdatedAt int64
}

func (m *Message) Content() TextContent {
	for _, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			return c
		}
	}
	return TextContent{}
}

func (m *Message) ReasoningContent() ReasoningContent {
	for _, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			return c
		}
	}
	return ReasoningContent{}
}

func (m *Message) ReasoningSummary() ReasoningSummaryContent {
	for _, part := range m.Parts {
		if c, ok := part.(ReasoningSummaryContent); ok {
			return c
		}
	}
	return ReasoningSummaryContent{}
}

func (m *Message) ImageURLContent() []ImageURLContent {
	imageURLContents := make([]ImageURLContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ImageURLContent); ok {
			imageURLContents = append(imageURLContents, c)
		}
	}
	return imageURLContents
}

func (m *Message) BinaryContent() []BinaryContent {
	binaryContents := make([]BinaryContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(BinaryContent); ok {
			binaryContents = append(binaryContents, c)
		}
	}
	return binaryContents
}

func (m *Message) ToolCalls() []ToolCall {
	toolCalls := make([]ToolCall, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			toolCalls = append(toolCalls, c)
		}
	}
	return toolCalls
}

func (m *Message) ToolResults() []ToolResult {
	toolResults := make([]ToolResult, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolResult); ok {
			toolResults = append(toolResults, c)
		}
	}
	return toolResults
}

func (m *Message) IsFinished() bool {
	for _, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			return true
		}
	}
	return false
}

func (m *Message) FinishPart() *Finish {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return &c
		}
	}
	return nil
}

func (m *Message) FinishReason() FinishReason {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return c.Reason
		}
	}
	return ""
}

func (m *Message) IsThinking() bool {
	if m.ReasoningSummary().Summary != "" && m.Content().Text == "" && !m.IsFinished() {
		return true
	}
	return false
}

func (m *Message) AppendContent(delta string) {
	found := false
	for i, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			m.Parts[i] = TextContent{Text: c.Text + delta}
			found = true
		}
	}
	if !found {
		m.Parts = append(m.Parts, TextContent{Text: delta})
	}
}

func (m *Message) AppendReasoningContent(delta string) {
	found := false
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningSummaryContent); ok {
			m.Parts[i] = ReasoningSummaryContent{ID: c.ID, EncryptedContent: c.EncryptedContent, Summary: c.Summary + delta, StartedAt: c.StartedAt, FinishedAt: c.FinishedAt}
			found = true
		}
	}
	if !found {
		m.Parts = append(m.Parts, ReasoningSummaryContent{Summary: delta, StartedAt: time.Now().Unix()})
	}
}

func (m *Message) AppendReasoningSignature(signature string) {
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			m.Parts[i] = ReasoningContent{Thinking: c.Thinking, Signature: c.Signature + signature, StartedAt: c.StartedAt, FinishedAt: c.FinishedAt}
			return
		}
	}
	m.Parts = append(m.Parts, ReasoningContent{Signature: signature})
}

func (m *Message) FinishThinking() {
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningSummaryContent); ok {
			if c.FinishedAt == 0 {
				m.Parts[i] = ReasoningSummaryContent{ID: c.ID, EncryptedContent: c.EncryptedContent, Summary: c.Summary, StartedAt: c.StartedAt, FinishedAt: time.Now().Unix()}
			}
		}
	}
}

func (m *Message) ThinkingDuration() time.Duration {
	reasoning := m.ReasoningContent()
	if reasoning.StartedAt == 0 {
		return 0
	}

	endTime := reasoning.FinishedAt
	if endTime == 0 {
		endTime = time.Now().Unix()
	}

	return time.Duration(endTime-reasoning.StartedAt) * time.Second
}

func (m *Message) FinishToolCall(toolCallID string) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == toolCallID {
				m.Parts[i] = ToolCall{
					ID:       c.ID,
					Name:     c.Name,
					Input:    c.Input,
					Type:     c.Type,
					Finished: true,
				}
				return
			}
		}
	}
}

func (m *Message) AppendToolCallInput(toolCallID string, inputDelta string) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == toolCallID {
				m.Parts[i] = ToolCall{
					ID:       c.ID,
					Name:     c.Name,
					Input:    c.Input + inputDelta,
					Type:     c.Type,
					Finished: c.Finished,
				}
				return
			}
		}
	}
}

func (m *Message) AddToolCall(tc ToolCall) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == tc.ID {
				m.Parts[i] = tc
				return
			}
		}
	}
	m.Parts = append(m.Parts, tc)
}

func (m *Message) SetToolCalls(tc []ToolCall) {
	// remove any existing tool call part it could have multiple
	parts := make([]ContentPart, 0)
	for _, part := range m.Parts {
		if _, ok := part.(ToolCall); ok {
			continue
		}
		parts = append(parts, part)
	}
	m.Parts = parts
	for _, toolCall := range tc {
		m.Parts = append(m.Parts, toolCall)
	}
}

func (m *Message) AddToolResult(tr ToolResult) {
	m.Parts = append(m.Parts, tr)
}

func (m *Message) SetToolResults(tr []ToolResult) {
	for _, toolResult := range tr {
		m.Parts = append(m.Parts, toolResult)
	}
}

func (m *Message) AddFinish(reason FinishReason, message, details string) {
	// remove any existing finish part
	for i, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			m.Parts = slices.Delete(m.Parts, i, i+1)
			break
		}
	}
	m.Parts = append(m.Parts, Finish{Reason: reason, Time: time.Now().Unix(), Message: message, Details: details})
}

func (m *Message) AddImageURL(url, detail string) {
	m.Parts = append(m.Parts, ImageURLContent{URL: url, Detail: detail})
}

func (m *Message) AddBinary(mimeType string, data []byte) {
	m.Parts = append(m.Parts, BinaryContent{MIMEType: mimeType, Data: data})
}
