package diffview_test

import (
	"os"
	"testing"

	"github.com/charmbracelet/crush/internal/tui/exp/diffview"
	"github.com/charmbracelet/x/exp/golden"
)

func TestBasicOps_WordDiff(t *testing.T) {
	if os.Getenv("CRUSH_TEST_ENABLE_WORDDIFF") == "" {
		t.Skip("set CRUSH_TEST_ENABLE_WORDDIFF=1 to run external word-diff goldens")
	}
	if !isGitAvailable() {
		t.Skip("git not available for word-diff tests")
	}
	cases := []struct {
		name   string
		before string
		after  string
	}{
		{
			name:   "EditLineMiddle",
			before: "one\ntwo\nthree\n",
			after:  "one\nTWO EDITED\nthree\n",
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
					// enable external inline provider
					dv = dv.InlineProvider(diffview.NewExternalInlineProvider())
					output := dv.String()
					golden.RequireEqual(t, []byte(output))
				})
			}
		})
	}
}

func isGitAvailable() bool {
	_, ok := os.LookupEnv("PATH")
	return ok
}
