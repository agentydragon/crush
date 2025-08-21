//go:build !race

package e2e

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpt "github.com/mark3labs/mcp-go/mcptest"
)

type inprocFactory struct{ t *testing.T }

type inprocHookFactoryText struct{ t *testing.T }

func (f inprocFactory) New(name string, m config.MCPConfig) (*client.Client, error) {
	// Build a minimal in-process echo MCP server bound over stdio (pipes), no subprocess
	s := mcpt.NewUnstartedServer(f.t)
	s.AddTool(mcp.NewTool("echo", mcp.WithString("text")), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		m := req.GetArguments()
		var text string
		if v, ok := m["text"].(string); ok {
			text = v
		}
		return mcp.NewToolResultText(text), nil
	})
	if err := s.Start(context.Background()); err != nil {
		return nil, err
	}
	return s.Client(), nil
}

func (f inprocHookFactoryText) New(name string, m config.MCPConfig) (*client.Client, error) {
	// In-process hook server that handles crush_hook.on_sampling
	s := mcpt.NewUnstartedServer(f.t)
	var used bool
	s.AddTool(mcp.NewTool("crush_hook.on_sampling"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if used {
			// Second and subsequent calls: identity (no changes, no resample)
			return mcp.NewToolResultText(""), nil
		}
		used = true
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

func WithInprocMCP(t *testing.T) agent.AgentOption { return agent.WithMCPClientFactory(inprocFactory{t: t}) }
func WithInprocHookMCPText(t *testing.T) agent.AgentOption { return agent.WithMCPClientFactory(inprocHookFactoryText{t: t}) }
