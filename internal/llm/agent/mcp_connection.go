package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

type defaultMCPConnection struct {
	name string
	cl   *client.Client
	wire MCPWireLogger
}

func newDefaultMCPConnection(name string, cl *client.Client, wire MCPWireLogger) *defaultMCPConnection {
	return &defaultMCPConnection{name: name, cl: cl, wire: wire}
}

func (c *defaultMCPConnection) Name() string           { return c.name }
func (c *defaultMCPConnection) Client() *client.Client { return c.cl }

func (c *defaultMCPConnection) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	res, err := c.cl.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, err
	}
	return res.Tools, nil
}

func (c *defaultMCPConnection) Call(ctx context.Context, callID, toolName, input string) (tools.ToolResponse, error) {
	args := map[string]any{}
	if err := jsonUnmarshal([]byte(input), &args); err != nil {
		return tools.NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}
	if c.wire != nil && c.wire.Enabled() {
		c.wire.Out(c.name, toolName, callID, input)
	}
	start := time.Now()
	result, err := c.cl.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: toolName, Arguments: args}})
	dur := time.Since(start)
	if err != nil {
		if c.wire != nil && c.wire.Enabled() {
			c.wire.Err(c.name, toolName, callID, dur, err)
		}
		return tools.NewTextErrorResponse(err.Error()), nil
	}
	var output strings.Builder
	for _, v := range result.Content {
		if v, ok := v.(mcp.TextContent); ok {
			output.WriteString(v.Text)
		} else {
			fmt.Fprintf(&output, "%v: ", v)
		}
	}
	resp := tools.NewTextResponse(output.String())
	if result.IsError {
		resp.IsError = true
	}
	if c.wire != nil && c.wire.Enabled() {
		c.wire.In(c.name, toolName, callID, resp, dur)
	}
	return resp, nil
}

func (c *defaultMCPConnection) Close() { _ = c.cl.Close() }

// small helpers to avoid importing encoding/json here twice across files
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
