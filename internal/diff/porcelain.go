package diff

import (
	"bufio"
	"strings"
)

// parseGitWordPorcelain extracts a minimal unified-like text from a git --word-diff=porcelain output.
// For now we don't emit spans; we just pass unified text through if available, otherwise return empty.
func parseGitWordPorcelain(out string) (string, bool) {
	// If the output already contains unified headers (---/+++ or @@), return as-is.
	if strings.Contains(out, "@@ ") || strings.Contains(out, "--- ") || strings.Contains(out, "+++ ") {
		return out, true
	}
	// Some porcelain outputs may be plain with markers; we can't reconstruct unified headers reliably here.
	// Return false to let callers fallback to unified path.
	_ = bufio.NewScanner(strings.NewReader(out))
	return "", false
}
