package e2e

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpt "github.com/mark3labs/mcp-go/mcptest"
	"github.com/stretchr/testify/require"
)

type hookFactory struct{ t *testing.T }

func (f hookFactory) New(name string, m config.MCPConfig) (*client.Client, error) {
	// In-process MCP server that exposes crush_hook.on_sampling and returns a plan
	s := mcpt.NewUnstartedServer(f.t)
	s.AddTool(mcp.NewTool("crush_hook.on_sampling"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Build a plan that replaces the sampled message with:
		// 1) system reminder, 2) assistant message that invokes bash tool_call, then next=assistant_sampling
		plan := map[string]any{
			"plan": map[string]any{
				"replace_with": []any{
					map[string]any{
						"role": "system",
						"parts": []any{map[string]any{"type": "text", "data": map[string]any{"text": "Policy: check safely first."}}},
					},
					map[string]any{
						"role": "assistant",
						"parts": []any{
							map[string]any{"type": "text", "data": map[string]any{"text": "Acknowledged. Running a safe check."}},
							map[string]any{"type": "tool_call", "data": map[string]any{"id": "tc_check", "name": "bash", "input": "{\"command\":\"echo safe\"}", "type": "function", "finished": true}},
						},
					},
				},
				"next": "assistant_sampling",
				"ui_info": "hook applied",
			},
		}
		b, _ := json.Marshal(plan)
		return mcp.NewToolResultText(string(b)), nil
	})
	if err := s.Start(context.Background()); err != nil {
		return nil, err
	}
	return s.Client(), nil
}

func WithHookMCP(t *testing.T) agent.AgentOption { return agent.WithMCPClientFactory(hookFactory{t: t}) }

// A hook that requests resample without tools: replace_with text only + next=assistant_sampling.
type hookFactoryText struct{ t *testing.T }

func (f hookFactoryText) New(name string, m config.MCPConfig) (*client.Client, error) {
	// In-process MCP server that exposes crush_hook.on_sampling and returns a text-only plan
	s := mcpt.NewUnstartedServer(f.t)
	s.AddTool(mcp.NewTool("crush_hook.on_sampling"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		plan := map[string]any{
			"plan": map[string]any{
				"replace_with": []any{
					map[string]any{"role": "assistant", "parts": []any{map[string]any{"type": "text", "data": map[string]any{"text": "Hook says: proceed."}}}},
				},
				"next":   "assistant_sampling",
				"ui_info": "hook applied",
			},
		}
		b, _ := json.Marshal(plan)
		return mcp.NewToolResultText(string(b)), nil
	})
	if err := s.Start(context.Background()); err != nil {
		return nil, err
	}
	return s.Client(), nil
}

func WithHookMCPText(t *testing.T) agent.AgentOption { return agent.WithMCPClientFactory(hookFactoryText{t: t}) }

func TestSequenceTransformer_PostSampleReplace_RunsToolAndResamples(t *testing.T) {
	// Setup mock Responses server
	mock := &mockResponsesServer{}
	ts := httptest.NewServer(mock)
	defer ts.Close()
	// Setup with bash allowed and inproc hook MCP
	agentSvc, sessionsSvc, messagesSvc, perms, _, cleanup := SetupServices(t, ts.URL+"/v1", []string{"bash"}, "", WithHookMCP(t))
	defer cleanup()
	// Auto-approve permissions so bash runs without prompt
	ctx := context.Background()
	s, err := sessionsSvc.Create(ctx, "hook-seq-transform")
	require.NoError(t, err)
	perms.AutoApproveSession(s.ID)

	// Enable a single MCP with handles_hook=true and auto-approve permissions (so bash runs without prompt)
	cfg := config.Get()
	cfg.MCP["inproc"] = config.MCPConfig{Type: config.MCPStdio, HandlesHook: true}

	// Orchestrate provider SSE: initial sample, then final after tool
	mock.Enqueue(Step{Do: []Action{actionEmit(sseResponseCreated())}})
	// First stream: emit a function_call for bash with id tc_check; provider will finish with tool_use
	mock.Enqueue(Step{Do: []Action{actionEmit(
		SSE{Data: map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "function_call", "id": "tc_check", "name": "bash"}}},
		SSE{Data: map[string]any{"type": "response.function_call_arguments.delta", "item_id": "tc_check", "delta": "{\"command\":\"echo safe\"}"}},
		SSE{Data: map[string]any{"type": "response.function_call_arguments.done", "item_id": "tc_check"}},
		SSE{Data: map[string]any{"type": "response.completed", "response": map[string]any{
			"status": "incomplete",
			"incomplete_details": map[string]any{"reason": "tool_use"},
			"output": []any{map[string]any{"type": "function_call", "id": "tc_check", "name": "bash", "arguments": "{\"command\":\"echo safe\"}"}},
		}}},
	)}})
	mock.Enqueue(Step{Do: []Action{actionClose()}})
	// Second stream: after function_call_output is posted by the client, emit a final assistant text
	mock.Enqueue(Step{WaitUntil: []Condition{{Kind: CondRequestBodyContains, Name: "function_call_output"}}, Do: []Action{actionEmit(sseTextDelta("ok", "out2"), sseTextDone(), sseCompletedText("ok", "out2")), actionClose()}})

	// Create a session and run one prompt
	evCh, err := agentSvc.Run(ctx, s.ID, "do something simple")
	require.NoError(t, err)

	// Collect events until we observe: tool message created with result for tc_check and a second assistant sampling done
	deadline := time.After(10 * time.Second)
	var sawTool, sawSecondAssistant bool
	var toolContent string
	for !(sawTool && sawSecondAssistant) {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for tool+second assistant; sawTool=%v sawSecond=%v", sawTool, sawSecondAssistant)
		case <-evCh:
			// ignore; use DB state for robust detection
		default:
			// fall through
		}
		// Inspect messages for tool result and second assistant
		msgs, _ := messagesSvc.List(ctx, s.ID)
		var lastAssistantFinished bool
		for _, m := range msgs {
			if m.Role == message.Tool {
				for _, tr := range m.ToolResults() {
					if tr.ToolCallID == "tc_check" {
						sawTool = true
						toolContent = tr.Content
					}
				}
			}
			if m.Role == message.Assistant {
				lastAssistantFinished = m.IsFinished()
			}
		}
		if sawTool && lastAssistantFinished {
			sawSecondAssistant = true
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.True(t, sawTool, "expected synthetic tool to run")
	require.Contains(t, toolContent, "safe")

	// Verify that the assistant message used for tool execution was the replaced one (has tool_call id tc_check)
	msgs, err := messagesSvc.List(ctx, s.ID)
	require.NoError(t, err)
	var foundAssistantWithCall bool
	for _, m := range msgs {
		if m.Role == message.Assistant {
			for _, tc := range m.ToolCalls() {
				if tc.ID == "tc_check" {
					foundAssistantWithCall = true
				}
			}
		}
	}
	require.True(t, foundAssistantWithCall, "assistant replacement with tool_call not found in history")
}
