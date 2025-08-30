package diffview

import (
	"slices"

	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/x/exp/slice"
)

type splitHunk struct {
	fromLine int
	toLine   int
	lines    []*splitLine
}

type splitLine struct {
	before *udiff.Line
	after  *udiff.Line
}

// hunkToSplit converts a unified hunk into split rows. When pairReplacements is true,
// it will pair a Delete followed by an Insert (before encountering an Equal) into a
// single splitLine with both before and after populated. This enables features like
// indent-only change detection that rely on having both sides available.
func hunkToSplit(h *udiff.Hunk, pairReplacements bool) (sh splitHunk) {
	lines := slices.Clone(h.Lines)
	sh = splitHunk{
		fromLine: h.FromLine,
		toLine:   h.ToLine,
		lines:    make([]*splitLine, 0, len(lines)),
	}

	for {
		var ul udiff.Line
		var ok bool
		ul, lines, ok = slice.Shift(lines)
		if !ok {
			break
		}

		var sl splitLine

		switch ul.Kind {
		// For equal lines, add as is
		case udiff.Equal:
			ulCopy := ul
			sl.before = &ulCopy
			sl.after = &ulCopy

		// For inserted lines, set after and keep before as nil
		case udiff.Insert:
			ulCopy := ul
			sl.before = nil
			sl.after = &ulCopy

		// For deleted lines, set before and (optionally) pair with next insert.
		case udiff.Delete:
			ulCopy := ul
			sl.before = &ulCopy

			if pairReplacements {
				// Look ahead for an Insert before any Equal, pair it and consume it.
				inner:
				for i, l := range lines {
					switch l.Kind {
					case udiff.Insert:
						ln := l // copy
						sl.after = &ln
						_, lines, _ = slice.DeleteAt(lines, i)
						break inner
					case udiff.Equal:
						break inner
					}
				}
			} else {
				// Keep columns dense: do not pair, but still consume a following insert
				// so it appears as its own right-only row later.
				inner2:
				for i, l := range lines {
					switch l.Kind {
					case udiff.Insert:
						_, lines, _ = slice.DeleteAt(lines, i)
						break inner2
					case udiff.Equal:
						break inner2
					}
				}
			}
		}

		sh.lines = append(sh.lines, &sl)
	}

	return
}
