package agent

import (
	"context"
	"fmt"
	"log/slog"
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
	// Launch each MCP client concurrently so slow ones don’t block the rest.
	for name, mc := range cfg.MCP {
		name, mc := name, mc // capture
		go func() {
			if mc.Disabled {
				updateMCPState(name, MCPStateDisabled, nil, nil, 0)
				return
			}
			// Mark as starting so UI shows spinner immediately.
			updateMCPState(name, MCPStateStarting, nil, nil, 0)

			cl, err := (defaultMCPFactory{wire: m.wire}).New(name, mc)
			if err != nil {
				updateMCPState(name, MCPStateError, err, nil, 0)
				slog.Error("mcp.create", "name", name, "err", err)
				return
			}
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := cl.Start(cctx); err != nil {
				_ = cl.Close()
				updateMCPState(name, MCPStateError, err, nil, 0)
				slog.Error("mcp.start", "name", name, "err", err)
				return
			}
			if _, err := cl.Initialize(cctx, mcpInitRequest); err != nil {
				_ = cl.Close()
				updateMCPState(name, MCPStateError, err, nil, 0)
				slog.Error("mcp.init", "name", name, "err", err)
				return
			}
			wire := m.wire
			if wire == nil {
				wire = perMCPLogger(name)
			}
			conn := newDefaultMCPConnection(name, cl, wire)
			m.conns.Set(name, conn)
			mts, err := conn.ListTools(ctx)
			if err != nil {
				_ = cl.Close()
				updateMCPState(name, MCPStateError, err, nil, 0)
				slog.Error("mcp.list_tools", "name", name, "err", err)
				return
			}
			// Update state to connected or error based on tool count
			if len(mts) == 0 {
				updateMCPState(name, MCPStateError, fmt.Errorf("no tools advertised"), cl, 0)
			} else {
				updateMCPState(name, MCPStateConnected, nil, cl, len(mts))
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
		}()
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
