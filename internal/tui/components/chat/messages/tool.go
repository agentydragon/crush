package messages

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/v2/key"
	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/diff"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/tui/components/anim"
	"github.com/charmbracelet/crush/internal/tui/components/core/layout"
	"github.com/charmbracelet/crush/internal/tui/styles"
	"github.com/charmbracelet/crush/internal/tui/util"
	"github.com/charmbracelet/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ToolCallCmp defines the interface for tool call components in the chat interface.
// It manages the display of tool execution including pending states, results, and errors.
type ToolCallCmp interface {
	util.Model                         // Basic Bubble Tea model interface
	layout.Sizeable                    // Width/height management
	layout.Focusable                   // Focus state management
	GetToolCall() message.ToolCall     // Access to tool call data
	GetToolResult() message.ToolResult // Access to tool result data
	SetToolResult(message.ToolResult)  // Update tool result
	SetToolCall(message.ToolCall)      // Update tool call
	SetCancelled()                     // Mark as cancelled
	ParentMessageID() string           // Get parent message ID
	Spinning() bool                    // Animation state for pending tools
	GetNestedToolCalls() []ToolCallCmp // Get nested tool calls
	SetNestedToolCalls([]ToolCallCmp)  // Set nested tool calls
	SetIsNested(bool)                  // Set whether this tool call is nested
	ID() string
	SetPermissionRequested() // Mark permission request
	SetPermissionGranted()   // Mark permission granted
	SetLiveToolState(state tools.ToolState)
}

// toolCallCmp implements the ToolCallCmp interface for displaying tool calls.
// It handles rendering of tool execution states including pending, completed, and error states.
type toolCallCmp struct {
	width       int  // Component width for text wrapping
	focused     bool // Focus state for border styling
	isNested    bool // Whether this tool call is nested within another
	createdAtMS int64

	// Tool call data and state
	parentMessageID     string             // ID of the message that initiated this tool call
	call                message.ToolCall   // The tool call being executed
	result              message.ToolResult // The result of the tool execution
	cancelled           bool               // Whether the tool call was cancelled
	permissionRequested bool
	permissionGranted   bool

	// Live tool state (from streaming sink)
	liveTitle       string
	liveDetail      string
	liveSet         bool
	liveStartedAtMS int64
	liveDeadlineMS  int64

	// Animation state for pending tool calls
	spinning bool       // Whether to show loading animation
	anim     util.Model // Animation component for pending states

	nestedToolCalls []ToolCallCmp // Nested tool calls for hierarchical display

	// Cached renderings for heavy content
	cachedPlainKey string
	cachedPlain    string
	cachedCodeKey  string
	cachedCode     string
}

// ToolCallOption provides functional options for configuring tool call components
type ToolCallOption func(*toolCallCmp)

// WithToolCallCancelled marks the tool call as cancelled
func WithToolCallCancelled() ToolCallOption {
	return func(m *toolCallCmp) {
		m.cancelled = true
	}
}

// WithToolCallResult sets the initial tool result
func WithToolCallResult(result message.ToolResult) ToolCallOption {
	return func(m *toolCallCmp) {
		m.result = result
	}
}

func WithToolCallNested(isNested bool) ToolCallOption {
	return func(m *toolCallCmp) {
		m.isNested = isNested
	}
}

func WithToolCallNestedCalls(calls []ToolCallCmp) ToolCallOption {
	return func(m *toolCallCmp) {
		m.nestedToolCalls = calls
	}
}

func WithToolPermissionRequested() ToolCallOption {
	return func(m *toolCallCmp) {
		m.permissionRequested = true
	}
}

func WithToolPermissionGranted() ToolCallOption {
	return func(m *toolCallCmp) {
		m.permissionGranted = true
	}
}

// NewToolCallCmp creates a new tool call component with the given parent message ID,
// tool call, and optional configuration
func NewToolCallCmp(parentMessageID string, tc message.ToolCall, permissions permission.Service, opts ...ToolCallOption) ToolCallCmp {
	m := &toolCallCmp{
		call:            tc,
		parentMessageID: parentMessageID,
		createdAtMS:     time.Now().UnixMilli(),
	}
	for _, opt := range opts {
		opt(m)
	}
	t := styles.CurrentTheme()
	m.anim = anim.New(anim.Settings{
		Size:        15,
		Label:       "Working",
		GradColorA:  t.Primary,
		GradColorB:  t.Secondary,
		LabelColor:  t.FgBase,
		CycleColors: true,
	})
	if m.isNested {
		m.anim = anim.New(anim.Settings{
			Size:        10,
			GradColorA:  t.Primary,
			GradColorB:  t.Secondary,
			CycleColors: true,
		})
	}
	return m
}

// Init initializes the tool call component and starts animations if needed.
// Returns a command to start the animation for pending tool calls.
func (m *toolCallCmp) Init() tea.Cmd {
	m.spinning = m.shouldSpin()
	return m.anim.Init()
}

// Update handles incoming messages and updates the component state.
// Manages animation updates for pending tool calls.
func (m *toolCallCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case anim.StepMsg:
		var cmds []tea.Cmd
		for i, nested := range m.nestedToolCalls {
			if nested.Spinning() {
				u, cmd := nested.Update(msg)
				m.nestedToolCalls[i] = u.(ToolCallCmp)
				cmds = append(cmds, cmd)
			}
		}
		if m.spinning {
			m.updateAnimLabel()
			u, cmd := m.anim.Update(msg)
			m.anim = u.(util.Model)
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	case tea.KeyPressMsg:
		if key.Matches(msg, CopyKey) {
			return m, m.copyTool()
		}
	}
	return m, nil
}

// View renders the tool call component based on its current state.
// Shows either a pending animation or the tool-specific rendered result.
func (m *toolCallCmp) View() string {
	box := m.style()

	if !m.cancelled && m.result.ToolCallID == "" {
		return box.Render(m.renderPending())
	}

	r := registry.lookup(m.call.Name)

	content := r.Render(m)
	if meta := m.debugMeta(); meta != "" {
		content = content + "\n" + styles.CurrentTheme().S().Base.Foreground(styles.CurrentTheme().FgMuted).Render(meta)
	}
	if m.isNested {
		return box.Render(content)
	}
	return box.Render(content)
}

// State management methods

// SetCancelled marks the tool call as cancelled
func (m *toolCallCmp) SetCancelled() {
	m.cancelled = true
	m.spinning = false
	m.invalidateCache()
}

func (m *toolCallCmp) copyTool() tea.Cmd {
	content := m.formatToolForCopy()
	return tea.Sequence(
		tea.SetClipboard(content),
		func() tea.Msg {
			_ = clipboard.WriteAll(content)
			return nil
		},
		util.ReportInfo("Tool content copied to clipboard"),
	)
}

func (m *toolCallCmp) formatToolForCopy() string {
	var parts []string

	toolName := prettifyToolName(m.call.Name)
	parts = append(parts, fmt.Sprintf("## %s Tool Call", toolName))

	if m.call.Input != "" {
		params := m.formatParametersForCopy()
		if params != "" {
			parts = append(parts, "### Parameters:")
			parts = append(parts, params)
		}
	}

	if m.result.ToolCallID != "" {
		if m.result.IsError {
			parts = append(parts, "### Error:")
			parts = append(parts, m.result.Content)
		} else {
			parts = append(parts, "### Result:")
			content := m.formatResultForCopy()
			if content != "" {
				parts = append(parts, content)
			}
		}
	} else if m.cancelled {
		parts = append(parts, "### Status:")
		parts = append(parts, "Cancelled")
	} else {
		parts = append(parts, "### Status:")
		parts = append(parts, "Pending...")
	}

	return strings.Join(parts, "\n\n")
}

func (m *toolCallCmp) formatParametersForCopy() string {
	switch m.call.Name {
	case tools.BashToolName:
		var params tools.BashParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			cmd := strings.ReplaceAll(params.Command, "\n", " ")
			cmd = strings.ReplaceAll(cmd, "\t", "    ")
			return fmt.Sprintf("**Command:** %s", cmd)
		}
	case tools.ViewToolName:
		var params tools.ViewParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**File:** %s", fsext.PrettyPath(params.FilePath)))
			if params.Limit > 0 {
				parts = append(parts, fmt.Sprintf("**Limit:** %d", params.Limit))
			}
			if params.Offset > 0 {
				parts = append(parts, fmt.Sprintf("**Offset:** %d", params.Offset))
			}
			return strings.Join(parts, "\n")
		}
	case tools.EditToolName:
		var params tools.EditParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			return fmt.Sprintf("**File:** %s", fsext.PrettyPath(params.FilePath))
		}
	case tools.MultiEditToolName:
		var params tools.MultiEditParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**File:** %s", fsext.PrettyPath(params.FilePath)))
			parts = append(parts, fmt.Sprintf("**Edits:** %d", len(params.Edits)))
			return strings.Join(parts, "\n")
		}
	case tools.WriteToolName:
		var params tools.WriteParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			return fmt.Sprintf("**File:** %s", fsext.PrettyPath(params.FilePath))
		}
	case tools.FetchToolName:
		var params tools.FetchParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**URL:** %s", params.URL))
			if params.Format != "" {
				parts = append(parts, fmt.Sprintf("**Format:** %s", params.Format))
			}
			if params.Timeout > 0 {
				parts = append(parts, fmt.Sprintf("**Timeout:** %s", (time.Duration(params.Timeout)*time.Second).String()))
			}
			return strings.Join(parts, "\n")
		}
	case tools.GrepToolName:
		var params tools.GrepParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**Pattern:** %s", params.Pattern))
			if params.Path != "" {
				parts = append(parts, fmt.Sprintf("**Path:** %s", params.Path))
			}
			if params.Include != "" {
				parts = append(parts, fmt.Sprintf("**Include:** %s", params.Include))
			}
			if params.LiteralText {
				parts = append(parts, "**Literal:** true")
			}
			return strings.Join(parts, "\n")
		}
	case tools.GlobToolName:
		var params tools.GlobParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**Pattern:** %s", params.Pattern))
			if params.Path != "" {
				parts = append(parts, fmt.Sprintf("**Path:** %s", params.Path))
			}
			return strings.Join(parts, "\n")
		}
	case tools.LSToolName:
		var params tools.LSParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			path := params.Path
			if path == "" {
				path = "."
			}
			return fmt.Sprintf("**Path:** %s", fsext.PrettyPath(path))
		}
	case tools.DownloadToolName:
		var params tools.DownloadParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**URL:** %s", params.URL))
			parts = append(parts, fmt.Sprintf("**File Path:** %s", fsext.PrettyPath(params.FilePath)))
			if params.Timeout > 0 {
				parts = append(parts, fmt.Sprintf("**Timeout:** %s", (time.Duration(params.Timeout)*time.Second).String()))
			}
			return strings.Join(parts, "\n")
		}
	case tools.SourcegraphToolName:
		var params tools.SourcegraphParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**Query:** %s", params.Query))
			if params.Count > 0 {
				parts = append(parts, fmt.Sprintf("**Count:** %d", params.Count))
			}
			if params.ContextWindow > 0 {
				parts = append(parts, fmt.Sprintf("**Context:** %d", params.ContextWindow))
			}
			return strings.Join(parts, "\n")
		}
	case tools.DiagnosticsToolName:
		return "**Project:** diagnostics"
	case agent.AgentToolName:
		var params agent.AgentParams
		if json.Unmarshal([]byte(m.call.Input), &params) == nil {
			return fmt.Sprintf("**Task:**\n%s", params.Prompt)
		}
	}

	var params map[string]any
	if json.Unmarshal([]byte(m.call.Input), &params) == nil {
		var parts []string
		for key, value := range params {
			displayKey := strings.ReplaceAll(key, "_", " ")
			if len(displayKey) > 0 {
				displayKey = strings.ToUpper(displayKey[:1]) + displayKey[1:]
			}
			parts = append(parts, fmt.Sprintf("**%s:** %v", displayKey, value))
		}
		return strings.Join(parts, "\n")
	}

	return ""
}

func (m *toolCallCmp) formatResultForCopy() string {
	switch m.call.Name {
	case tools.BashToolName:
		return m.formatBashResultForCopy()
	case tools.ViewToolName:
		return m.formatViewResultForCopy()
	case tools.EditToolName:
		return m.formatEditResultForCopy()
	case tools.MultiEditToolName:
		return m.formatMultiEditResultForCopy()
	case tools.WriteToolName:
		return m.formatWriteResultForCopy()
	case tools.FetchToolName:
		return m.formatFetchResultForCopy()
	case agent.AgentToolName:
		return m.formatAgentResultForCopy()
	case tools.DownloadToolName, tools.GrepToolName, tools.GlobToolName, tools.LSToolName, tools.SourcegraphToolName, tools.DiagnosticsToolName:
		return fmt.Sprintf("```\n%s\n```", m.result.Content)
	default:
		return m.result.Content
	}
}

func (m *toolCallCmp) formatBashResultForCopy() string {
	var meta tools.BashResponseMetadata
	if m.result.Metadata != "" {
		json.Unmarshal([]byte(m.result.Metadata), &meta)
	}

	output := meta.Output
	if output == "" && m.result.Content != tools.BashNoOutput {
		output = m.result.Content
	}

	if output == "" {
		return ""
	}

	return fmt.Sprintf("```bash\n%s\n```", output)
}

func (m *toolCallCmp) formatViewResultForCopy() string {
	var meta tools.ViewResponseMetadata
	if m.result.Metadata != "" {
		json.Unmarshal([]byte(m.result.Metadata), &meta)
	}

	if meta.Content == "" {
		return m.result.Content
	}

	lang := ""
	if meta.FilePath != "" {
		ext := strings.ToLower(filepath.Ext(meta.FilePath))
		switch ext {
		case ".go":
			lang = "go"
		case ".js", ".mjs":
			lang = "javascript"
		case ".ts":
			lang = "typescript"
		case ".py":
			lang = "python"
		case ".rs":
			lang = "rust"
		case ".java":
			lang = "java"
		case ".c":
			lang = "c"
		case ".cpp", ".cc", ".cxx":
			lang = "cpp"
		case ".sh", ".bash":
			lang = "bash"
		case ".json":
			lang = "json"
		case ".yaml", ".yml":
			lang = "yaml"
		case ".xml":
			lang = "xml"
		case ".html":
			lang = "html"
		case ".css":
			lang = "css"
		case ".md":
			lang = "markdown"
		}
	}

	var result strings.Builder
	if lang != "" {
		result.WriteString(fmt.Sprintf("```%s\n", lang))
	} else {
		result.WriteString("```\n")
	}
	result.WriteString(meta.Content)
	result.WriteString("\n```")

	return result.String()
}

func (m *toolCallCmp) formatEditResultForCopy() string {
	var meta tools.EditResponseMetadata
	if m.result.Metadata == "" {
		return m.result.Content
	}

	if json.Unmarshal([]byte(m.result.Metadata), &meta) != nil {
		return m.result.Content
	}

	var params tools.EditParams
	json.Unmarshal([]byte(m.call.Input), &params)

	var result strings.Builder

	if meta.OldContent != "" || meta.NewContent != "" {
		fileName := params.FilePath
		if fileName != "" {
			fileName = fsext.PrettyPath(fileName)
		}
		diffContent, additions, removals := diff.GenerateDiff(meta.OldContent, meta.NewContent, fileName)

		result.WriteString(fmt.Sprintf("Changes: +%d -%d\n", additions, removals))
		result.WriteString("```diff\n")
		result.WriteString(diffContent)
		result.WriteString("\n```")
	}

	return result.String()
}

func (m *toolCallCmp) formatMultiEditResultForCopy() string {
	var meta tools.MultiEditResponseMetadata
	if m.result.Metadata == "" {
		return m.result.Content
	}

	if json.Unmarshal([]byte(m.result.Metadata), &meta) != nil {
		return m.result.Content
	}

	var params tools.MultiEditParams
	json.Unmarshal([]byte(m.call.Input), &params)

	var result strings.Builder
	if meta.OldContent != "" || meta.NewContent != "" {
		fileName := params.FilePath
		if fileName != "" {
			fileName = fsext.PrettyPath(fileName)
		}
		diffContent, additions, removals := diff.GenerateDiff(meta.OldContent, meta.NewContent, fileName)

		result.WriteString(fmt.Sprintf("Changes: +%d -%d\n", additions, removals))
		result.WriteString("```diff\n")
		result.WriteString(diffContent)
		result.WriteString("\n```")
	}

	return result.String()
}

func (m *toolCallCmp) formatWriteResultForCopy() string {
	var params tools.WriteParams
	if json.Unmarshal([]byte(m.call.Input), &params) != nil {
		return m.result.Content
	}

	lang := ""
	if params.FilePath != "" {
		ext := strings.ToLower(filepath.Ext(params.FilePath))
		switch ext {
		case ".go":
			lang = "go"
		case ".js", ".mjs":
			lang = "javascript"
		case ".ts":
			lang = "typescript"
		case ".py":
			lang = "python"
		case ".rs":
			lang = "rust"
		case ".java":
			lang = "java"
		case ".c":
			lang = "c"
		case ".cpp", ".cc", ".cxx":
			lang = "cpp"
		case ".sh", ".bash":
			lang = "bash"
		case ".json":
			lang = "json"
		case ".yaml", ".yml":
			lang = "yaml"
		case ".xml":
			lang = "xml"
		case ".html":
			lang = "html"
		case ".css":
			lang = "css"
		case ".md":
			lang = "markdown"
		}
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("File: %s\n", fsext.PrettyPath(params.FilePath)))
	if lang != "" {
		result.WriteString(fmt.Sprintf("```%s\n", lang))
	} else {
		result.WriteString("```\n")
	}
	result.WriteString(params.Content)
	result.WriteString("\n```")

	return result.String()
}

func (m *toolCallCmp) formatFetchResultForCopy() string {
	var params tools.FetchParams
	if json.Unmarshal([]byte(m.call.Input), &params) != nil {
		return m.result.Content
	}

	var result strings.Builder
	if params.URL != "" {
		result.WriteString(fmt.Sprintf("URL: %s\n", params.URL))
	}

	switch params.Format {
	case "html":
		result.WriteString("```html\n")
	case "text":
		result.WriteString("```\n")
	default: // markdown
		result.WriteString("```markdown\n")
	}
	result.WriteString(m.result.Content)
	result.WriteString("\n```")

	return result.String()
}

func (m *toolCallCmp) formatAgentResultForCopy() string {
	var result strings.Builder

	if len(m.nestedToolCalls) > 0 {
		result.WriteString("### Nested Tool Calls:\n")
		for i, nestedCall := range m.nestedToolCalls {
			nestedContent := nestedCall.(*toolCallCmp).formatToolForCopy()
			indentedContent := strings.ReplaceAll(nestedContent, "\n", "\n  ")
			result.WriteString(fmt.Sprintf("%d. %s\n", i+1, indentedContent))
			if i < len(m.nestedToolCalls)-1 {
				result.WriteString("\n")
			}
		}

		if m.result.Content != "" {
			result.WriteString("\n### Final Result:\n")
		}
	}

	if m.result.Content != "" {
		result.WriteString(fmt.Sprintf("```markdown\n%s\n```", m.result.Content))
	}

	return result.String()
}

// SetToolCall updates the tool call data and stops spinning if finished
func (m *toolCallCmp) SetToolCall(call message.ToolCall) {
	m.call = call
	if m.cancelled {
		m.spinning = false
		m.invalidateCache()
		return
	}
	m.spinning = m.result.ToolCallID == ""
	m.invalidateCache()
}

// ParentMessageID returns the ID of the message that initiated this tool call
func (m *toolCallCmp) ParentMessageID() string {
	return m.parentMessageID
}

// SetToolResult updates the tool result and stops the spinning animation
func (m *toolCallCmp) SetToolResult(result message.ToolResult) {
	m.result = result
	m.spinning = false
	m.invalidateCache()
}

// GetToolCall returns the current tool call data
func (m *toolCallCmp) GetToolCall() message.ToolCall {
	return m.call
}

// GetToolResult returns the current tool result data
func (m *toolCallCmp) GetToolResult() message.ToolResult {
	return m.result
}

// GetNestedToolCalls returns the nested tool calls
func (m *toolCallCmp) GetNestedToolCalls() []ToolCallCmp {
	return m.nestedToolCalls
}

// SetNestedToolCalls sets the nested tool calls
func (m *toolCallCmp) SetNestedToolCalls(calls []ToolCallCmp) {
	m.nestedToolCalls = calls
	for _, nested := range m.nestedToolCalls {
		nested.SetSize(m.width, 0)
	}
	m.invalidateCache()
}

// SetIsNested sets whether this tool call is nested within another
func (m *toolCallCmp) SetIsNested(isNested bool) {
	m.isNested = isNested
	m.invalidateCache()
}

// Rendering methods

// renderPending displays the tool name with a loading animation for pending tool calls
func (m *toolCallCmp) renderPending() string {
	t := styles.CurrentTheme()
	icon := t.S().Base.Foreground(t.GreenDark).Render(styles.ToolPending)
	toolNameStyle := t.S().Base.Foreground(t.Blue)
	if m.isNested {
		toolNameStyle = t.S().Base.Foreground(t.FgHalfMuted)
	}
	tool := toolNameStyle.Render(prettifyToolName(m.call.Name))
	spinner := m.anim.View()
	line := fmt.Sprintf("%s %s %s", icon, tool, spinner)

	parts := []string{line}
	if m.liveSet && (m.liveTitle != "" || m.liveDetail != "") {
		// Do NOT truncate live detail across newlines. Only clamp individual line widths.
		// This preserves multi-line streaming tails like "1\n2\n3" for test visibility.
		title := ""
		detail := ""
		if m.liveTitle != "" {
			title = t.S().Base.Foreground(t.FgHalfMuted).Render(m.fit(m.liveTitle, m.textWidth()-2))
		}
		if m.liveDetail != "" {
			w := m.textWidth() - 2
			if w < 10 {
				w = m.textWidth()
			}
			lines := strings.Split(m.liveDetail, "\n")
			for i, ln := range lines {
				lines[i] = m.fit(ln, w)
			}
			detail = t.S().Base.Foreground(t.FgSubtle).Render(strings.Join(lines, "\n"))
		}
		if title != "" {
			parts = append(parts, title)
		}
		if detail != "" {
			parts = append(parts, detail)
		}
		slog.Info("tool.pending.render", "id", m.call.ID, "live", m.liveSet, "title", m.liveTitle, "detail", m.liveDetail)
	} else if !m.liveSet && m.call.Input != "" {
		// Generic fallback: show parameters inline so the user sees what will run.
		inline := ""
		// Try object first.
		var obj map[string]any
		if json.Unmarshal([]byte(m.call.Input), &obj) == nil && len(obj) > 0 {
			var kvs []string
			for k, v := range obj {
				vs := strings.ReplaceAll(fmt.Sprint(v), "\n", " ")
				kvs = append(kvs, k+"="+vs)
			}
			inline = strings.Join(kvs, " ")
		} else {
			// Try string-wrapped JSON, then fall back to raw input.
			var s string
			if json.Unmarshal([]byte(m.call.Input), &s) == nil && s != "" {
				inline = strings.ReplaceAll(s, "\n", " ")
			} else {
				inline = strings.ReplaceAll(m.call.Input, "\n", " ")
			}
		}
		if inline != "" {
			fallback := t.S().Base.Foreground(t.FgHalfMuted).Render(m.fit(inline, m.textWidth()-2))
			parts = append(parts, fallback)
		}
	}

	if meta := m.debugMeta(); meta != "" {
		parts = append(parts, t.S().Base.Foreground(t.FgMuted).Render(meta))
	}
	return strings.Join(parts, "\n")
}

func (m *toolCallCmp) debugMeta() string {
	cfg := config.Get()
	if cfg == nil || cfg.Options == nil || !cfg.Options.Debug {
		return ""
	}
	state := "pending"
	if m.cancelled {
		state = "cancelled"
	} else if m.result.ToolCallID != "" {
		if m.result.IsError {
			state = "error"
		} else {
			state = "result"
		}
	} else if m.liveSet {
		state = "live"
	}
	elapsed := ""
	if m.liveStartedAtMS > 0 {
		el := time.Since(time.UnixMilli(m.liveStartedAtMS)).Round(time.Second)
		elapsed = el.String()
	}
	remain := ""
	if m.liveDeadlineMS > 0 {
		dl := time.UnixMilli(m.liveDeadlineMS)
		now := time.Now()
		if dl.After(now) {
			remain = (dl.Sub(now)).Round(time.Second).String()
		} else {
			remain = "overdue " + (now.Sub(dl)).Round(time.Second).String()
		}
	}
	meta := fmt.Sprintf("id=%s state=%s", m.call.ID, state)
	if elapsed != "" {
		meta += " elapsed=" + elapsed
	}
	if remain != "" {
		meta += " remaining=" + remain
	}
	if last := pubsub.LastDropUnixMS(); last > 0 && last > m.createdAtMS {
		meta += " drops"
	}
	return meta
}

// style returns the lipgloss style for the tool call component.
// Applies muted colors and focus-dependent border styles.
func (m *toolCallCmp) style() lipgloss.Style {
	t := styles.CurrentTheme()

	if m.isNested {
		return t.S().Muted
	}
	style := t.S().Muted.PaddingLeft(4)

	if m.focused {
		style = style.PaddingLeft(3).BorderStyle(focusedMessageBorder).BorderLeft(true).BorderForeground(t.GreenDark)
	}
	return style
}


func (m *toolCallCmp) SetLiveToolState(state tools.ToolState) {
	m.liveTitle = state.Title
	m.liveDetail = state.Detail
	m.liveSet = true
	if state.StartedAt > 0 {
		m.liveStartedAtMS = state.StartedAt
	} else if m.liveStartedAtMS == 0 {
		m.liveStartedAtMS = time.Now().UnixMilli()
	}
	if state.Meta != nil {
		if v, ok := state.Meta["deadline_unix_ms"]; ok {
			switch x := v.(type) {
			case int64:
				m.liveDeadlineMS = x
			case int:
				m.liveDeadlineMS = int64(x)
			case float64:
				m.liveDeadlineMS = int64(x)
			case json.Number:
				if n, err := x.Int64(); err == nil {
					m.liveDeadlineMS = n
				}
			}
		}
	}
	// Invalidate cached render and log for diagnosis.
	slog.Info("tool.pending.live_set", "id", m.call.ID, "width", m.width, "detail_len", len(m.liveDetail), "detail", m.liveDetail)
	m.invalidateCache()
}

func (m *toolCallCmp) updateAnimLabel() {
	if m.isNested {
		return
	}
	label := "Working"
	if m.liveStartedAtMS > 0 {
		start := time.UnixMilli(m.liveStartedAtMS)
		elapsed := time.Since(start).Round(time.Second)
		label = fmt.Sprintf("Working %s", elapsed)
		if m.liveDeadlineMS > 0 {
			deadline := time.UnixMilli(m.liveDeadlineMS)
			now := time.Now()
			if deadline.After(now) {
				rem := deadline.Sub(now).Round(time.Second)
				label = fmt.Sprintf("Working %s · %s left", elapsed, rem)
			} else if now.After(deadline) {
				overdue := now.Sub(deadline).Round(time.Second)
				label = fmt.Sprintf("Working %s · overdue %s", elapsed, overdue)
			}
		}
	}
	if a, ok := m.anim.(*anim.Anim); ok {
		a.SetLabel(label)
	}
}

// textWidth calculates the available width for text content,
// accounting for borders and padding
func (m *toolCallCmp) textWidth() int {
	if m.isNested {
		return m.width - 6
	}
	return m.width - 5 // take into account the border and PaddingLeft
}

// fit truncates content to fit within the specified width with ellipsis
func (m *toolCallCmp) fit(content string, width int) string {
	t := styles.CurrentTheme()
	lineStyle := t.S().Muted
	dots := lineStyle.Render("…")
	return ansi.Truncate(content, width, dots)
}

func (m *toolCallCmp) invalidateCache() {
	m.cachedPlainKey = ""
	m.cachedPlain = ""
	m.cachedCodeKey = ""
	m.cachedCode = ""
}

// Focus management methods

// Blur removes focus from the tool call component
func (m *toolCallCmp) Blur() tea.Cmd {
	m.focused = false
	return nil
}

// Focus sets focus on the tool call component
func (m *toolCallCmp) Focus() tea.Cmd {
	m.focused = true
	return nil
}

// IsFocused returns whether the tool call component is currently focused
func (m *toolCallCmp) IsFocused() bool {
	return m.focused
}

// Size management methods

// GetSize returns the current dimensions of the tool call component
func (m *toolCallCmp) GetSize() (int, int) {
	return m.width, 0
}

// SetSize updates the width of the tool call component for text wrapping
func (m *toolCallCmp) SetSize(width int, height int) tea.Cmd {
	m.width = width
	for _, nested := range m.nestedToolCalls {
		nested.SetSize(width, height)
	}
	m.invalidateCache()
	return nil
}

// shouldSpin determines whether the tool call should show a loading animation.
// Returns true if the tool call is not finished or if the result doesn't match the call ID.
func (m *toolCallCmp) shouldSpin() bool {
	return m.result.ToolCallID == "" && !m.cancelled
}

// Spinning returns whether the tool call is currently showing a loading animation
func (m *toolCallCmp) Spinning() bool {
	if m.spinning {
		return true
	}
	for _, nested := range m.nestedToolCalls {
		if nested.Spinning() {
			return true
		}
	}
	return m.spinning
}

func (m *toolCallCmp) ID() string {
	return m.call.ID
}

// SetPermissionRequested marks that a permission request was made for this tool call
func (m *toolCallCmp) SetPermissionRequested() {
	m.permissionRequested = true
}

// SetPermissionGranted marks that permission was granted for this tool call
func (m *toolCallCmp) SetPermissionGranted() {
	m.permissionGranted = true
}
