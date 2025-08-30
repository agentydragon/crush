package diffview_test

import (
	"os"
	"testing"

	"github.com/charmbracelet/crush/internal/tui/exp/diffview"
	"github.com/charmbracelet/x/exp/golden"
)

// This test duplicates the prod scenario from SID 9f706440-0a4f-4ede-a55d-f8c34f3818e2:
// a single docstring line is added; unchanged lines must render equal in Split.
func TestCLIDocstring_AddOneLine_Split(t *testing.T) {
	t.Parallel()

	before, err := os.ReadFile("testdata/cli_docstring_before.txt")
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile("testdata/cli_docstring_after.txt")
	if err != nil {
		t.Fatal(err)
	}

	dv := diffview.New().
		Before("cli.py", string(before)).
		After("cli.py", string(after)).
		Split().
		Style(diffview.DefaultLightStyle()).
		ChromaStyle(nil)

	output := dv.String()
	golden.RequireEqual(t, []byte(output))
}
