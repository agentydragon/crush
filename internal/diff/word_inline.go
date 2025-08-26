package diff

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// PieceKind represents a word-diff piece kind.
type PieceKind int

const (
	PieceEqual PieceKind = iota + 1
	PieceInsert
	PieceDelete
)

// Piece is a contiguous run of equal/insert/delete text.
type Piece struct {
	Kind PieceKind
	Text string
}

// PerLineWordPieces runs an external git word-diff on two single lines and returns combined pieces.
// Requires git in PATH. Lines may include or omit a trailing \n; function normalizes internally.
func PerLineWordPieces(before, after string) ([]Piece, bool) {
	before = strings.TrimSuffix(before, "\r\n")
	before = strings.TrimSuffix(before, "\n") + "\n"
	after = strings.TrimSuffix(after, "\r\n")
	after = strings.TrimSuffix(after, "\n") + "\n"

	if !hasGit() {
		return nil, false
	}

	dir, _ := os.MkdirTemp("", "crush-worddiff-*")
	defer os.RemoveAll(dir)
	oldPath := dir + "/old"
	newPath := dir + "/new"
	_ = os.WriteFile(oldPath, []byte(before), 0o600)
	_ = os.WriteFile(newPath, []byte(after), 0o600)

	cmdStr := "git diff --no-index --word-diff=porcelain -U0 -- a " + shQuote(oldPath) + " -- b " + shQuote(newPath)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "cmd.exe", "/C", cmdStr)
	} else {
		c = exec.CommandContext(ctx, "/bin/sh", "-lc", cmdStr)
	}
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = new(bytes.Buffer)
	_ = c.Run() // exit status 1 is normal for diffs

	body := filterPorcelainContent(out.String())
	if strings.TrimSpace(body) == "" {
		return nil, false
	}

	pieces := parsePorcelainInline(body)
	if len(pieces) == 0 {
		return nil, false
	}

	// Drop trailing newline from final equal piece if present
	if n := len(pieces); n > 0 {
		pieces[n-1].Text = strings.TrimSuffix(pieces[n-1].Text, "\n")
	}
	return pieces, true
}

// filterPorcelainContent removes headers and keeps only body lines with markers.
func filterPorcelainContent(s string) string {
	var b strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") ||
			strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, "\\ No newline at end of file") {
			continue
		}
		// Keep any line; porcelain markers will be inside
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

// parsePorcelainInline converts a line with markers into combined pieces.
// Recognizes deletions as "[- ... -]" and insertions as "{+ ... +}".
func parsePorcelainInline(s string) []Piece {
	pieces := make([]Piece, 0, 8)
	for len(s) > 0 {
		idxDel := strings.Index(s, "[-")
		idxIns := strings.Index(s, "{+")
		// find earliest marker
		idx := -1
		kind := 0
		if idxDel >= 0 && (idxIns < 0 || idxDel < idxIns) {
			idx = idxDel
			kind = int(PieceDelete)
		} else if idxIns >= 0 {
			idx = idxIns
			kind = int(PieceInsert)
		}
		if idx < 0 {
			if s != "" {
				pieces = append(pieces, Piece{Kind: PieceEqual, Text: s})
			}
			break
		}
		if idx > 0 {
			pieces = append(pieces, Piece{Kind: PieceEqual, Text: s[:idx]})
			s = s[idx:]
		}
		switch PieceKind(kind) {
		case PieceDelete:
			end := strings.Index(s, "-]")
			if end < 0 { // malformed
				pieces = append(pieces, Piece{Kind: PieceEqual, Text: s})
				return pieces
			}
			content := s[len("[-"):end]
			pieces = append(pieces, Piece{Kind: PieceDelete, Text: content})
			s = s[end+len("-]"):]
		case PieceInsert:
			end := strings.Index(s, "+}")
			if end < 0 {
				pieces = append(pieces, Piece{Kind: PieceEqual, Text: s})
				return pieces
			}
			content := s[len("{+"):end]
			pieces = append(pieces, Piece{Kind: PieceInsert, Text: content})
			s = s[end+len("+}"):]
		}
	}
	return pieces
}
