package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

// Setup creates an isolated temporary work directory and points HOME/XDG_* to it,
// also chdir into it. Returns the temp path and a cleanup function that restores env and cwd.
func Setup() (string, func(), error) {
	tmp, err := os.MkdirTemp("", "crush-test-*")
	if err != nil {
		return "", nil, err
	}
	oldHome := os.Getenv("HOME")
	oldXDGData := os.Getenv("XDG_DATA_HOME")
	oldXDGConfig := os.Getenv("XDG_CONFIG_HOME")
	oldCwd, _ := os.Getwd()

	_ = os.Setenv("HOME", tmp)
	_ = os.Setenv("XDG_DATA_HOME", filepath.Join(tmp, ".local", "share"))
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	_ = os.Chdir(tmp)

	cleanup := func() {
		_ = os.Chdir(oldCwd)
		if oldHome == "" { os.Unsetenv("HOME") } else { os.Setenv("HOME", oldHome) }
		if oldXDGData == "" { os.Unsetenv("XDG_DATA_HOME") } else { os.Setenv("XDG_DATA_HOME", oldXDGData) }
		if oldXDGConfig == "" { os.Unsetenv("XDG_CONFIG_HOME") } else { os.Setenv("XDG_CONFIG_HOME", oldXDGConfig) }
		_ = os.RemoveAll(tmp)
	}
	return tmp, cleanup, nil
}

// MustSetup is a testing-friendly wrapper around Setup that fails the test on error.
func MustSetup(t *testing.T) (string, func()) {
	t.Helper()
	tmp, cleanup, err := Setup()
	if err != nil {
		t.Fatalf("testenv setup failed: %v", err)
	}
	return tmp, cleanup
}
