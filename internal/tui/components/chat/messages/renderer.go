package messages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/ansiext"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/tui/components/core"
	"github.com/charmbracelet/crush/internal/tui/highlight"
	"github.com/charmbracelet/crush/internal/tui/styles"
	"github.com/charmbracelet/lipgloss/v2"
	"github.com/charmbracelet/lipgloss/v2/tree"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/crush/internal/format/lineno"
)

// responseContextHeight limits the number of lines displayed in tool output
const responseContextHeight = 10

// renderer defines the interface for tool-specific rendering implementations
type renderer interface {
	// Render returns the complete (already styled) tool‑call view, not
	// including the outer border.
	Render(v *toolCallCmp) string
}

// rendererFactory creates new renderer instances
type rendererFactory func() renderer

// renderRegistry manages the mapping of tool names to their renderers
type renderRegistry map[string]rendererFactory

// register adds a new renderer factory to the registry
func (rr renderRegistry) register(name string, f rendererFactory) { rr[name] = f }

// lookup retrieves a renderer for the given tool name, falling back to generic renderer
func (rr renderRegistry) lookup(name string) renderer {
	if f, ok := rr[name]; ok {
		return f()
	}
	return genericRenderer{} // sensible fallback
}

// registry holds all registered tool renderers
var registry = renderRegistry{}

// baseRenderer provides common functionality for all tool renderers
type baseRenderer struct{}

// paramBuilder helps construct parameter lists for tool headers
type paramBuilder struct {
	args []string
}

// newParamBuilder creates a new parameter builder
func newParamBuilder() *paramBuilder {
	return &paramBuilder{args: make([]string, 0)}
}

// addMain adds the main parameter (first argument)
func (pb *paramBuilder) addMain(value string) *paramBuilder {
	if value != "" {
		pb.args = append(pb.args, value)
	}
	return pb
}

// addKeyValue adds a key-value pair parameter
func (pb *paramBuilder) addKeyValue(key, value string) *paramBuilder {
	if value != "" {
		pb.args = append(pb.args, key, value)
	}
	return pb
}

// addFlag adds a boolean flag parameter
func (pb *paramBuilder) addFlag(key string, value bool) *paramBuilder {
	if value {
		pb.args = append(pb.args, key, "true")
	}
	return pb
}

// build returns the final parameter list
func (pb *paramBuilder) build() []string {
	return pb.args
}

// renderWithParams provides a common rendering pattern for tools with parameters
func (br baseRenderer) renderWithParams(v *toolCallCmp, toolName string, args []string, contentRenderer func() string) string {
	width := v.textWidth()
	if v.isNested {
		width -= 4 // Adjust for nested tool call indentation
	}
	header := br.makeHeader(v, toolName, width, args...)
	if v.isNested {
		return v.style().Render(header)
	}
	if res, done := earlyState(header, v); done {
		return res
	}
	body := contentRenderer()
	return joinHeaderBody(header, body)
}

// unmarshalParams safely unmarshal JSON parameters
func (br baseRenderer) unmarshalParams(input string, target any) error {
	return json.Unmarshal([]byte(input), target)
}

// parseMetadataOrPlain tries to unmarshal tool result metadata into target.
// Returns (true, "") on success. On failure returns (false, plainContent) where
// plainContent is a pre-rendered fallback using v.result.Content.
func (br baseRenderer) parseMetadataOrPlain(v *toolCallCmp, target any) (bool, string) {
	if err := json.Unmarshal([]byte(v.result.Metadata), target); err != nil {
		return false, renderPlainContent(v, v.result.Content)
	}
	return true, ""
}

// makeHeader builds the tool call header with status icon and parameters for a nested tool call.
func (br baseRenderer) makeNestedHeader(v *toolCallCmp, tool string, width int, params ...string) string {
	return br.makeHeaderCommon(v, tool, width, true, params...)
}

// makeHeader builds "<Tool>: param (key=value)" and truncates as needed.
func (br baseRenderer) makeHeader(v *toolCallCmp, tool string, width int, params ...string) string {
	return br.makeHeaderCommon(v, tool, width, v.isNested, params...)
}

// makeHeaderCommon builds the header; nested controls styling differences.
func (br baseRenderer) makeHeaderCommon(v *toolCallCmp, tool string, width int, nested bool, params ...string) string {
	t := styles.CurrentTheme()
	icon := t.S().Base.Foreground(t.GreenDark).Render(styles.ToolPending)
	if v.result.ToolCallID != "" {
		if v.result.Recovered {
			icon = t.S().Base.Foreground(t.Red).Render(styles.ToolError)
		} else if v.result.IsError {
			icon = t.S().Base.Foreground(t.RedDark).Render(styles.ToolError)
		} else {
			icon = t.S().Base.Foreground(t.Green).Render(styles.ToolSuccess)
		}
	} else if v.cancelled {
		icon = t.S().Muted.Render(styles.ToolPending)
	}
	if nested {
		tool = t.S().Base.Foreground(t.FgHalfMuted).Render(tool)
	} else {
		tool = t.S().Base.Foreground(t.Blue).Render(tool)
	}
	prefix := fmt.Sprintf("%s %s ", icon, tool)
	return prefix + renderParamList(nested, width-lipgloss.Width(prefix), params...)
}

// renderError provides consistent error rendering
func (br baseRenderer) renderError(v *toolCallCmp, message string) string {
	t := styles.CurrentTheme()
	header := br.makeHeader(v, prettifyToolName(v.call.Name), v.textWidth(), "")
	errorTag := t.S().Base.Padding(0, 1).Background(t.Red).Foreground(t.White).Render("ERROR")
	message = t.S().Base.Foreground(t.FgHalfMuted).Render(v.fit(message, v.textWidth()-3-lipgloss.Width(errorTag))) // -2 for padding and space
	return joinHeaderBody(header, errorTag+" "+message)
}

// Register tool renderers
func init() {
	registry.register(tools.BashToolName, func() renderer { return bashRenderer{} })
	registry.register(tools.DownloadToolName, func() renderer { return downloadRenderer{} })
	registry.register(tools.ViewToolName, func() renderer { return viewRenderer{} })
	registry.register(tools.EditToolName, func() renderer { return editRenderer{} })
	registry.register(tools.MultiEditToolName, func() renderer { return multiEditRenderer{} })
	registry.register(tools.WriteToolName, func() renderer { return writeRenderer{} })
	registry.register(tools.FetchToolName, func() renderer { return fetchRenderer{} })
	registry.register(tools.GlobToolName, func() renderer { return globRenderer{} })
	registry.register(tools.GrepToolName, func() renderer { return grepRenderer{} })
	registry.register(tools.LSToolName, func() renderer { return lsRenderer{} })
	registry.register(tools.SourcegraphToolName, func() renderer { return sourcegraphRenderer{} })
	registry.register(tools.DiagnosticsToolName, func() renderer { return diagnosticsRenderer{} })
	registry.register(agent.AgentToolName, func() renderer { return agentRenderer{} })
}

// -----------------------------------------------------------------------------
//  Generic renderer
// -----------------------------------------------------------------------------

// genericRenderer handles unknown tool types with basic parameter display
type genericRenderer struct {
	baseRenderer
}

// Render displays the tool call with its raw input and plain content output
func (gr genericRenderer) Render(v *toolCallCmp) string {
	return gr.renderWithParams(v, prettifyToolName(v.call.Name), []string{v.call.Input}, func() string {
		return renderPlainContent(v, v.result.Content)
	})
}

// -----------------------------------------------------------------------------
//  Bash renderer
// -----------------------------------------------------------------------------

// bashRenderer handles bash command execution display
type bashRenderer struct {
	baseRenderer
}

// Render displays the bash command with sanitized newlines and plain output
func (br bashRenderer) Render(v *toolCallCmp) string {
	var params tools.BashParams
	if err := br.unmarshalParams(v.call.Input, &params); err != nil {
		return br.renderError(v, "Invalid bash parameters")
	}

	cmd := sanitizeInline(params.Command)
	args := newParamBuilder().addMain(cmd).build()

	return br.renderWithParams(v, "Bash", args, func() string {
		var meta tools.BashResponseMetadata
		if ok, fallback := br.parseMetadataOrPlain(v, &meta); !ok {
			return fallback
		}
		// for backwards compatibility with older tool calls.
		if meta.Output == "" && v.result.Content != tools.BashNoOutput {
			meta.Output = v.result.Content
		}

		if meta.Output == "" {
			return ""
		}
		return renderPlainContent(v, meta.Output)
	})
}

// -----------------------------------------------------------------------------
//  View renderer
// -----------------------------------------------------------------------------

// viewRenderer handles file viewing with syntax highlighting and line numbers
type viewRenderer struct {
	baseRenderer
}

// Render displays file content with optional limit and offset parameters
func (vr viewRenderer) Render(v *toolCallCmp) string {
	var params tools.ViewParams
	if err := vr.unmarshalParams(v.call.Input, &params); err != nil {
		return vr.renderError(v, "Invalid view parameters")
	}

	file := fsext.PrettyPath(params.FilePath)
	args := newParamBuilder().
		addMain(file).
		addKeyValue("limit", formatNonZero(params.Limit)).
		addKeyValue("offset", formatNonZero(params.Offset)).
		build()

	return vr.renderWithParams(v, "View", args, func() string {
		var meta tools.ViewResponseMetadata
		if ok, fallback := vr.parseMetadataOrPlain(v, &meta); !ok {
			return fallback
		}
		return renderCodeContent(v, meta.FilePath, meta.Content, params.Offset)
	})
}

// formatNonZero returns string representation of non-zero integers, empty string for zero
func formatNonZero(value int) string {
	if value == 0 {
		return ""
	}
	return fmt.Sprintf("%d", value)
}

// -----------------------------------------------------------------------------
//  Edit renderer
// -----------------------------------------------------------------------------

// editRenderer handles file editing with diff visualization
type editRenderer struct {
	baseRenderer
}

// Render displays the edited file with a formatted diff of changes
func (er editRenderer) Render(v *toolCallCmp) string {
	var params tools.EditParams
	if err := er.unmarshalParams(v.call.Input, &params); err != nil {
		return er.renderError(v, "Invalid edit parameters")
	}
	file := fsext.PrettyPath(params.FilePath)
	args := newParamBuilder().addMain(file).build()

	return er.renderWithParams(v, "Edit", args, func() string {
		var meta tools.EditResponseMetadata
		if ok, fallback := er.parseMetadataOrPlain(v, &meta); !ok {
			return fallback
		}

		formatter := core.DiffFormatter().
			Before(fsext.PrettyPath(params.FilePath), meta.OldContent).
			After(fsext.PrettyPath(params.FilePath), meta.NewContent).
			Width(v.textWidth() - 2) // -2 for padding
		if v.textWidth() > 120 {
			formatter = formatter.Split()
		}
		formatted := formatter.String()
		if lipgloss.Height(formatted) > responseContextHeight {
			contentLines := strings.Split(formatted, "\n")
			truncateMessage := styles.CurrentTheme().S().Muted.
				Background(styles.CurrentTheme().BgBaseLighter).
				PaddingLeft(2).
				Width(v.textWidth() - 2).
				Render(fmt.Sprintf("… (%d lines)", len(contentLines)-responseContextHeight))
			formatted = strings.Join(contentLines[:responseContextHeight], "\n") + "\n" + truncateMessage
		}
		return formatted
	})
}

// -----------------------------------------------------------------------------
//  Multi-Edit renderer
// -----------------------------------------------------------------------------

// multiEditRenderer handles multiple file edits with diff visualization
type multiEditRenderer struct {
	baseRenderer
}

// Render displays the multi-edited file with a formatted diff of changes
func (mer multiEditRenderer) Render(v *toolCallCmp) string {
	// If this tool call ended in a recovered state (partial/invalid input), surface the recovered content
	// instead of showing a parameter error. This clears pending state and matches E2E expectations.
	if v.result.Recovered {
		return mer.renderWithParams(v, "Multi-Edit", nil, func() string {
			return renderPlainContent(v, v.result.Content)
		})
	}

	var params tools.MultiEditParams
	if err := mer.unmarshalParams(v.call.Input, &params); err != nil {
		return mer.renderError(v, "Invalid multi-edit parameters")
	}
	file := fsext.PrettyPath(params.FilePath)
	editsCount := len(params.Edits)
	args := newParamBuilder().
		addMain(file).
		addKeyValue("edits", fmt.Sprintf("%d", editsCount)).
		build()

	return mer.renderWithParams(v, "Multi-Edit", args, func() string {
		var meta tools.MultiEditResponseMetadata
		if ok, fallback := mer.parseMetadataOrPlain(v, &meta); !ok {
			return fallback
		}

		formatter := core.DiffFormatter().
			Before(fsext.PrettyPath(params.FilePath), meta.OldContent).
			After(fsext.PrettyPath(params.FilePath), meta.NewContent).
			Width(v.textWidth() - 2) // -2 for padding
		if v.textWidth() > 120 {
			formatter = formatter.Split()
		}
		formatted := formatter.String()
		if lipgloss.Height(formatted) > responseContextHeight {
			contentLines := strings.Split(formatted, "\n")
			truncateMessage := styles.CurrentTheme().S().Muted.
				Background(styles.CurrentTheme().BgBaseLighter).
				PaddingLeft(2).
				Width(v.textWidth() - 2).
				Render(fmt.Sprintf("… (%d lines)", len(contentLines)-responseContextHeight))
			formatted = strings.Join(contentLines[:responseContextHeight], "\n") + "\n" + truncateMessage
		}
		return formatted
	})
}

// -----------------------------------------------------------------------------
//  Write renderer
// -----------------------------------------------------------------------------

// writeRenderer handles file writing with syntax-highlighted content preview
type writeRenderer struct {
	baseRenderer
}

// Render displays the file being written with syntax highlighting
func (wr writeRenderer) Render(v *toolCallCmp) string {
	var params tools.WriteParams
	if err := wr.unmarshalParams(v.call.Input, &params); err != nil {
		return wr.renderError(v, "Invalid write parameters")
	}
	file := fsext.PrettyPath(params.FilePath)
	args := newParamBuilder().addMain(file).build()

	return wr.renderWithParams(v, "Write", args, func() string {
		return renderCodeContent(v, file, params.Content, 0)
	})
}

// -----------------------------------------------------------------------------
//  Fetch renderer
// -----------------------------------------------------------------------------

// fetchRenderer handles URL fetching with format-specific content display
type fetchRenderer struct {
	baseRenderer
}

// Render displays the fetched URL with format and timeout parameters
func (fr fetchRenderer) Render(v *toolCallCmp) string {
	var params tools.FetchParams
	if err := fr.unmarshalParams(v.call.Input, &params); err != nil {
		return fr.renderError(v, "Invalid fetch parameters")
	}
	args := newParamBuilder().
		addMain(params.URL).
		addKeyValue("format", params.Format).
		addKeyValue("timeout", formatTimeout(params.Timeout)).
		build()

	return fr.renderWithParams(v, "Fetch", args, func() string {
		file := fr.getSyntaxFileName(params.Format)
		return renderCodeContent(v, file, v.result.Content, 0)
	})
}

// getSyntaxFileName returns a representative filename used for syntax highlighting
func (fr fetchRenderer) getSyntaxFileName(format string) string {
	switch format {
	case "text":
		return "fetch.txt"
	case "html":
		return "fetch.html"
	default:
		return "fetch.md"
	}
}

// formatTimeout converts timeout seconds to duration string
func formatTimeout(timeout int) string {
	if timeout == 0 {
		return ""
	}
	return (time.Duration(timeout) * time.Second).String()
}

// -----------------------------------------------------------------------------
//  Download renderer
// -----------------------------------------------------------------------------

// downloadRenderer handles file downloading with URL and file path display
type downloadRenderer struct {
	baseRenderer
}

// Render displays the download URL and destination file path with timeout parameter
func (dr downloadRenderer) Render(v *toolCallCmp) string {
	var params tools.DownloadParams
	var args []string
	if err := dr.unmarshalParams(v.call.Input, &params); err == nil {
		args = newParamBuilder().
			addMain(params.URL).
			addKeyValue("file_path", fsext.PrettyPath(params.FilePath)).
			addKeyValue("timeout", formatTimeout(params.Timeout)).
			build()
	}

	return dr.renderWithParams(v, "Download", args, func() string {
		return renderPlainContent(v, v.result.Content)
	})
}

// -----------------------------------------------------------------------------
//  Glob renderer
// -----------------------------------------------------------------------------

// globRenderer handles file pattern matching with path filtering
type globRenderer struct {
	baseRenderer
}

// Render displays the glob pattern with optional path parameter
func (gr globRenderer) Render(v *toolCallCmp) string {
	var params tools.GlobParams
	if err := gr.unmarshalParams(v.call.Input, &params); err != nil {
		return gr.renderError(v, "Invalid glob parameters")
	}
	args := newParamBuilder().
		addMain(params.Pattern).
		addKeyValue("path", params.Path).
		build()

	return gr.renderWithParams(v, "Glob", args, func() string {
		return renderPlainContent(v, v.result.Content)
	})
}

// -----------------------------------------------------------------------------
//  Grep renderer
// -----------------------------------------------------------------------------

// grepRenderer handles content searching with pattern matching options
type grepRenderer struct {
	baseRenderer
}

// Render displays the search pattern with path, include, and literal text options
func (gr grepRenderer) Render(v *toolCallCmp) string {
	var params tools.GrepParams
	if err := gr.unmarshalParams(v.call.Input, &params); err != nil {
		return gr.renderError(v, "Invalid grep parameters")
	}
	args := newParamBuilder().
		addMain(params.Pattern).
		addKeyValue("path", params.Path).
		addKeyValue("include", params.Include).
		addFlag("literal", params.LiteralText).
		build()

	return gr.renderWithParams(v, "Grep", args, func() string {
		return renderPlainContent(v, v.result.Content)
	})
}

// -----------------------------------------------------------------------------
//  LS renderer
// -----------------------------------------------------------------------------

// lsRenderer handles directory listing with default path handling
type lsRenderer struct {
	baseRenderer
}

// Render displays the directory path, defaulting to current directory
func (lr lsRenderer) Render(v *toolCallCmp) string {
	var params tools.LSParams
	if err := lr.unmarshalParams(v.call.Input, &params); err != nil {
		return lr.renderError(v, "Invalid ls parameters")
	}
	path := params.Path
	if path == "" {
		path = "."
	}
	path = fsext.PrettyPath(path)

	args := newParamBuilder().addMain(path).build()

	return lr.renderWithParams(v, "List", args, func() string {
		return renderPlainContent(v, v.result.Content)
	})
}

// -----------------------------------------------------------------------------
//  Sourcegraph renderer
// -----------------------------------------------------------------------------

// sourcegraphRenderer handles code search with count and context options
type sourcegraphRenderer struct {
	baseRenderer
}

// Render displays the search query with optional count and context window parameters
func (sr sourcegraphRenderer) Render(v *toolCallCmp) string {
	var params tools.SourcegraphParams
	if err := sr.unmarshalParams(v.call.Input, &params); err != nil {
		return sr.renderError(v, "Invalid sourcegraph parameters")
	}
	args := newParamBuilder().
		addMain(params.Query).
		addKeyValue("count", formatNonZero(params.Count)).
		addKeyValue("context", formatNonZero(params.ContextWindow)).
		build()

	return sr.renderWithParams(v, "Sourcegraph", args, func() string {
		return renderPlainContent(v, v.result.Content)
	})
}

// -----------------------------------------------------------------------------
//  Diagnostics renderer
// -----------------------------------------------------------------------------

// diagnosticsRenderer handles project-wide diagnostic information
type diagnosticsRenderer struct {
	baseRenderer
}

// Render displays project diagnostics with plain content formatting
func (dr diagnosticsRenderer) Render(v *toolCallCmp) string {
	args := newParamBuilder().addMain("project").build()

	return dr.renderWithParams(v, "Diagnostics", args, func() string {
		return renderPlainContent(v, v.result.Content)
	})
}

// -----------------------------------------------------------------------------
//  Task renderer
// -----------------------------------------------------------------------------

// agentRenderer handles project-wide diagnostic information
type agentRenderer struct {
	baseRenderer
}

func RoundedEnumerator(children tree.Children, index int) string {
	if children.Length()-1 == index {
		return " ╰──"
	}
	return " ├──"
}

// Render displays agent task parameters and result content
func (tr agentRenderer) Render(v *toolCallCmp) string {
	t := styles.CurrentTheme()
	var params agent.AgentParams
	tr.unmarshalParams(v.call.Input, &params)

	prompt := params.Prompt
	prompt = strings.ReplaceAll(prompt, "\n", " ")

	header := tr.makeHeader(v, "Agent", v.textWidth())
	if res, done := earlyState(header, v); v.cancelled && done {
		return res
	}
	taskTag := t.S().Base.Padding(0, 1).MarginLeft(1).Background(t.BlueLight).Foreground(t.White).Render("Task")
	remainingWidth := v.textWidth() - lipgloss.Width(header) - lipgloss.Width(taskTag) - 2 // -2 for padding
	prompt = t.S().Muted.Width(remainingWidth).Render(prompt)
	header = lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		lipgloss.JoinHorizontal(
			lipgloss.Left,
			taskTag,
			" ",
			prompt,
		),
	)
	childTools := tree.Root(header)

	for _, call := range v.nestedToolCalls {
		childTools.Child(call.View())
	}
	parts := []string{
		childTools.Enumerator(RoundedEnumerator).String(),
	}

	if v.result.ToolCallID == "" {
		v.spinning = true
		parts = append(parts, "", v.anim.View())
	} else {
		v.spinning = false
	}

	header = lipgloss.JoinVertical(
		lipgloss.Left,
		parts...,
	)

	if v.result.ToolCallID == "" {
		return header
	}

	body := renderPlainContent(v, v.result.Content)
	return joinHeaderBody(header, body)
}

// renderParamList renders params, params[0] (params[1]=params[2] ....)
func renderParamList(nested bool, paramsWidth int, params ...string) string {
	t := styles.CurrentTheme()
	if len(params) == 0 {
		return ""
	}
	mainParam := params[0]
	if paramsWidth >= 0 && lipgloss.Width(mainParam) > paramsWidth {
		mainParam = ansi.Truncate(mainParam, paramsWidth, "…")
	}

	if len(params) == 1 {
		if nested {
			return t.S().Muted.Render(mainParam)
		}
		return t.S().Subtle.Render(mainParam)
	}
	otherParams := params[1:]
	// create pairs of key/value
	// if odd number of params, the last one is a key without value
	if len(otherParams)%2 != 0 {
		otherParams = append(otherParams, "")
	}
	parts := make([]string, 0, len(otherParams)/2)
	for i := 0; i < len(otherParams); i += 2 {
		key := otherParams[i]
		value := otherParams[i+1]
		if value == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", key, value))
	}

	partsRendered := strings.Join(parts, ", ")
	remainingWidth := paramsWidth - lipgloss.Width(partsRendered) - 3 // count for " ()"
	if remainingWidth < 30 {
		if nested {
			return t.S().Muted.Render(mainParam)
		}
		// No space for the params, just show the main
		return t.S().Subtle.Render(mainParam)
	}

	if len(parts) > 0 {
		mainParam = fmt.Sprintf("%s (%s)", mainParam, strings.Join(parts, ", "))
	}

	if nested {
		return t.S().Muted.Render(ansi.Truncate(mainParam, paramsWidth, "…"))
	}
	return t.S().Subtle.Render(ansi.Truncate(mainParam, paramsWidth, "…"))
}

// earlyState returns immediately‑rendered error/cancelled/ongoing states.
func earlyState(header string, v *toolCallCmp) (string, bool) {
	t := styles.CurrentTheme()
	message := ""
	switch {
	case v.result.IsError:
		message = v.renderToolError()
	case v.cancelled:
		message = t.S().Base.Foreground(t.FgSubtle).Render("Canceled.")
	case v.result.ToolCallID == "":
		if v.permissionRequested && !v.permissionGranted {
			message = t.S().Base.Foreground(t.FgSubtle).Render("Requesting for permission...")
		} else {
			message = t.S().Base.Foreground(t.FgSubtle).Render("Waiting for tool response...")
		}
	default:
		return "", false
	}

	message = t.S().Base.PaddingLeft(2).Render(message)
	return lipgloss.JoinVertical(lipgloss.Left, header, "", message), true
}

func joinHeaderBody(header, body string) string {
	t := styles.CurrentTheme()
	if body == "" {
		return header
	}
	body = t.S().Base.PaddingLeft(2).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body)
}

// truncateToLines returns the string truncated to at most maxLines (by '\n') and
// the total number of lines in the original string.
func truncateToLines(s string, maxLines int) (string, int) {
	if s == "" {
		return "", 0
	}
	if maxLines <= 0 {
		return "", 0
	}
	lines := 1
	cut := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if lines == maxLines && cut == -1 {
				cut = i
			}
			lines++
		}
	}
	if cut == -1 {
		return s, lines
	}
	return s[:cut], lines
}

func renderPlainContent(v *toolCallCmp, content string) string {
	t := styles.CurrentTheme()
	keyH := sha256.Sum256([]byte(content))
	key := fmt.Sprintf("plain:%d:%s:%t:%s", v.textWidth(), t.Name, t.IsDark, hex.EncodeToString(keyH[:8]))
	if v.cachedPlainKey == key && v.cachedPlain != "" {
		return v.cachedPlain
	}

	c := strings.ReplaceAll(content, "\r\n", "\n")
	c = strings.ReplaceAll(c, "\t", "    ")
	c = strings.TrimSpace(c)

	trunc, totalLines := truncateToLines(c, responseContextHeight)
	lines := strings.Split(trunc, "\n")

	width := v.textWidth() - 2
	out := make([]string, 0, len(lines)+1)
	for _, ln := range lines {
		ln = ansiext.Escape(ln)
		ln = " " + ln
		if len(ln) > width {
			ln = v.fit(ln, width)
		}
		out = append(out, t.S().Muted.
			Width(width).
			Background(t.BgBaseLighter).
			Render(ln))
	}

	if more := totalLines - responseContextHeight; more > 0 {
		out = append(out, t.S().Muted.
			Background(t.BgBaseLighter).
			Width(width).
			Render(fmt.Sprintf("… (%d lines)", more)))
	}
	res := strings.Join(out, "\n")
	v.cachedPlainKey = key
	v.cachedPlain = res
	return res
}

// moved to internal/format/lineno.Digits

func renderCodeContent(v *toolCallCmp, path, content string, offset int) string {
	t := styles.CurrentTheme()
	r, g, b, _ := t.BgBase.RGBA()
	keyH := sha256.Sum256([]byte(path + "|" + content))
	key := fmt.Sprintf("code:%d:%s:%t:%02x%02x%02x:%d:%s", v.textWidth(), t.Name, t.IsDark, uint8(r>>8), uint8(g>>8), uint8(b>>8), offset, hex.EncodeToString(keyH[:8]))
	if v.cachedCodeKey == key && v.cachedCode != "" {
		return v.cachedCode
	}

	c := strings.ReplaceAll(content, "\r\n", "\n")
	c = strings.ReplaceAll(c, "\t", "    ")

	trunc, totalLines := truncateToLines(c, responseContextHeight)
	lines := strings.Split(trunc, "\n")
	for i, ln := range lines {
		lines[i] = ansiext.Escape(ln)
	}

	bg := t.BgBase
	highlighted, _ := highlight.SyntaxHighlight(strings.Join(lines, "\n"), path, bg)
	lines = strings.Split(highlighted, "\n")

	if more := totalLines - responseContextHeight; more > 0 {
		lines = append(lines, t.S().Muted.
			Background(bg).
			Render(fmt.Sprintf(" …(%d lines)", more)))
	}

	maxLineNumber := len(lines) + offset
	maxDigits := lineno.Digits(maxLineNumber)
	numFmt := fmt.Sprintf("%%%dd", maxDigits)
	const numPR, numPL, codePR, codePL = 1, 1, 1, 2
	w := v.textWidth() - maxDigits - numPL - numPR - 2
	for i, ln := range lines {
		num := t.S().Base.
			Foreground(t.FgMuted).
			Background(t.BgBase).
			PaddingRight(1).
			PaddingLeft(1).
			Render(fmt.Sprintf(numFmt, i+1+offset))
		lines[i] = lipgloss.JoinHorizontal(lipgloss.Left,
			num,
			t.S().Base.
				Width(w).
				Background(bg).
				PaddingRight(1).
				PaddingLeft(2).
				Render(v.fit(ln, w-codePL-codePR)),
		)
	}

	res := lipgloss.JoinVertical(lipgloss.Left, lines...)
	v.cachedCodeKey = key
	v.cachedCode = res
	return res
}

func (v *toolCallCmp) renderToolError() string {
	t := styles.CurrentTheme()
	err := strings.ReplaceAll(v.result.Content, "\n", " ")
	errTag := t.S().Base.Padding(0, 1).Background(t.Red).Foreground(t.White).Render("ERROR")
	err = fmt.Sprintf("%s %s", errTag, t.S().Base.Foreground(t.FgHalfMuted).Render(v.fit(err, v.textWidth()-2-lipgloss.Width(errTag))))
	return err
}

func prettifyToolName(name string) string {
	switch name {
	case agent.AgentToolName:
		return "Agent"
	case tools.BashToolName:
		return "Bash"
	case tools.DownloadToolName:
		return "Download"
	case tools.EditToolName:
		return "Edit"
	case tools.MultiEditToolName:
		return "Multi-Edit"
	case tools.FetchToolName:
		return "Fetch"
	case tools.GlobToolName:
		return "Glob"
	case tools.GrepToolName:
		return "Grep"
	case tools.LSToolName:
		return "List"
	case tools.SourcegraphToolName:
		return "Sourcegraph"
	case tools.ViewToolName:
		return "View"
	case tools.WriteToolName:
		return "Write"
	default:
		return name
	}
}
