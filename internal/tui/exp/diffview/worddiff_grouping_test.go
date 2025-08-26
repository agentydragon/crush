package diffview_test

import (
	"testing"

	"github.com/charmbracelet/crush/internal/tui/exp/diffview"
	"github.com/charmbracelet/x/exp/golden"
)

// This test ensures unified renderer groups multi-line change runs: all deletes first,
// then all inserts, while still applying inline word-level highlighting for single-line
// replacements within runs.
func TestWordDiff_GroupedRun_Unified(t *testing.T) {
	t.Parallel()

	before := "alpha\nBravo Xray\ncharlie\n"
	after := "alpha\nBravo Zulu\nINSERTED A\nINSERTED B\ncharlie\n"

	dv := diffview.New().
		Before("a.txt", before).
		After("a.txt", after).
		Style(diffview.DefaultLightStyle()).
		ChromaStyle(nil) // stable output

	// Force unified view and enable external word diff provider
	dv.Unified()
	dv = dv.InlineProvider(diffview.NewExternalInlineProvider())

	output := dv.String()
	golden.RequireEqual(t, []byte(output))
}
