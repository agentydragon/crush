package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/crush/internal/config"
)

type GrepParams struct {
	Pattern     string `json:"pattern"`
	Path        string `json:"path"`
	Include     string `json:"include"`
	LiteralText bool   `json:"literal_text"`
}

type grepMatch struct {
	path     string
	modTime  time.Time
	lineNum  int
	lineText string
}

type GrepResponseMetadata struct {
	NumberOfMatches int  `json:"number_of_matches"`
	Truncated       bool `json:"truncated"`
}

type grepTool struct {
	workingDir string
}

const (
	GrepToolName    = "grep"
	grepDescription = `Fast content search tool powered by ripgrep (rg). Finds files containing specific text or patterns, returning matching file paths sorted by modification time (newest first).

REQUIREMENTS:
- Requires ripgrep (rg) to be installed and available on $PATH. If rg is missing, the tool returns an error.

WHEN TO USE THIS TOOL:
- Use when you need to find files containing specific text or patterns
- Great for searching code bases for function names, variable declarations, or error messages
- Useful for finding all files that use a particular API or pattern

HOW TO USE:
- Provide a regex pattern to search for within file contents
- Set literal_text=true to search for exact text with special characters (pattern is escaped to a safe regex)
- Optionally specify a starting directory (defaults to current working directory)
- Optionally provide an include pattern to filter which files to search (passed as ripgrep --glob)
- Results are sorted with most recently modified files first

LIMITATIONS:
- Results are limited to 100 files (newest first)
- Output lines are truncated to a maximum length for safety
- Overall output is truncated if it exceeds a safe maximum length

IGNORE FILE SUPPORT:
- ripgrep automatically respects .gitignore
- Also passes .crushignore via --ignore-file when present
`
)

func NewGrepTool(workingDir string) BaseTool {
	return &grepTool{workingDir: workingDir}
}

func (g *grepTool) Name() string { return GrepToolName }

func (g *grepTool) Info() ToolInfo {
	return ToolInfo{
		Name:        GrepToolName,
		Description: grepDescription,
		Parameters: map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "The regex pattern to search for in file contents",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "The directory to search in. Defaults to the current working directory.",
			},
			"include": map[string]any{
				"type":        "string",
				"description": "File pattern to include in the search (e.g. \"*.js\", \"*.{ts,tsx}\")",
			},
			"literal_text": map[string]any{
				"type":        "boolean",
				"description": "If true, the pattern will be treated as literal text with special regex characters escaped. Default is false.",
			},
		},
		Required: []string{"pattern"},
	}
}

// escapeRegexPattern escapes regex metacharacters so ripgrep treats the search as literal.
func escapeRegexPattern(pattern string) string {
	special := []string{"\\", ".", "+", "*", "?", "(", ")", "[", "]", "{", "}", "^", "$", "|"}
	for _, ch := range special {
		pattern = strings.ReplaceAll(pattern, ch, "\\"+ch)
	}
	return pattern
}

func (g *grepTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params GrepParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}
	if params.Pattern == "" { return NewTextErrorResponse("pattern is required"), nil }

	searchPattern := params.Pattern
	if params.LiteralText {
		searchPattern = escapeRegexPattern(params.Pattern)
	}

	searchPath := params.Path
	if searchPath == "" { searchPath = g.workingDir }

	to := 10 * time.Second
	if cfg := config.Get(); cfg != nil && cfg.Options != nil && cfg.Options.GrepTimeoutSecs > 0 {
		to = time.Duration(cfg.Options.GrepTimeoutSecs) * time.Second
	}
	ctxTO, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	matches, matchesTruncated, err := searchFiles(ctxTO, searchPattern, searchPath, params.Include, 100)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error searching files: %w", err)
	}

	var output strings.Builder
	if ctxTO.Err() == context.DeadlineExceeded || ctxTO.Err() == context.Canceled {
		fmt.Fprintf(&output, "Search aborted after %s due to timeout. Consider narrowing your pattern or path.\n\n", to)
	}
	if len(matches) == 0 {
		output.WriteString("No files found")
	} else {
		fmt.Fprintf(&output, "Found %d matches\n", len(matches))
		currentFile := ""
		for _, m := range matches {
			if currentFile != m.path {
				if currentFile != "" { output.WriteString("\n") }
				currentFile = m.path
				fmt.Fprintf(&output, "%s:\n", m.path)
			}
			if m.lineNum > 0 {
				fmt.Fprintf(&output, "  Line %d: %s\n", m.lineNum, truncateGrepLine(m.lineText))
			} else {
				fmt.Fprintf(&output, "  %s\n", m.path)
			}
		}
		if matchesTruncated {
			output.WriteString("\n(Results are truncated. Consider using a more specific path or pattern.)")
		}
	}

	raw := output.String()
	outTruncated := false
	if len(raw) > MaxOutputLength {
		raw = truncateOutput(raw)
		outTruncated = true
	}

	return WrapTextWithMeta(raw, GrepResponseMetadata{NumberOfMatches: len(matches), Truncated: matchesTruncated || outTruncated})
}

func truncateGrepLine(line string) string {
	if len(line) > MaxLineLength { return line[:MaxLineLength] + "..." }
	return line
}

func searchFiles(ctx context.Context, pattern, rootPath, include string, limit int) ([]grepMatch, bool, error) {
	matches, err := searchWithRipgrep(ctx, pattern, rootPath, include)
	partial := false
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded || ctx.Err() == context.Canceled {
			partial = true
		} else {
			return nil, false, err
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].modTime.After(matches[j].modTime) })
	truncated := partial || len(matches) > limit
	if len(matches) > limit { matches = matches[:limit] }
	return matches, truncated, nil
}

func searchWithRipgrep(ctx context.Context, pattern, path, include string) ([]grepMatch, error) {
	cmd := getRgSearchCmd(ctx, pattern, path, include)
	if cmd == nil { return nil, fmt.Errorf("ripgrep not found in $PATH") }

	cmd.Args = append(cmd.Args, "--ignore-file", filepath.Join(path, ".gitignore"))
	cmd.Args = append(cmd.Args, "--ignore-file", filepath.Join(path, ".crushignore"))

	stdout, err := cmd.StdoutPipe()
	if err != nil { return nil, err }
	stderr, err := cmd.StderrPipe()
	if err != nil { return nil, err }
	if err := cmd.Start(); err != nil { return nil, err }

	// Collect stderr in background for error diagnostics
	var stderrBuf strings.Builder
	stderrScanner := bufio.NewScanner(stderr)
	stderrScanner.Buffer(make([]byte, 1024), 1024*1024)
	go func() { for stderrScanner.Scan() { _ = stderrBuf.WriteByte('\n'); stderrBuf.WriteString(stderrScanner.Text()) } }()

	matches := make([]grepMatch, 0, 256)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if runtime.GOOS != "windows" {
				_ = cmd.Process.Signal(syscall.SIGTERM)
				time.AfterFunc(500*time.Millisecond, func() { _ = cmd.Process.Kill() })
			} else {
				_ = cmd.Process.Kill()
			}
		case <-done:
		}
	}()

	statCache := make(map[string]time.Time)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" { continue }
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 { continue }
		filePath := parts[0]
		lineNum, err := strconv.Atoi(parts[1])
		if err != nil { continue }
		lineText := parts[2]
		modTime, ok := statCache[filePath]
		if !ok {
			fi, err := os.Stat(filePath)
			if err != nil { continue }
			modTime = fi.ModTime()
			statCache[filePath] = modTime
		}
		matches = append(matches, grepMatch{path: filePath, modTime: modTime, lineNum: lineNum, lineText: lineText})
	}
	close(done)
	waitErr := cmd.Wait()

	// Helper to detect EMFILE/too many open files in stderr
	hasEMFILE := func(s string) bool {
		ls := strings.ToLower(s)
		return strings.Contains(ls, "too many open files") || strings.Contains(ls, "emfile")
	}
	stderrStr := stderrBuf.String()

	if ctx.Err() != nil { return matches, ctx.Err() }
	if err := scanner.Err(); err != nil {
		if hasEMFILE(stderrStr) {
			return matches, fmt.Errorf("FATAL: too many open files (EMFILE) during ripgrep search. Crush likely exhausted file descriptors. Action: reduce watchers/CRUSH_MAX_WATCHED_DIRS or narrow path; see ~/.crush/logs/ui/ui.log and pprof snapshot for FD counts")
		}
		return matches, err
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []grepMatch{}, nil
		}
		if hasEMFILE(stderrStr) {
			return matches, fmt.Errorf("FATAL: too many open files (EMFILE) during ripgrep wait. Crush likely exhausted file descriptors. Action: reduce watchers/CRUSH_MAX_WATCHED_DIRS or narrow path; see ~/.crush/logs/ui/ui.log and pprof snapshot for FD counts")
		}
		return matches, waitErr
	}
	return matches, nil
}
