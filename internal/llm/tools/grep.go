package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/config"
)

// regexCache provides thread-safe caching of compiled regex patterns
type regexCache struct {
	cache map[string]*regexp.Regexp
	mu    sync.RWMutex
}

// newRegexCache creates a new regex cache
func newRegexCache() *regexCache {
	return &regexCache{
		cache: make(map[string]*regexp.Regexp),
	}
}

// get retrieves a compiled regex from cache or compiles and caches it
func (rc *regexCache) get(pattern string) (*regexp.Regexp, error) {
	// Try to get from cache first (read lock)
	rc.mu.RLock()
	if regex, exists := rc.cache[pattern]; exists {
		rc.mu.RUnlock()
		return regex, nil
	}
	rc.mu.RUnlock()

	// Compile the regex (write lock)
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Double-check in case another goroutine compiled it while we waited
	if regex, exists := rc.cache[pattern]; exists {
		return regex, nil
	}

	// Compile and cache the regex
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	rc.cache[pattern] = regex
	return regex, nil
}

// Global regex cache instances
var (
	searchRegexCache = newRegexCache()
	globRegexCache   = newRegexCache()
	// Pre-compiled regex for glob conversion (used frequently)
	globBraceRegex = regexp.MustCompile(`\{([^}]+)\}`)
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
	grepDescription = `Fast content search tool that finds files containing specific text or patterns, returning matching file paths sorted by modification time (newest first).

WHEN TO USE THIS TOOL:
- Use when you need to find files containing specific text or patterns
- Great for searching code bases for function names, variable declarations, or error messages
- Useful for finding all files that use a particular API or pattern

HOW TO USE:
- Provide a regex pattern to search for within file contents
- Set literal_text=true if you want to search for the exact text with special characters (recommended for non-regex users)
- Optionally specify a starting directory (defaults to current working directory)
- Optionally provide an include pattern to filter which files to search
- Results are sorted with most recently modified files first

REGEX PATTERN SYNTAX (when literal_text=false):
- Supports standard regular expression syntax
- 'function' searches for the literal text "function"
- 'log\..*Error' finds text starting with "log." and ending with "Error"
- 'import\s+.*\s+from' finds import statements in JavaScript/TypeScript

COMMON INCLUDE PATTERN EXAMPLES:
- '*.js' - Only search JavaScript files
- '*.{ts,tsx}' - Only search TypeScript files
- '*.go' - Only search Go files

LIMITATIONS:
- Results are limited to 100 files (newest first)
- Output lines are truncated to a maximum length for safety
- Overall output is truncated if it exceeds a safe maximum length
- Performance depends on the number of files being searched
- Very large binary files may be skipped
- Hidden files (starting with '.') are skipped

IGNORE FILE SUPPORT:
- Respects .gitignore patterns to skip ignored files and directories
- Respects .crushignore patterns for additional ignore rules
- Both ignore files are automatically detected in the search root directory

CROSS-PLATFORM NOTES:
- Requires ripgrep (rg) to be installed and available on $PATH
- File paths are normalized automatically for cross-platform compatibility

TIPS:
- For faster, more targeted searches, first use Glob to find relevant files, then use Grep
- When doing iterative exploration that may require multiple rounds of searching, consider using the Agent tool instead
- Always check if results are truncated and refine your search pattern if needed
- Use literal_text=true when searching for exact text containing special characters like dots, parentheses, etc.`
)

func NewGrepTool(workingDir string) BaseTool {
	return &grepTool{
		workingDir: workingDir,
	}
}

func (g *grepTool) Name() string {
	return GrepToolName
}

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

// escapeRegexPattern escapes special regex characters so they're treated as literal characters
func escapeRegexPattern(pattern string) string {
	specialChars := []string{"\\", ".", "+", "*", "?", "(", ")", "[", "]", "{", "}", "^", "$", "|"}
	escaped := pattern

	for _, char := range specialChars {
		escaped = strings.ReplaceAll(escaped, char, "\\"+char)
	}

	return escaped
}

func truncateGrepLine(line string) string {
	if len(line) > MaxLineLength {
		return line[:MaxLineLength] + "..."
	}
	return line
}

func (g *grepTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params GrepParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if params.Pattern == "" {
		return NewTextErrorResponse("pattern is required"), nil
	}

	// If literal_text is true, escape the pattern
	searchPattern := params.Pattern
	if params.LiteralText {
		searchPattern = escapeRegexPattern(params.Pattern)
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = g.workingDir
	}

	// Apply configurable timeout for grep
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
		for _, match := range matches {
			if currentFile != match.path {
				if currentFile != "" {
					output.WriteString("\n")
				}
				currentFile = match.path
				fmt.Fprintf(&output, "%s:\n", match.path)
			}
			if match.lineNum > 0 {
				fmt.Fprintf(&output, "  Line %d: %s\n", match.lineNum, truncateGrepLine(match.lineText))
			} else {
				fmt.Fprintf(&output, "  %s\n", match.path)
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

	return WithResponseMetadata(
		NewTextResponse(raw),
		GrepResponseMetadata{
			NumberOfMatches: len(matches),
			Truncated:       matchesTruncated || outTruncated,
		},
	), nil
}

func searchFiles(ctx context.Context, pattern, rootPath, include string, limit int) ([]grepMatch, bool, error) {
	matches, err := searchWithRipgrep(ctx, pattern, rootPath, include)
	partial := false
	if err != nil {
		// If timeout/cancel, return partial results collected so far without error
		if ctx.Err() == context.DeadlineExceeded || ctx.Err() == context.Canceled {
			partial = true
			// Best-effort fallback: if rg yielded nothing yet, try a quick regex scan
			if len(matches) == 0 {
				if m2, err2 := searchFilesWithRegex(pattern, rootPath, include); err2 == nil && len(m2) > 0 {
					matches = m2
				}
			}
		} else {
			// No fallback: require ripgrep to be available and succeed
			return nil, false, err
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].modTime.After(matches[j].modTime)
	})

	truncated := partial || len(matches) > limit
	if len(matches) > limit {
		matches = matches[:limit]
	}

	return matches, truncated, nil
}

func searchWithRipgrep(ctx context.Context, pattern, path, include string) ([]grepMatch, error) {
	cmd := getRgSearchCmd(ctx, pattern, path, include)
	if cmd == nil {
		return nil, fmt.Errorf("ripgrep not found in $PATH")
	}

	cmd.Args = append(
		cmd.Args,
		"--ignore-file", filepath.Join(path, ".gitignore"),
		"--ignore-file", filepath.Join(path, ".crushignore"),
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Start the command
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	matches := make([]grepMatch, 0, 256)
	scanner := bufio.NewScanner(stdout)
	// Allow long lines
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	// Kill process on context cancel/timeout
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Try graceful shutdown to allow rg to flush buffered matches
			if runtime.GOOS != "windows" {
				_ = cmd.Process.Signal(syscall.SIGTERM)
				// Fallback hard kill after a short grace period
				time.AfterFunc(150*time.Millisecond, func() { _ = cmd.Process.Kill() })
			} else {
				_ = cmd.Process.Kill()
			}
		case <-done:
		}
	}()

	statCache := make(map[string]time.Time)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		filePath := parts[0]
		lineNum, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		lineText := parts[2]

		modTime, ok := statCache[filePath]
		if !ok {
			fi, err := os.Stat(filePath)
			if err != nil {
				continue
			}
			modTime = fi.ModTime()
			statCache[filePath] = modTime
		}

		matches = append(matches, grepMatch{
			path:     filePath,
			modTime:  modTime,
			lineNum:  lineNum,
			lineText: lineText,
		})
	}
	close(done)
	waitErr := cmd.Wait()

	// If context timed out/canceled, return partial with context error
	if ctx.Err() != nil {
		return matches, ctx.Err()
	}
	// Scanner error (unrelated to ctx)
	if err := scanner.Err(); err != nil {
		return matches, err
	}
	// Non-zero exit but not "no matches"
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []grepMatch{}, nil
		}
		return matches, waitErr
	}
	return matches, nil
}

func searchFilesWithRegex(pattern, rootPath, include string) ([]grepMatch, error) {
	matches := []grepMatch{}

	// Use cached regex compilation
	regex, err := searchRegexCache.get(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	var includePattern *regexp.Regexp
	if include != "" {
		regexPattern := globToRegex(include)
		includePattern, err = globRegexCache.get(regexPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid include pattern: %w", err)
		}
	}

	// Create walker with gitignore and crushignore support
	walker := fsext.NewFastGlobWalker(rootPath)

	err = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		if info.IsDir() {
			return nil // Skip directories
		}

		// Use walker's shouldSkip method instead of just SkipHidden
		if walker.ShouldSkip(path) {
			return nil
		}

		if includePattern != nil && !includePattern.MatchString(path) {
			return nil
		}

		match, lineNum, lineText, err := fileContainsPattern(path, regex)
		if err != nil {
			return nil // Skip files we can't read
		}

		if match {
			matches = append(matches, grepMatch{
				path:     path,
				modTime:  info.ModTime(),
				lineNum:  lineNum,
				lineText: lineText,
			})

			if len(matches) >= 200 {
				return filepath.SkipAll
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return matches, nil
}

func fileContainsPattern(filePath string, pattern *regexp.Regexp) (bool, int, string, error) {
	// Quick binary file detection
	if isBinaryFile(filePath) {
		return false, 0, "", nil
	}

	file, err := os.Open(filePath)
	if err != nil {
		return false, 0, "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if pattern.MatchString(line) {
			return true, lineNum, line, nil
		}
	}

	return false, 0, "", scanner.Err()
}

var binaryExts = map[string]struct{}{
	".exe": {}, ".dll": {}, ".so": {}, ".dylib": {},
	".bin": {}, ".obj": {}, ".o": {}, ".a": {},
	".zip": {}, ".tar": {}, ".gz": {}, ".bz2": {},
	".jpg": {}, ".jpeg": {}, ".png": {}, ".gif": {},
	".pdf": {}, ".doc": {}, ".docx": {}, ".xls": {},
	".mp3": {}, ".mp4": {}, ".avi": {}, ".mov": {},
}

// isBinaryFile performs a quick check to determine if a file is binary
func isBinaryFile(filePath string) bool {
	// Check file extension first (fastest)
	ext := strings.ToLower(filepath.Ext(filePath))
	if _, isBinary := binaryExts[ext]; isBinary {
		return true
	}

	// Quick content check for files without clear extensions
	file, err := os.Open(filePath)
	if err != nil {
		return false // If we can't open it, let the caller handle the error
	}
	defer file.Close()

	// Read first 512 bytes to check for null bytes
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return false
	}

	// Check for null bytes (common in binary files)
	for i := range n {
		if buffer[i] == 0 {
			return true
		}
	}

	return false
}

func globToRegex(glob string) string {
	regexPattern := strings.ReplaceAll(glob, ".", "\\.")
	regexPattern = strings.ReplaceAll(regexPattern, "*", ".*")
	regexPattern = strings.ReplaceAll(regexPattern, "?", ".")

	// Use pre-compiled regex instead of compiling each time
	regexPattern = globBraceRegex.ReplaceAllStringFunc(regexPattern, func(match string) string {
		inner := match[1 : len(match)-1]
		return "(" + strings.ReplaceAll(inner, ",", "|") + ")"
	})

	return regexPattern
}
