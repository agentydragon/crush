package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
	ignore "github.com/sabhiram/go-gitignore"
)

// IgnoreSet centralizes ignore rules for a workspace.
// Currently includes:
//  - .crushignore at workspace root (gitignore semantics)
//  - optional extra doublestar globs (e.g., per-LSP ignore_globs)
//
// TODO: consider incorporating .gitignore as well (opt-in), but today
// search tools already respect it separately.
// TODO: consider default directory excludes here to have a single source of truth.

type IgnoreSet struct {
	crush       *ignore.GitIgnore
	git         *ignore.GitIgnore
	extraGlobs   []string
	defaultGlobs []string
}

// Crush exposes the compiled .crushignore (read-only use by other packages).
func (s *IgnoreSet) Crush() *ignore.GitIgnore { return s.crush }

func (s *IgnoreSet) Matches(workspacePath, path string) bool {
	rel, err := filepath.Rel(workspacePath, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if s.crush != nil && s.crush.MatchesPath(rel) {
		return true
	}
	if s.git != nil && s.git.MatchesPath(rel) {
		return true
	}
	for _, g := range s.extraGlobs {
		if ok, _ := doublestar.PathMatch(g, rel); ok {
			return true
		}
	}
	for _, g := range s.defaultGlobs {
		if ok, _ := doublestar.PathMatch(g, rel); ok {
			return true
		}
	}
	return false
}

var (
	crushIgnoreCache sync.Map // key: workspacePath -> *ignore.GitIgnore
	gitIgnoreCache   sync.Map // key: workspacePath -> *ignore.GitIgnore
)

// WorkspaceIgnore returns an IgnoreSet for the workspace including .crushignore and .gitignore plus defaults.
func WorkspaceIgnore(workspacePath string) *IgnoreSet {
	return &IgnoreSet{
		crush:       loadCrushIgnoreOnce(workspacePath),
		git:         loadGitIgnoreOnce(workspacePath),
		defaultGlobs: defaultIgnoreGlobs(),
	}
}

// LSPIgnore returns an IgnoreSet for a given LSP name, merging .crushignore + .gitignore + extra globs + defaults.
func (c *Config) LSPIgnore(name string) *IgnoreSet {
	var globs []string
	if c != nil {
		if l, ok := c.LSP[name]; ok {
			globs = append(globs, normalizeGlobs(l.IgnoreGlobs)...)
		}
	}
	return &IgnoreSet{
		crush:       loadCrushIgnoreOnce(c.WorkingDir()),
		git:         loadGitIgnoreOnce(c.WorkingDir()),
		extraGlobs:   globs,
		defaultGlobs: defaultIgnoreGlobs(),
	}
}

func normalizeGlobs(globs []string) []string {
	out := make([]string, 0, len(globs))
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if g == "" || strings.HasPrefix(g, "#") {
			continue
		}
		g = strings.TrimPrefix(g, "./")
		g = strings.TrimPrefix(g, "/")
		g = filepath.ToSlash(g)
		if strings.HasSuffix(g, "/") {
			g = g + "**"
		}
		out = append(out, g)
	}
	return out
}

func loadCrushIgnoreOnce(workspacePath string) *ignore.GitIgnore {
	if v, ok := crushIgnoreCache.Load(workspacePath); ok {
		if gi, ok2 := v.(*ignore.GitIgnore); ok2 {
			return gi
		}
	}
	p := filepath.Join(workspacePath, ".crushignore")
	if _, err := os.Stat(p); err == nil {
		if gi, err := ignore.CompileIgnoreFile(p); err == nil {
			crushIgnoreCache.Store(workspacePath, gi)
			return gi
		}
	}
	crushIgnoreCache.Store(workspacePath, (*ignore.GitIgnore)(nil))
	return nil
}

func loadGitIgnoreOnce(workspacePath string) *ignore.GitIgnore {
	if v, ok := gitIgnoreCache.Load(workspacePath); ok {
		if gi, ok2 := v.(*ignore.GitIgnore); ok2 {
			return gi
		}
	}
	p := filepath.Join(workspacePath, ".gitignore")
	if _, err := os.Stat(p); err == nil {
		if gi, err := ignore.CompileIgnoreFile(p); err == nil {
			gitIgnoreCache.Store(workspacePath, gi)
			return gi
		}
	}
	gitIgnoreCache.Store(workspacePath, (*ignore.GitIgnore)(nil))
	return nil
}

func defaultIgnoreGlobs() []string {
	return []string{
		".crush/**",
		".git/**",
		"node_modules/**",
		"vendor/**",
		"dist/**",
		"build/**",
		"target/**",
		"**/__pycache__/**",
		"bin/**",
		"obj/**",
		"out/**",
		"coverage/**",
		"logs/**",
		"generated/**",
		"bower_components/**",
		"jspm_packages/**",
	}
}
