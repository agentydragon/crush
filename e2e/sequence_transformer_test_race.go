//go:build race

package e2e

import (
	"testing"

	"github.com/charmbracelet/crush/internal/llm/agent"
)

/*
Race-mode skip: In-process MCP hook tests

Rationale matches e2e/mcp_inprocess_factory_race.go — the mcptest stdio harness triggers
bufio.Reader data races under -race because both sides read the same Reader instance concurrently.
We skip only under -race and keep normal test runs intact.
*/
func WithHookMCP(t *testing.T) agent.AgentOption {
	t.Helper()
	t.Skip("-race: skip in-process MCP hook (mcptest stdio) due to upstream bufio.Reader race")
	return agent.WithMCPClientFactory(nil)
}
func WithHookMCPText(t *testing.T) agent.AgentOption {
	t.Helper()
	t.Skip("-race: skip in-process MCP hook (mcptest stdio) due to upstream bufio.Reader race")
	return agent.WithMCPClientFactory(nil)
}
