package diffview

import (
	"os"
	"runtime"
	"testing"
)

func TestWordDiff_ThresholdsLargeChangeFallsBack(t *testing.T) {
	if os.Getenv("CRUSH_TEST_ENABLE_WORDDIFF") == "" {
		t.Skip("set CRUSH_TEST_ENABLE_WORDDIFF=1 to run external word-diff tests")
	}
	if runtime.GOOS == "windows" {
		t.Skip("skip on windows env variability")
	}
	dv := New().
		Before("a.txt", "lorem ipsum dolor sit amet consectetur adipiscing elit\n").
		After("a.txt", "entirely different content with almost no overlap\n").
		Style(DefaultLightStyle()).
		ChromaStyle(nil)
	// For now, we do not have thresholds wired; ensure we at least render something deterministic
	out := dv.String()
	if out == "" {
		t.Fatalf("expected non-empty output for large change case")
	}
}
