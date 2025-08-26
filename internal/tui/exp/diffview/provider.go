package diffview

import (
	"github.com/charmbracelet/crush/internal/diff"
)

// NewExternalInlineProvider returns an InlineProvider that shells out to git
// per line pair using --word-diff=porcelain and maps to inline pieces for DV.
func NewExternalInlineProvider() func(beforeLine, afterLine string) (combined []InlinePiece, ok bool) {
	return func(beforeLine, afterLine string) (combined []InlinePiece, ok bool) {
		pieces, ok := diff.PerLineWordPieces(beforeLine, afterLine)
		if !ok || len(pieces) == 0 {
			// Fallback: simple inline by common prefix/suffix
			b := beforeLine
			a := afterLine
			// normalize
			if len(b) > 0 && b[len(b)-1] == '\n' {
				b = b[:len(b)-1]
			}
			if len(a) > 0 && a[len(a)-1] == '\n' {
				a = a[:len(a)-1]
			}
			if b == a {
				return []InlinePiece{{Kind: InlinePieceEqual, Text: b}}, true
			}
			// prefix
			pi := 0
			for pi < len(b) && pi < len(a) && b[pi] == a[pi] {
				pi++
			}
			// suffix
			si := 0
			for si < len(b)-pi && si < len(a)-pi && b[len(b)-1-si] == a[len(a)-1-si] {
				si++
			}
			combined = make([]InlinePiece, 0, 3)
			if pi > 0 {
				combined = append(combined, InlinePiece{Kind: InlinePieceEqual, Text: b[:pi]})
			}
			bStart, bEnd := pi, len(b)-si
			aStart, aEnd := pi, len(a)-si
			if bEnd < bStart {
				bEnd = bStart
			}
			if aEnd < aStart {
				aEnd = aStart
			}
			bd := b[bStart:bEnd]
			ae := a[aStart:aEnd]
			if len(bd) > 0 {
				combined = append(combined, InlinePiece{Kind: InlinePieceDelete, Text: bd})
			}
			if len(ae) > 0 {
				combined = append(combined, InlinePiece{Kind: InlinePieceInsert, Text: ae})
			}
			if si > 0 {
				combined = append(combined, InlinePiece{Kind: InlinePieceEqual, Text: b[len(b)-si:]})
			}
			if len(combined) == 0 {
				return nil, false
			}
			return combined, true
		}
		combined = make([]InlinePiece, 0, len(pieces))
		for _, p := range pieces {
			switch p.Kind {
			case diff.PieceEqual:
				combined = append(combined, InlinePiece{Kind: InlinePieceEqual, Text: p.Text})
			case diff.PieceDelete:
				combined = append(combined, InlinePiece{Kind: InlinePieceDelete, Text: p.Text})
			case diff.PieceInsert:
				combined = append(combined, InlinePiece{Kind: InlinePieceInsert, Text: p.Text})
			}
		}
		return combined, true
	}
}
