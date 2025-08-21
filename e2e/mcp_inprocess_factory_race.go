//go:build race

package e2e

import (
	"testing"

	"github.com/charmbracelet/crush/internal/llm/agent"
)

/*
Race-mode skip: In-process MCP stdio transport (mcptest)

Context
- Our e2e suite includes helpers that spin up an in-process MCP server using mcp-go/mcptest
  and connect it to a client over stdio-like pipes.
- Under the Go race detector, the mcptest stdio harness triggers data races inside bufio.Reader
  (ReadSlice/fill/Buffered on the same Reader instance) because both sides run in the same
  process and concurrent goroutines end up reading the same Reader.
- In real usage (separate processes), each side has its own FDs/Readers and the race does not occur.

Symptoms (from -race)
- Concurrent read/write to bufio.Reader buffer from goroutines spawned by:
  - client/transport.(*Stdio).readResponses (client side)
  - mcptest.Server.Start (server side)

Decision
- To keep race builds meaningful without flaking on an upstream test harness issue, we skip
  the in-process MCP tests under -race only. Non-race builds still run these tests fully.

How to run full e2e including in-process MCP
- Omit -race (go test ./e2e) or run the real stdio/HTTP/SSE MCP variants.

Tracking
- TODO(mpokorny): Remove these skips once mcp-go’s mcptest stdio harness is made race-safe
  (single-reader fanout or guarded Reader). If an upstream issue number exists, reference it here.
*/
func WithInprocMCP(t *testing.T) agent.AgentOption {
	t.Helper()
	t.Skip("-race: skip in-process MCP (mcptest stdio) due to upstream bufio.Reader race; see file header")
	return agent.WithMCPClientFactory(nil)
}

func WithInprocHookMCPText(t *testing.T) agent.AgentOption {
	t.Helper()
	t.Skip("-race: skip in-process MCP (mcptest stdio) due to upstream bufio.Reader race; see file header")
	return agent.WithMCPClientFactory(nil)
}
