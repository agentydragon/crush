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
	mcpClients   = csync.NewMap[string, *client.Client]()
	mcpStates    = csync.NewMap[string, MCPClientInfo]()
	mcpBroker    = pubsub.NewBroker[MCPEvent]()
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
	c, ok := mcpClients.Get(name)
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
	sink := tools.SinkFromContext(ctx)
	// progress message updated via MCP notifications
	updateWaiting := func(detail string) {
		sink.Update(tools.ToolState{Phase: tools.PhaseWaiting, Title: "Waiting for MCP server response…", Detail: detail})
	}
	updateWaiting(fmt.Sprintf("server=%s tool=%s", b.mcpName, b.tool.Name))
	deadline, hasDeadline := callCtx.Deadline()
	ticker := time.NewTicker(1 * time.Second)
	done := make(chan struct{})
	progressCh := make(chan string, 4)
	// Generate a progress token and register listener
	progressToken := fmt.Sprintf("crush-%d", time.Now().UnixNano())
	registerProgressListener(b.mcpName, progressToken, func(msg string, prog, total float64) {
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
		select { case progressCh <- detail: default: }
	})
	defer unregisterProgressListener(b.mcpName, progressToken)
	go func() {
		var lastDetail string
		for {
			select {
			case d := <-progressCh:
				lastDetail = d
				if hasDeadline {
					rem := time.Until(deadline).Round(time.Second)
					if rem < 0 { rem = 0 }
					lastDetail = fmt.Sprintf("%s remaining=%s", lastDetail, rem)
				}
				updateWaiting(lastDetail)
			case <-ticker.C:
				elapsed := time.Since(start).Round(time.Second)
				detail := lastDetail
				if detail == "" {
					detail = fmt.Sprintf("server=%s tool=%s elapsed=%s", b.mcpName, b.tool.Name, elapsed)
				} else {
					detail = fmt.Sprintf("%s elapsed=%s", detail, elapsed)
				}
				if hasDeadline {
					rem := time.Until(deadline).Round(time.Second)
					if rem < 0 { rem = 0 }
					detail = fmt.Sprintf("%s remaining=%s", detail, rem)
				}
				updateWaiting(detail)
			case <-done:
				return
			}
		}
	}()
	resp, err := runTool(callCtx, b.mcpName, b.tool.Name, params.Input, &mcp.Meta{ProgressToken: progressToken})
	close(done)
	ticker.Stop()
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
	result, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		slog.Error("error listing tools", "error", err)
		updateMCPState(name, MCPStateError, err, nil, 0)
		c.Close()
		mcpClients.Del(name)
		return nil
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
	// Temporary proxy to existing broker until manager fully replaces it
	return mcpBroker.Subscribe(ctx)
}

// GetMCPStates returns the current state of all MCP clients
func GetMCPStates() map[string]MCPClientInfo {
	states := make(map[string]MCPClientInfo)
	for name, info := range mcpStates.Seq2() {
		states[name] = info
	}
	return states
}

// GetMCPState returns the state of a specific MCP client
func GetMCPState(name string) (MCPClientInfo, bool) {
	return mcpStates.Get(name)
}

// updateMCPState updates the state of an MCP client and publishes an event
func updateMCPState(name string, state MCPState, err error, client *client.Client, toolCount int) {
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
	mcpStates.Set(name, info)

	// Publish state change event
	mcpBroker.Publish(pubsub.UpdatedEvent, MCPEvent{
		Type:      MCPEventStateChanged,
		Name:      name,
		State:     state,
		Error:     err,
		ToolCount: toolCount,
	})
}

// CloseMCPClients closes all MCP clients. This should be called during application shutdown.
func CloseMCPClients() {
	for c := range mcpClients.Seq() {
		_ = c.Close()
	}
	mcpBroker.Shutdown()
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
				c, err = defaultMCPFactory{}.New(name, m)
			}
			if err != nil {
				updateMCPState(name, MCPStateError, err, nil, 0)
				slog.Error("error creating mcp client", "error", err, "name", name)
				return
			}
			if err := c.Start(ctx); err != nil {
				updateMCPState(name, MCPStateError, err, nil, 0)
				slog.Error("error starting mcp client", "error", err, "name", name)
				_ = c.Close()
				return
			}
			if _, err := c.Initialize(ctx, mcpInitRequest); err != nil {
				updateMCPState(name, MCPStateError, err, nil, 0)
				slog.Error("error initializing mcp client", "error", err, "name", name)
				_ = c.Close()
				return
			}

			slog.Info("Initialized mcp client", "name", name)
			mcpClients.Set(name, c)

			// Register notification handler to capture progress notifications (once per client)
			if _, ok := mcpNotifyOnce.Get(name); !ok {
				mcpNotifyOnce.Set(name, true)
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
				dispatchProgress(name, tok, msg, prog, total)
				})
			}

			tools := getTools(ctx, name, permissions, c, cfg.WorkingDir(), nil)
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
var (
	mcpNotifyOnce = csync.NewMap[string, bool]()
	progressMu    sync.RWMutex
	progress      = map[string]map[string]func(string, float64, float64){} // mcpName -> token -> cb(msg, progress, total)
)

func registerProgressListener(mcpName, token string, cb func(string, float64, float64)) {
	progressMu.Lock()
	defer progressMu.Unlock()
	m, ok := progress[mcpName]
	if !ok {
		m = make(map[string]func(string, float64, float64))
		progress[mcpName] = m
	}
	m[token] = cb
}

func unregisterProgressListener(mcpName, token string) {
	progressMu.Lock()
	defer progressMu.Unlock()
	if m, ok := progress[mcpName]; ok {
		delete(m, token)
	}
}

func dispatchProgress(mcpName, token, msg string, prog, total float64) {
	if token == "" {
		return
	}
	progressMu.RLock()
	cb := progress[mcpName][token]
	progressMu.RUnlock()
	if cb != nil {
		cb(msg, prog, total)
	}
}

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
