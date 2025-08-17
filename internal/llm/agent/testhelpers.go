package agent

import (
	"sync"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// TODO(mpokorny): Remove global MCP state and sync.Once; use injectable registries.
// ResetMCPForTests resets MCP globals so tests can reinitialize clients/tools deterministically.
func ResetMCPForTests() {
	mcpTools = nil
	mcpToolsOnce = sync.Once{}
	defaultMCPMgr = &defaultMCPManager{conns: map[string]mcpConnection{}, states: csync.NewMap[string, MCPClientInfo](), broker: pubsub.NewBroker[MCPEvent](), bundles: map[string]*mcpBundle{}}
}
