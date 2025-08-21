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

type mcpBundle struct {
	name               string
	wire               MCPWireLogger
	progress           map[string]func(string, float64, float64)
	notifierRegistered bool
}


func (b *mcpBundle) logEvent(extra map[string]any) {
	if b.wire != nil && b.wire.Enabled() {
		b.wire.Event(b.name, extra)
	}
}

func (b *mcpBundle) registerProgressListener(token string, cb func(string, float64, float64)) {
	if b.progress == nil {
		b.progress = map[string]func(string, float64, float64){}
	}
	b.progress[token] = cb
}

func (b *mcpBundle) unregisterProgressListener(token string) {
	if b.progress != nil {
		delete(b.progress, token)
	}
}

func (b *mcpBundle) dispatchProgress(token, msg string, prog, total float64) {
	if b.progress != nil {
		if cb := b.progress[token]; cb != nil {
			cb(msg, prog, total)
		}
	}
}

func (b *mcpBundle) logInit(transport string) {
	b.logEvent(map[string]any{"event": "init", "transport": transport})
}

func (b *mcpBundle) logListTools(dur time.Duration, count int) {
	b.logEvent(map[string]any{"event": "list_tools", "duration_ms": dur.Milliseconds(), "tool_count": count})
}

func (b *mcpBundle) logCallStart(tool, callID string, effTimeout time.Duration, deadlineMS int64) {
	b.logEvent(map[string]any{"event": "call_start", "tool": tool, "tool_call_id": callID, "effective_timeout_ms": int64(effTimeout / time.Millisecond), "deadline_unix_ms": deadlineMS})
}

func (b *mcpBundle) logState(prev, next MCPState, reason string, connectedAt time.Time, toolCount int, err error) {
	extra := map[string]any{"event": "state", "prev": prev.String(), "next": next.String(), "reason": reason, "tool_count": toolCount, "connected_at": connectedAt.UnixMilli()}
	if err != nil {
		extra["error"] = err.Error()
	}
	b.logEvent(extra)
}
func (b *mcpBundle) logClosed() { b.logEvent(map[string]any{"event": "closed"}) }

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
