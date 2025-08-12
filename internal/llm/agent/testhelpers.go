package agent

import (
	"sync"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/mark3labs/mcp-go/client"
)

// TODO(mpokorny): Remove global MCP state and sync.Once; use injectable registries.
// ResetMCPForTests resets MCP globals so tests can reinitialize clients/tools deterministically.
func ResetMCPForTests() {
	mcpTools = nil
	mcpToolsOnce = sync.Once{}
	mcpClients = csync.NewMap[string, *client.Client]()
	mcpStates = csync.NewMap[string, MCPClientInfo]()
}

// ResetMCPWireLoggersForTests clears cached MCP wire loggers so each test can direct logs independently.
func ResetMCPWireLoggersForTests() {
	mcpWireLoggers = sync.Map{}
	mcpStdioLoggers = sync.Map{}
}
