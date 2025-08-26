package diffview_test

import (
	"os"
	"testing"

	"github.com/charmbracelet/crush/internal/tui/exp/diffview"
	"github.com/charmbracelet/x/exp/golden"
)

// Stress word-diff inline rendering on many small word edits in a single line.
// Before:  many many words a a aa a a a a a a many many words
// After:   many many words b b b bb b b b b bb b many many words
func TestWordDiff_ManySmallEdits_Unified(t *testing.T) {
	if os.Getenv("CRUSH_TEST_ENABLE_WORDDIFF") == "" {
		t.Skip("set CRUSH_TEST_ENABLE_WORDDIFF=1 to run external word-diff goldens")
	}

	before := "many many words a a aa a a a a a a many many words\n"
	after := "many many words b b b bb b b b b bb b many many words\n"

	dv := diffview.New().
		Before("t.txt", before).
		After("t.txt", after).
		Style(diffview.DefaultLightStyle()).
		ChromaStyle(nil)

	dv.Unified()
	dv = dv.InlineProvider(diffview.NewExternalInlineProvider())

	output := dv.String()
	golden.RequireEqual(t, []byte(output))
}
