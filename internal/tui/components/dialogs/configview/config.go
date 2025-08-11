package configview

import (
	"github.com/charmbracelet/bubbles/v2/help"
	"github.com/charmbracelet/bubbles/v2/key"
	"github.com/charmbracelet/bubbles/v2/viewport"
	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/tui/components/core"
	"github.com/charmbracelet/crush/internal/tui/components/dialogs"
	"github.com/charmbracelet/crush/internal/tui/styles"
	"github.com/charmbracelet/crush/internal/tui/util"
	"github.com/charmbracelet/lipgloss/v2"
)

const ConfigDialogID dialogs.DialogID = "config_view"

type KeyMap struct {
	Close           key.Binding
	ToggleRedaction key.Binding
}

func DefaultKeyMap() KeyMap {
	return KeyMap{
		Close:           key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		ToggleRedaction: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "toggle redaction")),
	}
}

func (k KeyMap) KeyBindings() []key.Binding { return []key.Binding{k.Close, k.ToggleRedaction} }
func (k KeyMap) FullHelp() [][]key.Binding  { return [][]key.Binding{k.KeyBindings()} }
func (k KeyMap) ShortHelp() []key.Binding   { return k.KeyBindings() }

type ConfigDialog interface{ dialogs.DialogModel }

type cmp struct {
	wWidth, wHeight int
	width, height   int
	vp              viewport.Model
	help            help.Model
	keyMap          KeyMap
	redacted        bool
}

func New(redacted bool) ConfigDialog {
	vp := viewport.New()
	h := help.New()
	h.Styles = styles.CurrentTheme().S().Help
	return &cmp{vp: vp, help: h, keyMap: DefaultKeyMap(), redacted: redacted}
}

func (c *cmp) Init() tea.Cmd { return c.vp.Init() }

func (c *cmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.wWidth, c.wHeight = msg.Width, msg.Height
		return c, c.SetSize()
	case tea.KeyPressMsg:
		if key.Matches(msg, c.keyMap.Close) {
			return c, util.CmdHandler(dialogs.CloseDialogMsg{})
		}
		if key.Matches(msg, c.keyMap.ToggleRedaction) {
			c.redacted = !c.redacted
			bts, err := config.Get().EffectiveJSON(c.redacted)
			if err != nil {
				return c, util.ReportError(err)
			}
			t := styles.CurrentTheme()
			c.vp.SetContent(t.S().Base.Background(t.BgSubtle).Padding(1, 2).Render(string(bts)))
			return c, nil
		}
	}
	v, cmd := c.vp.Update(msg)
	c.vp = v
	return c, cmd
}

func (c *cmp) View() string {
	t := styles.CurrentTheme()
	title := core.Title("Effective Config", c.width-4)
	return t.S().Base.
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus).
		Width(c.width).
		Render(lipgloss.JoinVertical(
			lipgloss.Left,
			title,
			"",
			c.vp.View(),
			"",
			c.help.View(c.keyMap),
		))
}

func (c *cmp) Position() (int, int) {
	row := c.wHeight / 2
	row -= c.height / 2
	col := c.wWidth / 2
	col -= c.width / 2
	return row, col
}

func (c *cmp) ID() dialogs.DialogID { return ConfigDialogID }

func (c *cmp) SetSize() tea.Cmd {
	c.width = min(int(float64(c.wWidth)*0.9), 180)
	c.height = int(float64(c.wHeight) * 0.85)
	c.vp.SetWidth(c.width - 4)
	c.vp.SetHeight(c.height - 7)
	bts, err := config.Get().EffectiveJSON(true)
	if !c.redacted {
		bts, err = config.Get().EffectiveJSON(false)
	}
	if err != nil {
		return util.ReportError(err)
	}
	t := styles.CurrentTheme()
	c.vp.SetContent(t.S().Base.Background(t.BgSubtle).Padding(1, 2).Render(string(bts)))
	return nil
}
