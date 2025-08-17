package list

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/tui/styles"
)

type benchItem struct {
	id    string
	view  string
	w, h  int
	focus bool
}

func (b benchItem) Init() tea.Cmd                          { return nil }
func (b benchItem) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return b, nil }
func (b benchItem) View() string                            { return b.view }
func (b benchItem) ID() string                              { return b.id }
func (b benchItem) GetSize() (int, int)                     { return b.w, b.h }
func (b benchItem) SetSize(w, h int) tea.Cmd                { b.w, b.h = w, h; return nil }
func (b benchItem) Focus() tea.Cmd                          { b.focus = true; return nil }
func (b benchItem) Blur() tea.Cmd                           { b.focus = false; return nil }
func (b benchItem) IsFocused() bool                         { return b.focus }

func makeLongViewLines(n int, lineLen int) string {
	line := strings.Repeat("A", lineLen)
	return strings.Repeat(line+"\n", n)
}

func BenchmarkListView_LongItems(b *testing.B) {
	// Build many items with long lines to stress UV rendering & selection overlay
	items := make([]benchItem, 0, 200)
	for i := 0; i < 200; i++ {
		items = append(items, benchItem{id: fmt.Sprintf("id-%d", i), view: makeLongViewLines(100, 1000)})
	}
	l := New(items, WithSize(120, 40), WithGap(1), WithDirectionBackward(), WithEnableMouse())
	_ = l.Init()
	_ = l.SetSize(120, 40)
	// warm up
	_ = l.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = l.View()
	}
	_ = styles.CurrentTheme() // ensure theme is initialized
}
