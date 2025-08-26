package diffview_test

import (
	"os"
	"testing"

	"github.com/charmbracelet/crush/internal/tui/exp/diffview"
	"github.com/charmbracelet/x/exp/golden"
)

// This test exercises partial inline (word) diffs within a single logical line,
// ensuring the renderer collapses a delete+insert pair into one line with inline spans.
func TestWordDiff_PartialInline(t *testing.T) {
	if os.Getenv("CRUSH_TEST_ENABLE_WORDDIFF") == "" {
		t.Skip("set CRUSH_TEST_ENABLE_WORDDIFF=1 to run external word-diff goldens")
	}

	cases := []struct {
		name   string
		before string
		after  string
	}{
		{
			name:   "ReplaceMiddleWord",
			before: "the quick brown fox\n",
			after:  "the quick red fox\n",
		},
		{
			name:   "InsertMiddleWord",
			before: "the quick fox\n",
			after:  "the very quick fox\n",
		},
		{
			name:   "DeleteMiddleWord",
			before: "jumped over the lazy dog\n",
			after:  "jumped over the dog\n",
		},
	}

	for layoutName, layoutFunc := range LayoutFuncs {
		layoutName, layoutFunc := layoutName, layoutFunc
		t.Run(layoutName, func(t *testing.T) {
			for _, c := range cases {
				c := c
				t.Run(c.name, func(t *testing.T) {
					dv := diffview.New().
						Before("text.txt", c.before).
						After("text.txt", c.after).
						Style(diffview.DefaultLightStyle()).
						ChromaStyle(nil)
					dv = layoutFunc(dv)
					dv = dv.InlineProvider(diffview.NewExternalInlineProvider())
					output := dv.String()
					golden.RequireEqual(t, []byte(output))
				})
			}
		})
	}
}
