package compact

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/v2/key"
	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/charmbracelet/lipgloss/v2"

	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/tui/components/anim"
	"github.com/charmbracelet/crush/internal/tui/components/core"
	"github.com/charmbracelet/crush/internal/tui/components/dialogs"
	"github.com/charmbracelet/crush/internal/tui/styles"
	"github.com/charmbracelet/crush/internal/tui/util"
)

const CompactDialogID dialogs.DialogID = "compact"

// CompactDialog interface for the session compact dialog
type CompactDialog interface {
	dialogs.DialogModel
}

type compactDialogCmp struct {
	wWidth, wHeight int
	width, height   int
	selected        int
	keyMap          KeyMap
	sessionID       string
	state           compactState
	progress        string
	spinner         *anim.Anim
	agent           agent.Service
	noAsk           bool
}

type compactState int

const (
	stateConfirm compactState = iota
	stateCompacting
	stateError
)

// NewCompactDialogCmp creates a new session compact dialog
func NewCompactDialogCmp(agent agent.Service, sessionID string, noAsk bool) CompactDialog {
	return &compactDialogCmp{
		sessionID: sessionID,
		keyMap:    DefaultKeyMap(),
		state:     stateConfirm,
		selected:  0,
		spinner:   anim.New(anim.Settings{Size: 10, Label: ""}),
		agent:     agent,
		noAsk:     noAsk,
	}
}

func (c *compactDialogCmp) Init() tea.Cmd {
	if c.noAsk {
		c.spinner.SetLabel("Starting summarization...")
		return tea.Batch(c.spinner.Init(), c.startCompaction())
	}
	return c.spinner.Init()
}

func (c *compactDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if mm, cmd := c.spinner.Update(msg); cmd != nil {
		c.spinner = mm.(*anim.Anim)
		cmds = append(cmds, cmd)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.wWidth = msg.Width
		c.wHeight = msg.Height
		cmd := c.SetSize()
		return c, tea.Batch(append(cmds, cmd)...)

	case tea.KeyPressMsg:
		switch c.state {
		case stateConfirm:
			switch {
			case key.Matches(msg, c.keyMap.ChangeSelection):
				c.selected = (c.selected + 1) % 2
				return c, tea.Batch(cmds...)
			case key.Matches(msg, c.keyMap.Select):
				if c.selected == 0 {
					c.spinner.SetLabel("Starting summarization...")
					return c, tea.Batch(append(cmds, c.startCompaction())...)
				}
				return c, util.CmdHandler(dialogs.CloseDialogMsg{})
			case key.Matches(msg, c.keyMap.Y):
				c.spinner.SetLabel("Starting summarization...")
				return c, tea.Batch(append(cmds, c.startCompaction())...)
			case key.Matches(msg, c.keyMap.N):
				return c, util.CmdHandler(dialogs.CloseDialogMsg{})
			case key.Matches(msg, c.keyMap.Close):
				return c, util.CmdHandler(dialogs.CloseDialogMsg{})
			}
		case stateCompacting:
			switch {
			case key.Matches(msg, c.keyMap.Close):
				return c, util.CmdHandler(dialogs.CloseDialogMsg{})
			}
		case stateError:
			switch {
			case key.Matches(msg, c.keyMap.Select):
				return c, util.CmdHandler(dialogs.CloseDialogMsg{})
			case key.Matches(msg, c.keyMap.Close):
				return c, util.CmdHandler(dialogs.CloseDialogMsg{})
			}
		}

	case agent.AgentEvent:
		if msg.SessionID != "" && msg.SessionID != c.sessionID {
			return c, tea.Batch(cmds...)
		}
		if msg.Type == agent.AgentEventTypeSummarize {
			if msg.Error != nil {
				c.state = stateError
				c.progress = "Error: " + msg.Error.Error()
				c.spinner.SetLabel(c.progress)
			} else if msg.Done {
				return c, util.CmdHandler(dialogs.CloseDialogMsg{})
			} else {
				label := msg.Progress
				if msg.TotalBytes > 0 || msg.TotalRunes > 0 {
					label = label + " (" + fmt.Sprintf("%d bytes", msg.TotalBytes) + ")"
				}
				c.progress = label
				c.spinner.SetLabel(label)
			}
			return c, tea.Batch(cmds...)
		}
		if msg.Type == agent.AgentEventTypeError && c.state == stateCompacting {
			if msg.Error != nil {
				c.state = stateError
				c.progress = "Error: " + msg.Error.Error()
				c.spinner.SetLabel(c.progress)
			}
		}
		return c, tea.Batch(cmds...)
	}

	return c, tea.Batch(cmds...)
}

func (c *compactDialogCmp) startCompaction() tea.Cmd {
	c.state = stateCompacting
	c.progress = "Starting summarization..."
	c.spinner.SetLabel(c.progress)
	return func() tea.Msg {
		err := c.agent.Summarize(context.Background(), c.sessionID)
		if err != nil {
			c.state = stateError
			c.progress = "Error: " + err.Error()
		}
		return nil
	}
}

func (c *compactDialogCmp) renderButtons() string {
	t := styles.CurrentTheme()
	baseStyle := t.S().Base

	buttons := []core.ButtonOpts{
		{
			Text:           "Yes",
			UnderlineIndex: 0, // "Y"
			Selected:       c.selected == 0,
		},
		{
			Text:           "No",
			UnderlineIndex: 0, // "N"
			Selected:       c.selected == 1,
		},
	}

	content := core.SelectableButtons(buttons, "  ")

	return baseStyle.AlignHorizontal(lipgloss.Right).Width(c.width - 4).Render(content)
}

func (c *compactDialogCmp) renderContent() string {
	t := styles.CurrentTheme()
	baseStyle := t.S().Base

	switch c.state {
	case stateConfirm:
		explanation := t.S().Text.
			Width(c.width - 4).
			Render("This will summarize the current session and reset the context. The conversation history will be condensed into a summary to free up context space while preserving important information.")

		question := t.S().Text.
			Width(c.width - 4).
			Render("Do you want to continue?")

		return baseStyle.Render(lipgloss.JoinVertical(
			lipgloss.Left,
			explanation,
			"",
			question,
		))
	case stateCompacting:
		preview := ""
		if c.progress != "" {
			preview = c.progress
		}
		return baseStyle.Render(lipgloss.JoinVertical(
			lipgloss.Left,
			c.spinner.View(),
			"",
			preview,
		))
	case stateError:
		return baseStyle.Render(lipgloss.JoinVertical(
			lipgloss.Left,
			c.progress,
			"",
			"Press Enter to close",
		))
	}
	return ""
}

func (c *compactDialogCmp) render() string {
	t := styles.CurrentTheme()
	baseStyle := t.S().Base

	var title string
	switch c.state {
	case stateConfirm:
		title = "Compact Session"
	case stateCompacting:
		title = "Compacting Session"
	case stateError:
		title = "Compact Failed"
	}

	titleView := core.Title(title, c.width-4)
	content := c.renderContent()

	var dialogContent string
	if c.state == stateConfirm {
		buttons := c.renderButtons()
		dialogContent = lipgloss.JoinVertical(
			lipgloss.Top,
			titleView,
			"",
			content,
			"",
			buttons,
			"",
		)
	} else {
		dialogContent = lipgloss.JoinVertical(
			lipgloss.Top,
			titleView,
			"",
			content,
			"",
		)
	}

	return baseStyle.
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus).
		Width(c.width).
		Render(dialogContent)
}

func (c *compactDialogCmp) View() string {
	return c.render()
}

// SetSize sets the size of the component.
func (c *compactDialogCmp) SetSize() tea.Cmd {
	c.width = min(90, c.wWidth)
	c.height = min(15, c.wHeight)
	return nil
}

func (c *compactDialogCmp) Position() (int, int) {
	row := (c.wHeight / 2) - (c.height / 2)
	col := (c.wWidth / 2) - (c.width / 2)
	return row, col
}

// ID implements CompactDialog.
func (c *compactDialogCmp) ID() dialogs.DialogID {
	return CompactDialogID
}
