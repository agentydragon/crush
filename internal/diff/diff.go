package diff

import (
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// GenerateDiff creates a unified diff from two file contents
func GenerateDiff(beforeContent, afterContent, fileName string) (string, int, int) {
	fileName = strings.TrimPrefix(fileName, "/")

	if unified, ok := externalUnified(beforeContent, afterContent, fileName); ok {
		adds, rems := countChanges(unified)
		return unified, adds, rems
	}

	unified := udiff.Unified("a/"+fileName, "b/"+fileName, beforeContent, afterContent)
	adds, rems := countChanges(unified)
	return unified, adds, rems
}

func countChanges(unified string) (int, int) {
	additions := 0
	removals := 0
	lines := strings.SplitSeq(unified, "\n")
	for line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			additions++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removals++
		}
	}
	return additions, removals
}
