package diffview_test

import (
	"testing"

	"github.com/charmbracelet/crush/internal/tui/exp/diffview"
	"github.com/charmbracelet/x/exp/golden"
)

// TestBasicOps_Boundaries adds edge cases around file boundaries and trailing
// newline handling. It captures the current rendered characters of the diff
// component for both Split and Unified layouts.
func TestBasicOps_Boundaries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		before string
		after  string
	}{
		// Boundary operations
		{
			name:   "AddLineStart",
			before: "one\ntwo\n",
			after:  "ADDED\none\ntwo\n",
		},
		{
			name:   "AddLineEnd",
			before: "one\ntwo\n",
			after:  "one\ntwo\nADDED\n",
		},
		{
			name:   "DeleteLineStart",
			before: "one\ntwo\n",
			after:  "two\n",
		},
		{
			name:   "DeleteLineEnd",
			before: "one\ntwo\n",
			after:  "one\n",
		},

		// Trailing newline differences
		{
			name:   "NoTrailingNewlineBefore",
			before: "one\ntwo",
			after:  "one\ntwo\n",
		},
		{
			name:   "NoTrailingNewlineAfter",
			before: "one\ntwo\n",
			after:  "one\ntwo",
		},
	}

	for layoutName, layoutFunc := range LayoutFuncs {
		layoutName, layoutFunc := layoutName, layoutFunc
		t.Run(layoutName, func(t *testing.T) {
			t.Parallel()
			for _, c := range cases {
				c := c
				t.Run(c.name, func(t *testing.T) {
					t.Parallel()

					dv := diffview.New().
						Before("text.txt", c.before).
						After("text.txt", c.after).
						Style(diffview.DefaultLightStyle()).
						ChromaStyle(nil) // disable syntax highlighting for stability
					dv = layoutFunc(dv)

					output := dv.String()
					golden.RequireEqual(t, []byte(output))
				})
			}
		})
	}
}
