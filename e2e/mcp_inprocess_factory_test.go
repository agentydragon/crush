package e2e

import (
	"context"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/mark3labs/mcp-go/client"
	mcpt "github.com/mark3labs/mcp-go/mcptest"
	"github.com/mark3labs/mcp-go/mcp"
)

type inprocFactory struct{ t *testing.T }

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

func WithInprocMCP(t *testing.T) agent.AgentOption {
	return agent.WithMCPClientFactory(inprocFactory{t: t})
}
