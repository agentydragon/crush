package diffview

import (
	"os/exec"
	"runtime"
	"testing"
)

// helper to check if git is available
func hasGit(t *testing.T) bool {
	t.Helper()
	cmd := exec.Command("git", "--version")
	return cmd.Run() == nil
}

func TestProvider_CombinedSequences(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("word-diff porcelain may differ on windows env; skip here")
	}
	if !hasGit(t) {
		t.Skip("git not available in PATH; skipping provider tests")
	}

	p := NewExternalInlineProvider()

	t.Run("replace middle word", func(t *testing.T) {
		before := "the quick brown fox\n"
		after := "the quick red fox\n"
		seq, ok := p(before, after)
		if !ok {
			t.Fatalf("provider returned ok=false")
		}
		kinds := make([]InlinePieceKind, 0, len(seq))
		for _, s := range seq {
			if s.Text == "" { // no empties
				t.Fatalf("empty piece text in sequence: %+v", seq)
			}
			kinds = append(kinds, s.Kind)
		}
		// Expect Equal, Delete, Insert, Equal in order
		expected := []InlinePieceKind{InlinePieceEqual, InlinePieceDelete, InlinePieceInsert, InlinePieceEqual}
		if len(kinds) != len(expected) {
			t.Fatalf("unexpected kinds len: got %d want %d (%v)", len(kinds), len(expected), kinds)
		}
		for i := range expected {
			if kinds[i] != expected[i] {
				t.Fatalf("kinds[%d]=%v want %v (full=%v)", i, kinds[i], expected[i], kinds)
			}
		}
	})

	t.Run("insert only", func(t *testing.T) {
		before := "the quick fox\n"
		after := "the very quick fox\n"
		seq, ok := p(before, after)
		if !ok {
			t.Fatalf("provider returned ok=false")
		}
		kinds := make([]InlinePieceKind, 0, len(seq))
		for _, s := range seq {
			kinds = append(kinds, s.Kind)
		}
		expected := []InlinePieceKind{InlinePieceEqual, InlinePieceInsert, InlinePieceEqual}
		if len(kinds) != len(expected) {
			t.Fatalf("unexpected kinds len: got %d want %d (%v)", len(kinds), len(expected), kinds)
		}
		for i := range expected {
			if kinds[i] != expected[i] {
				t.Fatalf("kinds[%d]=%v want %v (full=%v)", i, kinds[i], expected[i], kinds)
			}
		}
	})

	t.Run("delete only", func(t *testing.T) {
		before := "jumped over the lazy dog\n"
		after := "jumped over the dog\n"
		seq, ok := p(before, after)
		if !ok {
			t.Fatalf("provider returned ok=false")
		}
		kinds := make([]InlinePieceKind, 0, len(seq))
		for _, s := range seq {
			kinds = append(kinds, s.Kind)
		}
		expected := []InlinePieceKind{InlinePieceEqual, InlinePieceDelete, InlinePieceEqual}
		if len(kinds) != len(expected) {
			t.Fatalf("unexpected kinds len: got %d want %d (%v)", len(kinds), len(expected), kinds)
		}
		for i := range expected {
			if kinds[i] != expected[i] {
				t.Fatalf("kinds[%d]=%v want %v (full=%v)", i, kinds[i], expected[i], kinds)
			}
		}
	})
}
