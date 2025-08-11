package diff

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/config"
)

func defaultExternalCmd() string {
	return "git diff --no-index --histogram --minimal -U3 -- a {old} -- b {new}"
}

func hasGit() bool {
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func runExternalDiff(before, after, fileName string) (string, bool) {
	cfg := config.Get()
	cmdTemplate := ""
	if cfg.Options != nil && cfg.Options.Diff != nil && cfg.Options.Diff.ExternalCommand != "" {
		cmdTemplate = cfg.Options.Diff.ExternalCommand
	} else if hasGit() {
		cmdTemplate = defaultExternalCmd()
	} else {
		return "", false
	}

	dir, _ := os.MkdirTemp("", "crush-diff-*")
	defer os.RemoveAll(dir)
	oldPath := filepath.Join(dir, "old")
	newPath := filepath.Join(dir, "new")
	_ = os.WriteFile(oldPath, []byte(before), 0o600)
	_ = os.WriteFile(newPath, []byte(after), 0o600)

	cmdStr := strings.ReplaceAll(cmdTemplate, "{old}", shQuote(oldPath))
	cmdStr = strings.ReplaceAll(cmdStr, "{new}", shQuote(newPath))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "cmd.exe", "/C", cmdStr)
	} else {
		c = exec.CommandContext(ctx, "/bin/sh", "-lc", cmdStr)
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	c.Stdout = &out
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 && out.Len() > 0 {
				return out.String(), true
			}
		}
		return "", false
	}
	return out.String(), true
}

func externalUnified(before, after, fileName string) (string, bool) {
	cfg := config.Get()
	cmd := ""
	if cfg.Options != nil && cfg.Options.Diff != nil {
		cmd = cfg.Options.Diff.ExternalCommand
	}
	text, ok := runExternalDiff(before, after, fileName)
	if !ok || strings.TrimSpace(text) == "" {
		return "", false
	}
	mode := "unified"
	if cfg.Options != nil && cfg.Options.Diff != nil && cfg.Options.Diff.ParseMode != "" {
		mode = cfg.Options.Diff.ParseMode
	}
	switch mode {
	case "git_word_porcelain":
		if !strings.Contains(cmd, "--word-diff=porcelain") {
			// If the configured command isn't porcelain, treat as unified.
			return text, true
		}
		if parsed, ok := parseGitWordPorcelain(text); ok {
			return parsed, true
		}
		return text, true
	case "auto":
		if strings.Contains(cmd, "--word-diff=porcelain") {
			if parsed, ok := parseGitWordPorcelain(text); ok {
				return parsed, true
			}
		}
		return text, true
	default:
		return text, true
	}
}
