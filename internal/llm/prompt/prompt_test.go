package prompt

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExpandPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected func() string
	}{
		{
			name:  "regular path unchanged",
			input: "/absolute/path",
			expected: func() string {
				return "/absolute/path"
			},
		},
		{
			name:  "tilde expansion",
			input: "~/documents",
			expected: func() string {
				home, _ := os.UserHomeDir()
				return filepath.Join(home, "documents")
			},
		},
		{
			name:  "tilde only",
			input: "~",
			expected: func() string {
				home, _ := os.UserHomeDir()
				return home
			},
		},
		{
			name:  "environment variable expansion",
			input: "$HOME",
			expected: func() string {
				return os.Getenv("HOME")
			},
		},
		{
			name:  "relative path unchanged",
			input: "relative/path",
			expected: func() string {
				return "relative/path"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandPath(tt.input)
			expected := tt.expected()

			// Skip test if environment variable is not set
			if strings.HasPrefix(tt.input, "$") && expected == "" {
				t.Skip("Environment variable not set")
			}

			if result != expected {
				t.Errorf("expandPath(%q) = %q, want %q", tt.input, result, expected)
			}
		})
	}
}

func TestProcessContextPaths(t *testing.T) {
	// Create a temporary directory and file for testing
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := "test content"

	err := os.WriteFile(testFile, []byte(testContent), 0o644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test with absolute path to file
	result := processContextPaths("", []string{testFile})
	expected := "# From:" + testFile + "\n" + testContent

	if result != expected {
		t.Errorf("processContextPaths with absolute path failed.\nGot: %q\nWant: %q", result, expected)
	}

	// Test with directory path (should process all files in directory)
	result = processContextPaths("", []string{tmpDir})
	if !strings.Contains(result, testContent) {
		t.Errorf("processContextPaths with directory path failed to include file content")
	}

	// Test with tilde expansion (if we can create a file in home directory)
	tmpDir = t.TempDir()
	setHomeEnv(t, tmpDir)
	homeTestFile := filepath.Join(tmpDir, "crush_test_file.txt")
	err = os.WriteFile(homeTestFile, []byte(testContent), 0o644)
	if err == nil {
		defer os.Remove(homeTestFile) // Clean up

		tildeFile := "~/crush_test_file.txt"
		result = processContextPaths("", []string{tildeFile})
		expected = "# From:" + homeTestFile + "\n" + testContent

		if result != expected {
			t.Errorf("processContextPaths with tilde expansion failed.\nGot: %q\nWant: %q", result, expected)
		}
	}
}

func TestTransclusionCyclesAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	// a.md -> includes b.md; b.md -> includes a.md (cycle)
	aPath := filepath.Join(dir, "a.md")
	bPath := filepath.Join(dir, "b.md")
	if err := os.WriteFile(aPath, []byte("A\n@b.md\n"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(bPath, []byte("B\n@a.md\n"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}

	out := processContextPaths("", []string{aPath})
	// a and b should each appear exactly once
	if got := strings.Count(out, "# From:"+aPath); got != 1 {
		t.Fatalf("expected a included once, got %d\nout=%q", got, out)
	}
	if got := strings.Count(out, "# From:"+bPath); got != 1 {
		t.Fatalf("expected b included once, got %d\nout=%q", got, out)
	}
	// Content lines should be present exactly once
	if got := strings.Count(out, "\nA\n"); got != 1 {
		t.Fatalf("expected 'A' once, got %d\nout=%q", got, out)
	}
	if got := strings.Count(out, "\nB\n"); got != 1 {
		t.Fatalf("expected 'B' once, got %d\nout=%q", got, out)
	}
}

func TestTransclusionMultipleReferences(t *testing.T) {
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.md")
	bPath := filepath.Join(dir, "b.md")
	if err := os.WriteFile(bPath, []byte("B\n"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	if err := os.WriteFile(aPath, []byte("A\n@b.md\n@b.md\n"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}

	out := processContextPaths("", []string{aPath})
	if got := strings.Count(out, "# From:"+bPath); got != 1 {
		t.Fatalf("expected b included once despite multiple refs, got %d\nout=%q", got, out)
	}
}

func TestTransclusionDedupAcrossTopLevelInputs(t *testing.T) {
	dir := t.TempDir()
	inc := filepath.Join(dir, "inc.md")
	if err := os.WriteFile(inc, []byte("INC\n"), 0o644); err != nil {
		t.Fatalf("write inc: %v", err)
	}
	top1 := filepath.Join(dir, "top1.md")
	top2 := filepath.Join(dir, "top2.md")
	if err := os.WriteFile(top1, []byte("@inc.md\n"), 0o644); err != nil {
		t.Fatalf("write top1: %v", err)
	}
	if err := os.WriteFile(top2, []byte("@inc.md\n"), 0o644); err != nil {
		t.Fatalf("write top2: %v", err)
	}

	out := processContextPaths("", []string{top1, top2})
	if got := strings.Count(out, "# From:"+inc); got != 1 {
		t.Fatalf("expected inc included once across top-level inputs, got %d\nout=%q", got, out)
	}
}

func setHomeEnv(tb testing.TB, path string) {
	tb.Helper()
	key := "HOME"
	if runtime.GOOS == "windows" {
		key = "USERPROFILE"
	}
	tb.Setenv(key, path)
}
