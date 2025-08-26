package diffview

import (
	"strings"

	"github.com/charmbracelet/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderPieceUnified maps a single InlinePiece to a styled string for unified view.
func renderPieceUnified(p InlinePiece, lsEq, lsDel, lsIns LineStyle) string {
	switch p.Kind {
	case InlinePieceEqual:
		// Equal segments inherit the surrounding line background; avoid nested styling.
		return p.Text
	case InlinePieceDelete:
		return lsDel.Code.Render(p.Text)
	case InlinePieceInsert:
		return lsIns.Code.Render(p.Text)
	default:
		return p.Text
	}
}

// renderPieceSplitLeft maps a piece for the left column (before): equal + delete only.
func renderPieceSplitLeft(p InlinePiece, lsEq, lsDel LineStyle) string {
	switch p.Kind {
	case InlinePieceEqual:
		// Equal segments inherit the surrounding line background; avoid nested styling.
		return p.Text
	case InlinePieceDelete:
		return lsDel.Code.Render(p.Text)
	default:
		return ""
	}
}

// renderPieceSplitRight maps a piece for the right column (after): equal + insert only.
func renderPieceSplitRight(p InlinePiece, lsEq, lsIns LineStyle) string {
	switch p.Kind {
	case InlinePieceEqual:
		// Equal segments inherit the surrounding line background; avoid nested styling.
		return p.Text
	case InlinePieceInsert:
		return lsIns.Code.Render(p.Text)
	default:
		return ""
	}
}

// assembleUnified builds the full content string for a unified inline row.
func assembleUnified(seq []InlinePiece, lsEq, lsDel, lsIns LineStyle) string {
	var sb strings.Builder
	for _, p := range seq {
		sb.WriteString(renderPieceUnified(p, lsEq, lsDel, lsIns))
	}
	return sb.String()
}

// assembleSplitLeft builds left content for a split inline row (equal+delete only).
func assembleSplitLeft(seq []InlinePiece, lsEq, lsDel LineStyle) string {
	var sb strings.Builder
	for _, p := range seq {
		sb.WriteString(renderPieceSplitLeft(p, lsEq, lsDel))
	}
	return sb.String()
}

// assembleSplitRight builds right content for a split inline row (equal+insert only).
func assembleSplitRight(seq []InlinePiece, lsEq, lsIns LineStyle) string {
	var sb strings.Builder
	for _, p := range seq {
		sb.WriteString(renderPieceSplitRight(p, lsEq, lsIns))
	}
	return sb.String()
}

// truncateANSI cuts and truncates a styled string using dv offsets/widths.
func truncateANSI(s string, xOffset, codeWidth int) string {
	// Sanitize: inline content must not contain newlines; they break row alignment.
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	// First cut by xOffset in grapheme width, then truncate to codeWidth.
	cut := ansi.GraphemeWidth.Cut(s, xOffset, len(s))
	return ansi.Truncate(cut, codeWidth, "…")
}

// printUnifiedRow prints a single unified row with numbers and content.
func (dv *DiffView) printUnifiedRow(b *strings.Builder, ls LineStyle, beforeNum, afterNum int, content string) {
	full := lipgloss.NewStyle().MaxWidth(dv.fullCodeWidth)
	// Ensure inline content is single-line to avoid spilling into a new row without numbers.
	content = strings.ReplaceAll(content, "\r", "")
	content = strings.ReplaceAll(content, "\n", "")
	if dv.lineNumbers {
		b.WriteString(ls.LineNumber.Render(pad(beforeNum, dv.beforeNumDigits)))
		b.WriteString(ls.LineNumber.Render(pad(afterNum, dv.afterNumDigits)))
	}
	b.WriteString(full.Render(ls.Code.Width(dv.fullCodeWidth).Render("  " + content)))
	b.WriteString("\n")
}

// printSplitRow prints a single split row with numbers and left/right content.
func (dv *DiffView) printSplitRow(b *strings.Builder, lsL, lsR LineStyle, beforeNum, afterNum int, left, right string) {
	beforeFull := lipgloss.NewStyle().MaxWidth(dv.fullCodeWidth)
	afterFull := lipgloss.NewStyle().MaxWidth(dv.fullCodeWidth + btoi(dv.extraColOnAfter))
	if dv.lineNumbers {
		b.WriteString(lsL.LineNumber.Render(pad(beforeNum, dv.beforeNumDigits)))
	}
	b.WriteString(beforeFull.Render(lsL.Code.Width(dv.fullCodeWidth).Render("  " + left)))
	if dv.lineNumbers {
		b.WriteString(lsR.LineNumber.Render(pad(afterNum, dv.afterNumDigits)))
	}
	b.WriteString(afterFull.Render(lsR.Code.Width(dv.fullCodeWidth + btoi(dv.extraColOnAfter)).Render("  " + right)))
	b.WriteString("\n")
}
