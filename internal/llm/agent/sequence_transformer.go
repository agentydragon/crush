package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/transform"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	overallDeadline = 1500 * time.Millisecond
	sleepInterval  = 50 * time.Millisecond
	perCallTimeout = 2500 * time.Millisecond
)

const hookToolName = "crush_hook.on_sampling"

// runPostSampleHook calls the MCP hook (if available) after sampling an assistant message.
// It returns the parsed plan or nil if identity/no hook.
func (a *agent) runPostSampleHook(ctx context.Context, sessionID string, sampled message.Message) (*transform.Response, error) {
	// Find a connected MCP client; attempt the tool on each until one handles it.
	states := GetMCPStates()
	// Enforce exactly one MCP with HandlesHook=true
	cfg := config.Get()
	var target string
	for name, m := range cfg.MCP {
		if m.HandlesHook && !m.Disabled {
			if target != "" {
				return nil, nil // multiple flagged → disable hook
			}
			target = name
		}
	}
	if target == "" {
		return nil, nil
	}
	// Build request payload (full history + sampled)
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	req := transform.Request{SessionID: sessionID, Phase: transform.PhasePostSample}
	// History
	req.Committed.History.Messages = make([]transform.MessageDTO, 0, len(msgs))
	for _, m := range msgs {
		req.Committed.History.Messages = append(req.Committed.History.Messages, toDTO(m))
	}
	// Sampled
	req.Sampled.Message = toDTO(sampled)

	// Marshal to map[string]any for MCP params
	var args map[string]any
	if b, err := json.Marshal(req); err == nil {
		_ = json.Unmarshal(b, &args)
	} else {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Best-effort wait for target MCP to reach connected (handles init race in fast post-sample path)
	deadline := time.Now().Add(overallDeadline)
	for {
		info, ok := states[target]
		if ok && info.State == MCPStateConnected && info.Client != nil {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(sleepInterval)
		states = GetMCPStates()
	}

	var lastErr error
	for name, info := range states {
		if name != target || info.State != MCPStateConnected || info.Client == nil {
			continue
		}
		slog.Info("debug.hook.actual_call", "target", target, "sampled_msg_id", sampled.ID, "args_keys", len(args))
		callCtx, cancel := context.WithTimeout(ctx, perCallTimeout)
		defer cancel()
		start := time.Now()
		if a.mcpWireLogger != nil && a.mcpWireLogger.Enabled() {
			a.mcpWireLogger.Out(name, hookToolName, sampled.ID, string(mustJSON(args)))
		}
		result, err := info.Client.CallTool(callCtx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: hookToolName, Arguments: args}})
		dur := time.Since(start)
		if err != nil {
			lastErr = err
			if a.mcpWireLogger != nil && a.mcpWireLogger.Enabled() {
				a.mcpWireLogger.Err(name, hookToolName, sampled.ID, dur, err)
			}
			slog.Debug("hook call failed on mcp", "mcp", name, "error", err)
			continue
		}
		// Extract text content concat
		var buf string
		for _, c := range result.Content {
			if t, ok := c.(mcp.TextContent); ok {
				buf += t.Text
			}
		}
		if buf == "" {
			// Treat empty as identity
			return nil, nil
		}
		var resp transform.Response
		if err := json.Unmarshal([]byte(buf), &resp); err != nil {
			lastErr = fmt.Errorf("invalid hook response json: %w", err)
			continue
		}
		if a.mcpWireLogger != nil && a.mcpWireLogger.Enabled() {
			a.mcpWireLogger.In(name, hookToolName, sampled.ID, resp, dur)
		}
		return &resp, nil
	}
	return nil, lastErr
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// toDTO converts a stored message into the transformer wire DTO.
func toDTO(m message.Message) transform.MessageDTO {
	dto := transform.MessageDTO{
		ID:        m.ID,
		Role:      string(m.Role),
		Model:     m.Model,
		Provider:  m.Provider,
		CreatedAt: m.CreatedAt,
		Parts:     make([]transform.PartWrapper, 0, len(m.Parts)),
	}
	for _, part := range m.Parts {
		switch p := part.(type) {
		case message.ReasoningSummaryContent:
			dto.Parts = append(dto.Parts, transform.PartWrapper{Type: "reasoning", Data: p})
		case message.ReasoningEncryptedContent:
			dto.Parts = append(dto.Parts, transform.PartWrapper{Type: "reasoning_encrypted", Data: p})
		case message.TextContent:
			dto.Parts = append(dto.Parts, transform.PartWrapper{Type: "text", Data: p})
		case message.ImageURLContent:
			dto.Parts = append(dto.Parts, transform.PartWrapper{Type: "image_url", Data: p})
		case message.BinaryContent:
			dto.Parts = append(dto.Parts, transform.PartWrapper{Type: "binary", Data: p})
		case message.ToolCall:
			dto.Parts = append(dto.Parts, transform.PartWrapper{Type: "tool_call", Data: p})
		case message.ToolResult:
			dto.Parts = append(dto.Parts, transform.PartWrapper{Type: "tool_result", Data: p})
		case message.Finish:
			dto.Parts = append(dto.Parts, transform.PartWrapper{Type: "finish", Data: p})
		default:
			slog.Warn("unknown part type in toDTO", "type", fmt.Sprintf("%T", p))
		}
	}
	return dto
}

// partsFromDTO converts allowed DTO parts into persisted ContentParts (text, tool_call only).
func partsFromDTO(dto transform.MessageDTO) []message.ContentPart {
	parts := make([]message.ContentPart, 0, len(dto.Parts))
	for _, w := range dto.Parts {
		switch w.Type {
		case "text":
			b, _ := json.Marshal(w.Data)
			var tc message.TextContent
			if err := json.Unmarshal(b, &tc); err == nil && tc.Text != "" {
				parts = append(parts, tc)
			} else if err != nil {
				slog.Warn("hook: invalid text part", "error", err)
			}
		case "tool_call":
			b, _ := json.Marshal(w.Data)
			var call message.ToolCall
			if err := json.Unmarshal(b, &call); err == nil && call.Name != "" {
				// Ensure finished flag is true for persisted assistant message tool composition
				if !call.Finished {
					call.Finished = true
				}
				parts = append(parts, call)
			} else if err != nil {
				slog.Warn("hook: invalid tool_call part", "error", err)
			}
		default:
			// Ignore unsupported types in v1
			slog.Debug("hook: ignoring unsupported part type", "type", w.Type)
		}
	}
	return parts
}

func hasToolCall(parts []message.ContentPart) bool {
	for _, p := range parts {
		if _, ok := p.(message.ToolCall); ok {
			return true
		}
	}
	return false
}
