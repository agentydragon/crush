package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrepWithIgnoreFiles(t *testing.T) {
	if getRg() == "" {
		t.Skip("rg is not in $PATH")
	}
	tempDir := t.TempDir()

	// Create test files
	testFiles := map[string]string{
		"file1.txt":           "hello world",
		"file2.txt":           "hello world",
		"ignored/file3.txt":   "hello world",
		"node_modules/lib.js": "hello world",
		"secret.key":          "hello world",
	}
	for path, content := range testFiles {
		fullPath := filepath.Join(tempDir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
		require.NoError(t, os.WriteFile(fullPath, []byte(content), 0o644))
	}
	// Create .gitignore and .crushignore
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, ".gitignore"), []byte("ignored/\n*.key\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, ".crushignore"), []byte("node_modules/\n"), 0o644))

	grepTool := NewGrepTool(tempDir)
	params := GrepParams{Pattern: "hello world", Path: tempDir}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	resp, err := grepTool.Run(context.Background(), ToolCall{Input: string(paramsJSON)})
	require.NoError(t, err)
	out := resp.Content
	require.Contains(t, out, "file1.txt")
	require.Contains(t, out, "file2.txt")
	require.NotContains(t, out, "file3.txt") // ignored/
	require.NotContains(t, out, "lib.js")     // node_modules/
	require.NotContains(t, out, "secret.key") // *.key
}

func TestGrepTimeoutPartialResults(t *testing.T) {
	if getRg() == "" {
		t.Skip("rg is not in $PATH")
	}
	tempDir := t.TempDir()
	for i := 0; i < 1200; i++ {
		p := filepath.Join(tempDir, "dir", "sub", "f"+strconv.Itoa(i)+".txt")
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		content := ""
		if i%9 == 0 {
			content = "needle here\n"
		}
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	grepTool := NewGrepTool(tempDir)
	params := GrepParams{Pattern: "needle", Path: tempDir}
	b, _ := json.Marshal(params)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	resp, err := grepTool.Run(ctx, ToolCall{Input: string(b)})
	require.NoError(t, err)
	require.Contains(t, resp.Content, "needle")
	require.Condition(t, func() bool {
		return strings.Contains(resp.Content, "Search aborted after") || strings.Contains(resp.Content, "(Results are truncated.")
	}, "expected timeout abort or truncation marker")
}
