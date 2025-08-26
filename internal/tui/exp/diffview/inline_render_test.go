package diffview

import (
	"testing"
)

func TestAssembleUnified_SimpleMapping(t *testing.T) {
	seq := []InlinePiece{
		{Kind: InlinePieceEqual, Text: "foo "},
		{Kind: InlinePieceDelete, Text: "bar"},
		{Kind: InlinePieceInsert, Text: "baz"},
		{Kind: InlinePieceEqual, Text: " qux"},
	}
	// Use DefaultDarkStyle for stable palette (we don't assert colors, just that output is non-empty)
	dv := &DiffView{style: DefaultDarkStyle()}
	content := assembleUnified(seq, dv.style.EqualLine, dv.style.DeleteLine, dv.style.InsertLine)
	if content == "" {
		t.Fatalf("assembleUnified produced empty content")
	}
}
