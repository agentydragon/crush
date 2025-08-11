package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func handleEcho(ctx context.Context, req mcp.CallToolRequest) (*mcp.ToolResult, error) {
	args := req.Params.Arguments
	var text string
	if v, ok := args["text"].(string); ok {
		text = v
	}
	return mcp.NewToolResultText(text), nil
}

func main() {
	s := server.NewMCPServer(
		"echo-server",
		"1.0.0",
		server.WithToolCapabilities(true),
	)
	s.AddTool(
		mcp.NewTool(
			"echo",
			mcp.WithDescription("Echo back input text"),
			mcp.WithString("text", mcp.Description("Text to echo"), mcp.Required()),
		),
		handleEcho,
	)
	_ = server.ServeStdio(s)
}
