package diffview_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/tui/exp/diffview"
	"github.com/charmbracelet/x/ansi"
)

func TestIndentOnlyChangesUnified(t *testing.T) {
	before := "func x() {\n  a()\n}\n"
	after := "func x() {\n    a()\n}\n"

	dv := diffview.New().
		Before("main.go", before).
		After("main.go", after).
		IgnoreIndentChanges(true).
		Unified().
		LineNumbers(false)

	out := dv.String()
	plain := ansi.Strip(out)

	for _, line := range strings.Split(strings.TrimSuffix(plain, "\n"), "\n") {
		if strings.HasPrefix(line, "  @@") {
			continue
		}
		if strings.HasPrefix(line, "+ ") || strings.HasPrefix(line, "- ") {
			t.Fatalf("expected no insert/delete markers for indent-only changes, got line: %q", line)
		}
	}
}

func TestIndentOnlyChangesSplit(t *testing.T) {
	before := "func x() {\n  a()\n}\n"
	after := "func x() {\n    a()\n}\n"

	dv := diffview.New().
		Before("main.go", before).
		After("main.go", after).
		IgnoreIndentChanges(true).
		Split().
		LineNumbers(false)

	out := dv.String()
	plain := ansi.Strip(out)

	if strings.Contains(plain, "+ ") || strings.Contains(plain, "- ") {
		t.Fatalf("expected no insert/delete markers in split for indent-only changes, got output:\n%s", plain)
	}
}
