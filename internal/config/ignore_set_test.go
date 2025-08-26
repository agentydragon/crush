package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TODO: support '!' negation in extra globs in a future change.

func TestIgnoreSet_Matches_CrushGitAndDefault(t *testing.T) {
	dir := t.TempDir()
	wd := dir
	// Create default-ignored dir
	if err := os.MkdirAll(filepath.Join(wd, "node_modules", "pkg"), 0o755); err != nil { t.Fatal(err) }
	// Create .gitignore and .crushignore
	if err := os.WriteFile(filepath.Join(wd, ".gitignore"), []byte("ignored_git.txt\n"), 0o644); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(wd, ".crushignore"), []byte("ignored_crush.txt\n"), 0o644); err != nil { t.Fatal(err) }
	// Files
	gitFile := filepath.Join(wd, "ignored_git.txt")
	crushFile := filepath.Join(wd, "ignored_crush.txt")
	defFile := filepath.Join(wd, "node_modules", "pkg", "x.txt")
	for _, f := range []string{gitFile, crushFile, defFile} {
		if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil { t.Fatal(err) }
	}

	is := WorkspaceIgnore(wd)
	if !is.Matches(wd, gitFile) {
		t.Fatalf("expected gitignored file to match: %s", gitFile)
	}
	if !is.Matches(wd, crushFile) {
		t.Fatalf("expected crushignored file to match: %s", crushFile)
	}
	if !is.Matches(wd, defFile) {
		t.Fatalf("expected default ignored path to match: %s", defFile)
	}
}

func TestLSPIgnore_ExtraGlobs(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{workingDir: dir, LSP: map[string]LSPConfig{
		"ts": {IgnoreGlobs: []string{"**/bazel-out/**", "terraform/.terraform/**"}},
	}}
	// Layout
	if err := os.MkdirAll(filepath.Join(dir, "bazel-out", "bin"), 0o755); err != nil { t.Fatal(err) }
	if err := os.MkdirAll(filepath.Join(dir, "terraform", ".terraform", "plugins"), 0o755); err != nil { t.Fatal(err) }
	p1 := filepath.Join(dir, "bazel-out", "bin", "a.js")
	p2 := filepath.Join(dir, "terraform", ".terraform", "plugins", "x")
	for _, p := range []string{p1, p2} {
		if err := os.WriteFile(p, []byte("hi"), 0o644); err != nil { t.Fatal(err) }
	}

	is := cfg.LSPIgnore("ts")
	if !is.Matches(dir, p1) { t.Fatalf("expected to match extra glob: %s", p1) }
	if !is.Matches(dir, p2) { t.Fatalf("expected to match extra glob: %s", p2) }
}
