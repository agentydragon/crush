package diffview_test

import (
	"testing"

	"github.com/charmbracelet/crush/internal/tui/exp/diffview"
	"github.com/charmbracelet/x/exp/golden"
)

func TestBasicOps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		before string
		after  string
	}{
		{
			name:   "AddLineMiddle",
			before: "one\ntwo\nthree\n",
			after:  "one\ntwo\nADDED\nthree\n",
		},
		{
			name:   "DeleteLineMiddle",
			before: "one\ntwo\nthree\n",
			after:  "one\nthree\n",
		},
		{
			name:   "EditLineMiddle",
			before: "one\ntwo\nthree\n",
			after:  "one\nTWO EDITED\nthree\n",
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
