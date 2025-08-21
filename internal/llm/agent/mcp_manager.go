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
	States() map[string]MCPClientInfo
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
	conns   *csync.Map[string, mcpConnection]
	tools   []tools.BaseTool
	states  *csync.Map[string, MCPClientInfo]
	broker  *pubsub.Broker[MCPEvent]
	bundles *csync.Map[string, *mcpBundle]
}

var defaultMCPMgr *defaultMCPManager

func DefaultMCPManager() MCPManager { return getDefaultMCPManager() }

func getDefaultMCPManager() *defaultMCPManager {
	if defaultMCPMgr == nil {
		defaultMCPMgr = &defaultMCPManager{factory: nil, wire: nil, conns: csync.NewMap[string, mcpConnection](), states: csync.NewMap[string, MCPClientInfo](), broker: pubsub.NewBroker[MCPEvent](), bundles: csync.NewMap[string, *mcpBundle]()}
	}
	return defaultMCPMgr
}

func NewDefaultMCPManager(factory MCPClientFactory, wire MCPWireLogger) MCPManager {
	return &defaultMCPManager{factory: factory, wire: wire, conns: csync.NewMap[string, mcpConnection](), states: csync.NewMap[string, MCPClientInfo](), broker: pubsub.NewBroker[MCPEvent](), bundles: csync.NewMap[string, *mcpBundle]()}
}

// mcpBundle is defined in mcp_connection.go and shared here

func (m *defaultMCPManager) bundle(name string) *mcpBundle {
	return m.bundles.GetOrSet(name, func() *mcpBundle {
		w := m.wire
		if w == nil {
			w = perMCPLogger(name)
		}
		return &mcpBundle{name: name, wire: w, progress: map[string]func(string, float64, float64){} }
	})
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
		if err := cl.Start(cctx); err != nil {
			cancel()
			_ = cl.Close()
			return fmt.Errorf("start mcp client %s: %w", name, err)
		}
		if _, err := cl.Initialize(cctx, mcpInitRequest); err != nil {
			cancel()
			_ = cl.Close()
			return fmt.Errorf("initialize mcp client %s: %w", name, err)
		}
		// done with cctx
		cancel()
		wire := m.wire
		if wire == nil {
			wire = perMCPLogger(name)
		}
		conn := newDefaultMCPConnection(name, cl, wire)
		m.conns.Set(name, conn)
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
	if c, ok := m.conns.Get(name); ok {
		return c.Client(), true
	}
	return nil, false
}

func (m *defaultMCPManager) CloseAll() {
	for c := range m.conns.Seq() { c.Close() }
	m.broker.Shutdown()
}

func (m *defaultMCPManager) Subscribe(ctx context.Context) <-chan pubsub.Event[MCPEvent] {
	return m.broker.Subscribe(ctx)
}
func (m *defaultMCPManager) State(name string) (MCPClientInfo, bool) { return m.states.Get(name) }
func (m *defaultMCPManager) States() map[string]MCPClientInfo {
	out := make(map[string]MCPClientInfo)
	for k, v := range m.states.Seq2() {
		out[k] = v
	}
	return out
}
