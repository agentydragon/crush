package styles

import (
	"fmt"
	"sync"

	"github.com/charmbracelet/glamour/v2"
)

// Helper functions for style pointers
func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }
func uintPtr(u uint) *uint       { return &u }

var (
	markdownCacheMu   sync.Mutex
	markdownRenderers = map[string]*glamour.TermRenderer{}
)

func markdownKey(themeName string, isDark bool, width int) string {
	return fmt.Sprintf("%s:%t:%d", themeName, isDark, width)
}

// returns a glamour TermRenderer configured with the current theme
func GetMarkdownRenderer(width int) *glamour.TermRenderer {
	t := CurrentTheme()
	key := markdownKey(t.Name, t.IsDark, width)
	markdownCacheMu.Lock()
	if r, ok := markdownRenderers[key]; ok && r != nil {
		markdownCacheMu.Unlock()
		return r
	}
	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(t.S().Markdown),
		glamour.WithWordWrap(width),
	)
	markdownRenderers[key] = r
	// Cap cache size to avoid unbounded growth; large to prioritize speed.
	if len(markdownRenderers) > 1000 {
		// naive eviction: clear map except current key
		markdownRenderers = map[string]*glamour.TermRenderer{key: r}
	}
	markdownCacheMu.Unlock()
	return r
}
