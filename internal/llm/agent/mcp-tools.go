package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

type AgentOption func(*agent)

type MCPClientFactory interface {
	New(name string, m config.MCPConfig) (*client.Client, error)
}


type MCPWireLogger interface {
	Enabled() bool
	LogStdio(mcp, stream, line string)
	Out(mcp, tool, callID, input string)
	In(mcp, tool, callID string, payload any, dur time.Duration)
	Err(mcp, tool, callID string, dur time.Duration, err error)
	Event(mcp string, extra map[string]any)
}

func WithMCPClientFactory(f MCPClientFactory) AgentOption {
	return func(a *agent) { a.mcpClientFactory = f }
}
func WithMCPWireLogger(w MCPWireLogger) AgentOption { return func(a *agent) { a.mcpWireLogger = w } }

// MCPState represents the current state of an MCP client
type MCPState int

const (
	MCPStateDisabled MCPState = iota
	MCPStateStarting
	MCPStateConnected
	MCPStateError
)

func (s MCPState) String() string {
	switch s {
	case MCPStateDisabled:
		return "disabled"
	case MCPStateStarting:
		return "starting"
	case MCPStateConnected:
		return "connected"
	case MCPStateError:
		return "error"
	default:
		return "unknown"
	}
}

// MCPEventType represents the type of MCP event
type MCPEventType string

const (
	MCPEventStateChanged MCPEventType = "state_changed"
)

// MCPEvent represents an event in the MCP system
type MCPEvent struct {
	Type      MCPEventType
	Name      string
	State     MCPState
	Error     error
	ToolCount int
}

// MCPClientInfo holds information about an MCP client's state
type MCPClientInfo struct {
	Name        string
	State       MCPState
	Error       error
	Client      *client.Client
	ToolCount   int
	ConnectedAt time.Time
}

var (
	mcpToolsOnce sync.Once
	mcpTools     []tools.BaseTool
)

type McpTool struct {
	mcpName     string
	tool        mcp.Tool
	permissions permission.Service
	workingDir  string
	wire        MCPWireLogger
}

var defaultMCPToolTimeout = 2 * time.Minute

func mcpToolTimeout() time.Duration {
	cfg := config.Get()
	if cfg.Options != nil && cfg.Options.MCP != nil && cfg.Options.MCP.ToolTimeoutSecs > 0 {
		return time.Duration(cfg.Options.MCP.ToolTimeoutSecs) * time.Second
	}
	return defaultMCPToolTimeout
}

func (b *McpTool) Name() string {
	return fmt.Sprintf("mcp_%s_%s", b.mcpName, b.tool.Name)
}

func (b *McpTool) Info() tools.ToolInfo {
	required := b.tool.InputSchema.Required
	if required == nil {
		required = make([]string, 0)
	}
	return tools.ToolInfo{
		Name:        fmt.Sprintf("mcp_%s_%s", b.mcpName, b.tool.Name),
		Description: b.tool.Description,
		Parameters:  b.tool.InputSchema.Properties,
		Required:    required,
	}
}

func runTool(ctx context.Context, name, toolName string, input string, meta *mcp.Meta) (tools.ToolResponse, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return tools.NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}
	call := func(c *client.Client) (tools.ToolResponse, error) {
		result, err := c.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name:      toolName,
				Arguments: args,
				Meta:      meta,
			},
		})
		if err != nil {
			return tools.NewTextErrorResponse(err.Error()), nil
		}
		var output strings.Builder
		for _, v := range result.Content {
			if v, ok := v.(mcp.TextContent); ok {
				output.WriteString(v.Text)
			} else {
				_, _ = fmt.Fprintf(&output, "%v: ", v)
			}
		}
		return tools.NewTextResponse(output.String()), nil
	}
	c, ok := getDefaultMCPManager().GetClient(name)
	if !ok {
		return tools.NewTextErrorResponse("mcp '" + name + "' not available"), nil
	}
	return call(c)
}

func (b *McpTool) Run(ctx context.Context, params tools.ToolCall) (tools.ToolResponse, error) {
	sessionID, messageID := tools.GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return tools.ToolResponse{}, fmt.Errorf("session ID and message ID are required for creating a new file")
	}
	permissionDescription := fmt.Sprintf("execute %s with the following parameters: %s", b.Info().Name, params.Input)
	p := b.permissions.Request(
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			ToolCallID:  params.ID,
			Path:        b.workingDir,
			ToolName:    b.Info().Name,
			Action:      "execute",
			Description: permissionDescription,
			Params:      params.Input,
		},
	)
	if !p {
		return tools.ToolResponse{}, permission.ErrorPermissionDenied
	}

	callCtx := ctx
	if _, ok := callCtx.Deadline(); !ok {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, mcpToolTimeout())
		defer cancel()
	}
	start := time.Now()
	slog.Info("MCP tool call start", "mcp", b.mcpName, "tool", b.tool.Name, "tool_call_id", params.ID)
	if b.wire != nil && b.wire.Enabled() {
		b.wire.Out(b.mcpName, b.tool.Name, params.ID, params.Input)
	}
	if mcpWireEnabled() {
		deadlineMS := int64(0)
		if d, ok := callCtx.Deadline(); ok { deadlineMS = d.UnixMilli() }
		getDefaultMCPManager().bundle(b.mcpName).logCallStart(b.tool.Name, params.ID, mcpToolTimeout(), deadlineMS)
	}
	sink := tools.SinkFromContext(ctx)
	startMS := tools.NowMillis()
	deadline, hasDeadline := callCtx.Deadline()
	updateWaiting := func(detail string) {
		st := tools.ToolState{Phase: tools.PhaseWaiting, Title: "Waiting for MCP server response…", Detail: detail, StartedAt: startMS}
		if hasDeadline { st.Meta = map[string]any{"deadline_unix_ms": deadline.UnixMilli()} }
		sink.Update(st)
	}
	updateWaiting(fmt.Sprintf("server=%s tool=%s", b.mcpName, b.tool.Name))

	done := make(chan struct{})
	progressCh := make(chan string, 4)
	// Generate a progress token and register listener
	progressToken := fmt.Sprintf("crush-%d", time.Now().UnixNano())
	getDefaultMCPManager().bundle(b.mcpName).registerProgressListener(progressToken, func(msg string, prog, total float64) {
		detail := msg
		if total > 0 {
			pct := 0.0
			if total != 0 {
				pct = (prog / total) * 100
			}
			detail = fmt.Sprintf("%s (%.0f%%)", strings.TrimSpace(msg), math.Round(pct))
		} else if prog > 0 {
			detail = fmt.Sprintf("%s (%.0f)", strings.TrimSpace(msg), math.Round(prog))
		}
		if mcpWireEnabled() {
			getDefaultMCPManager().bundle(b.mcpName).logEvent(map[string]any{"event":"progress","token":progressToken,"message":strings.TrimSpace(msg),"progress":prog,"total":total})
		}
		select { case progressCh <- detail: default: }
	})
	defer getDefaultMCPManager().bundle(b.mcpName).unregisterProgressListener(progressToken)
	go func() {
		for {
			select {
			case d := <-progressCh:
				updateWaiting(d)
			case <-done:
				return
			}
		}
	}()
	resp, err := runTool(callCtx, b.mcpName, b.tool.Name, params.Input, &mcp.Meta{ProgressToken: progressToken})
	close(done)
	dur := time.Since(start)
	if err != nil {
		slog.Error("MCP tool call error", "mcp", b.mcpName, "tool", b.tool.Name, "tool_call_id", params.ID, "duration_ms", dur.Milliseconds(), "error", err)
		if b.wire != nil && b.wire.Enabled() {
			b.wire.Err(b.mcpName, b.tool.Name, params.ID, dur, err)
		}
		sink.Update(tools.ToolState{Phase: tools.PhaseError, Title: "MCP tool error", Detail: fmt.Sprintf("after=%s", dur.Round(time.Second))})
		return resp, err
	}
	if resp.IsError {
		slog.Error("MCP tool call returned error", "mcp", b.mcpName, "tool", b.tool.Name, "tool_call_id", params.ID, "duration_ms", dur.Milliseconds())
	} else {
		slog.Info("MCP tool call done", "mcp", b.mcpName, "tool", b.tool.Name, "tool_call_id", params.ID, "duration_ms", dur.Milliseconds())
	}
	if b.wire != nil && b.wire.Enabled() {
		b.wire.In(b.mcpName, b.tool.Name, params.ID, resp, dur)
	}
	sink.Update(tools.ToolState{Phase: tools.PhaseDone, Title: "MCP tool complete", Detail: fmt.Sprintf("after=%s", dur.Round(time.Second))})
	return resp, nil
}

func getTools(ctx context.Context, name string, permissions permission.Service, c *client.Client, workingDir string, wire MCPWireLogger) []tools.BaseTool {
	start := time.Now()
	result, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		slog.Error("error listing tools", "error", err)
		updateMCPState(name, MCPStateError, err, nil, 0)
		c.Close()
		return nil
	}
	dur := time.Since(start)
	if mcpWireEnabled() {
		getDefaultMCPManager().bundle(name).logListTools(dur, len(result.Tools))
	}
	mcpTools := make([]tools.BaseTool, 0, len(result.Tools))
	for _, tool := range result.Tools {
		mcpTools = append(mcpTools, &McpTool{
			mcpName:     name,
			tool:        tool,
			permissions: permissions,
			workingDir:  workingDir,
			wire:        wire,
		})
	}
	return mcpTools
}

// SubscribeMCPEvents returns a channel for MCP events
func SubscribeMCPEvents(ctx context.Context) <-chan pubsub.Event[MCPEvent] {
	mgr := getDefaultMCPManager()
	return mgr.broker.Subscribe(ctx)
}

// GetMCPStates returns the current state of all MCP clients
func GetMCPStates() map[string]MCPClientInfo {
	mgr := getDefaultMCPManager()
	states := make(map[string]MCPClientInfo)
	for name, info := range mgr.states.Seq2() {
		states[name] = info
	}
	return states
}

// GetMCPState returns the state of a specific MCP client
func GetMCPState(name string) (MCPClientInfo, bool) {
	return getDefaultMCPManager().states.Get(name)
}

// updateMCPState updates the state of an MCP client and publishes an event
func updateMCPState(name string, state MCPState, err error, client *client.Client, toolCount int) {
	mgr := getDefaultMCPManager()
	prevInfo, _ := mgr.states.Get(name)

	info := MCPClientInfo{
		Name:      name,
		State:     state,
		Error:     err,
		Client:    client,
		ToolCount: toolCount,
	}
	if state == MCPStateConnected {
		info.ConnectedAt = time.Now()
	}
	mgr.states.Set(name, info)

	mgr.broker.Publish(pubsub.UpdatedEvent, MCPEvent{Type: MCPEventStateChanged, Name: name, State: state, Error: err, ToolCount: toolCount})

	if mcpWireEnabled() {
		why := ""
		switch {
		case state == MCPStateDisabled:
			why = "disabled"
		case err != nil:
			why = "error"
		case state == MCPStateStarting:
			why = "boot"
		case state == MCPStateConnected:
			why = "ready"
		}
		getDefaultMCPManager().bundle(name).logState(prevInfo.State, state, why, info.ConnectedAt, toolCount, err)
	}
}

// CloseMCPClients closes all MCP clients. This should be called during application shutdown.
func CloseMCPClients() {
	mgr := getDefaultMCPManager()
	for _, c := range mgr.conns {
		c.Close()
	}
	if mcpWireEnabled() {
		for name := range mgr.conns {
			mgr.bundle(name).logClosed()
		}
	}
	mgr.broker.Shutdown()
}

var mcpInitRequest = mcp.InitializeRequest{
	Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo: mcp.Implementation{
			Name:    "Crush",
			Version: version.Version,
		},
	},
}

func doGetMCPTools(ctx context.Context, permissions permission.Service, cfg *config.Config, factory MCPClientFactory) []tools.BaseTool {
	var wg sync.WaitGroup
	result := csync.NewSlice[tools.BaseTool]()

	// Initialize states for all configured MCPs
	for name, m := range cfg.MCP {
		if m.Disabled {
			updateMCPState(name, MCPStateDisabled, nil, nil, 0)
			slog.Debug("skipping disabled mcp", "name", name)
			continue
		}

		// Set initial starting state
		updateMCPState(name, MCPStateStarting, nil, nil, 0)

		wg.Add(1)
		go func(name string, m config.MCPConfig) {
			defer func() {
				wg.Done()
				if r := recover(); r != nil {
					var err error
					switch v := r.(type) {
					case error:
						err = v
					case string:
						err = fmt.Errorf("panic: %s", v)
					default:
						err = fmt.Errorf("panic: %v", v)
					}
					updateMCPState(name, MCPStateError, err, nil, 0)
					slog.Error("panic in mcp client initialization", "error", err, "name", name)
				}
			}()

			// MCP client startup timeout (connect + initialize + list tools)
			cfgTimeout := 10 * time.Second
			if cfg := config.Get(); cfg != nil && cfg.Options != nil && cfg.Options.MCP != nil && cfg.Options.MCP.InitTimeoutSecs > 0 {
				cfgTimeout = time.Duration(cfg.Options.MCP.InitTimeoutSecs) * time.Second
			}
			ctx, cancel := context.WithTimeout(ctx, cfgTimeout)
			defer cancel()
			var c *client.Client
			var err error
			if factory != nil {
				c, err = factory.New(name, m)
			} else {
				// Ensure transport-level wire logging (stdio/http/sse) is enabled using the per-MCP logger
				wire := perMCPLogger(name)
				c, err = (defaultMCPFactory{wire: wire}).New(name, m)
			}
			if err != nil {
				updateMCPState(name, MCPStateError, err, nil, 0)
				slog.Error("error creating mcp client", "error", err, "name", name)
				return
			}
			// Start the client with a long-lived context so SSE stays connected beyond init timeout
			if err := c.Start(context.Background()); err != nil {
				updateMCPState(name, MCPStateError, err, nil, 0)
				slog.Error("error starting mcp client", "error", err, "name", name)
				_ = c.Close()
				return
			}
			// Per MCP spec lifecycle, clients must send notifications/initialized after initialize.
			// mcp-go does this internally in Client.Initialize.
			// Spec: https://modelcontextprotocol.io/specification/2024-11-05/basic/lifecycle
			if _, err := c.Initialize(ctx, mcpInitRequest); err != nil {
				updateMCPState(name, MCPStateError, err, nil, 0)
				slog.Error("error initializing mcp client", "error", err, "name", name)
				_ = c.Close()
				return
			}

			slog.Info("Initialized mcp client", "name", name)
			mgr := getDefaultMCPManager()
			wire := mgr.wire
			if wire == nil { wire = perMCPLogger(name) }
			mgr.conns[name] = newDefaultMCPConnection(name, c, wire)
			if mcpWireEnabled() {
				mgr.bundle(name).logInit(string(m.Type))
			}

			// Register notification handler to capture progress notifications (once per client)
			b := getDefaultMCPManager().bundle(name)
			if !b.notifierRegistered {
				b.notifierRegistered = true
				c.OnNotification(func(n mcp.JSONRPCNotification) {
				if n.Method != "notifications/progress" {
					return
				}
				// Extract progressToken, progress, total, message
				var tok string
				if v, ok := n.Params.AdditionalFields["progressToken"]; ok {
					switch t := v.(type) {
					case string:
						tok = t
					default:
						tok = fmt.Sprintf("%v", t)
					}
				}
				msg, _ := n.Params.AdditionalFields["message"].(string)
				var prog, total float64
				if pv, ok := n.Params.AdditionalFields["progress"].(float64); ok {
					prog = pv
				}
				if tv, ok := n.Params.AdditionalFields["total"].(float64); ok {
					total = tv
				}
				getDefaultMCPManager().bundle(name).dispatchProgress(tok, msg, prog, total)
				})
			}

			tools := getTools(ctx, name, permissions, c, cfg.WorkingDir(), wire)
			updateMCPState(name, MCPStateConnected, nil, c, len(tools))
			result.Append(tools...)
		}(name, m)
	}
	wg.Wait()
	return slices.Collect(result.Seq())
}

type defaultMCPFactory struct{ wire MCPWireLogger }

func (f defaultMCPFactory) New(name string, m config.MCPConfig) (*client.Client, error) {
	switch m.Type {
	case config.MCPStdio:
		return client.NewStdioMCPClientWithOptions(
			m.Command,
			m.ResolvedEnv(),
			m.Args,
			transport.WithCommandLogger(mcpLogger{name: name, wire: f.wire, kind: "stdio"}),
		)
	case config.MCPHttp:
		return client.NewStreamableHttpClient(
			m.URL,
			transport.WithHTTPHeaders(m.ResolvedHeaders()),
			transport.WithHTTPLogger(mcpLogger{name: name, wire: f.wire, kind: "http"}),
		)
	case config.MCPSse:
		return client.NewSSEMCPClient(
			m.URL,
			client.WithHeaders(m.ResolvedHeaders()),
			transport.WithSSELogger(mcpLogger{name: name, wire: f.wire, kind: "sse"}),
		)
	default:
		return nil, fmt.Errorf("unsupported mcp type: %s", m.Type)
	}
}

// for MCP's clients.
type mcpLogger struct {
	name string
	wire MCPWireLogger
	kind string // stdio | http | sse
}

// progress notification registry

func (l mcpLogger) Errorf(format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	slog.Error(msg)
	if l.name != "" && l.wire != nil && l.wire.Enabled() {
		// Log raw wire text under its transport kind (stdio/http/sse)
		l.wire.LogStdio(l.name, l.kind, msg)
	}
}

func (l mcpLogger) Infof(format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	slog.Info(msg)
	if l.name != "" && l.wire != nil && l.wire.Enabled() {
		// Log raw wire text under its transport kind (stdio/http/sse)
		l.wire.LogStdio(l.name, l.kind, msg)
	}
}
