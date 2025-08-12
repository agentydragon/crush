package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

type MCPManager interface {
	StartAll(ctx context.Context, permissions permission.Service, cfg *config.Config) error
	ListTools() []tools.BaseTool
	GetClient(name string) (*client.Client, bool)
	CloseAll()
	Subscribe(ctx context.Context) <-chan pubsub.Event[MCPEvent]
	State(name string) (MCPClientInfo, bool)
}

type mcpConnection interface {
	Name() string
	Client() *client.Client
	Call(ctx context.Context, callID, toolName, input string) (tools.ToolResponse, error)
	ListTools(ctx context.Context) ([]mcp.Tool, error)
	Close()
}

type defaultMCPManager struct {
	factory MCPClientFactory
	wire    MCPWireLogger
	conns   map[string]mcpConnection
	tools   []tools.BaseTool
	states  *csync.Map[string, MCPClientInfo]
	broker  *pubsub.Broker[MCPEvent]
}

func NewDefaultMCPManager(factory MCPClientFactory, wire MCPWireLogger) MCPManager {
	return &defaultMCPManager{factory: factory, wire: wire, conns: map[string]mcpConnection{}, states: csync.NewMap[string, MCPClientInfo](), broker: pubsub.NewBroker[MCPEvent]()}
}

func (m *defaultMCPManager) StartAll(ctx context.Context, permissions permission.Service, cfg *config.Config) error {
	for name, mc := range cfg.MCP {
		if mc.Disabled {
			continue
		}
		cl, err := (defaultMCPFactory{wire: m.wire}).New(name, mc)
		if err != nil {
			return fmt.Errorf("create mcp client %s: %w", name, err)
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := cl.Start(cctx); err != nil {
			_ = cl.Close()
			return fmt.Errorf("start mcp client %s: %w", name, err)
		}
		if _, err := cl.Initialize(cctx, mcpInitRequest); err != nil {
			_ = cl.Close()
			return fmt.Errorf("initialize mcp client %s: %w", name, err)
		}
		conn := newDefaultMCPConnection(name, cl, m.wire)
		m.conns[name] = conn
		mts, err := conn.ListTools(ctx)
		if err != nil {
			_ = cl.Close()
			return fmt.Errorf("list tools for %s: %w", name, err)
		}
		for _, t := range mts {
			m.tools = append(m.tools, &McpTool{
				mcpName:     name,
				tool:        t,
				permissions: permissions,
				workingDir:  cfg.WorkingDir(),
				wire:        m.wire,
			})
		}
	}
	return nil
}

func (m *defaultMCPManager) ListTools() []tools.BaseTool { return m.tools }

func (m *defaultMCPManager) GetClient(name string) (*client.Client, bool) {
	if c, ok := m.conns[name]; ok {
		return c.Client(), true
	}
	return nil, false
}

func (m *defaultMCPManager) CloseAll() {
	for _, c := range m.conns {
		c.Close()
	}
	m.broker.Shutdown()
}

func (m *defaultMCPManager) Subscribe(ctx context.Context) <-chan pubsub.Event[MCPEvent] { return m.broker.Subscribe(ctx) }
func (m *defaultMCPManager) State(name string) (MCPClientInfo, bool) { return m.states.Get(name) }
