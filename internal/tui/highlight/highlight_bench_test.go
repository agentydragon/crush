package highlight

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/tui/styles"
)

func BenchmarkSyntaxHighlight_LongSingleLine(b *testing.B) {
	bg := styles.CurrentTheme().BgBase
	line := strings.Repeat("a", 50000)
	src := line + "\n" + line + "\n" + line
	// Warmup to populate caches
	_, _ = SyntaxHighlight(src, "x.go", bg)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = SyntaxHighlight(src, "x.go", bg)
	}
}
