// Package testrepo creates throwaway git repositories for tests. It is
// imported only from _test files; nothing links it into the vincent binary.
package testrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run executes git with args in dir, failing the test on error, and returns
// trimmed combined output.
func Run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Init creates a temp repository on the given initial branch with one commit
// and returns its path. Skips the test when git is not installed.
func Init(t *testing.T, branch string) string {
	t.Helper()
	dir := InitEmpty(t, branch)
	WriteFile(t, dir, "README.md", "test repo\n")
	Run(t, dir, "add", ".")
	Run(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// InitEmpty creates a temp repository with no commits (unborn HEAD).
func InitEmpty(t *testing.T, branch string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	Run(t, dir, "init", "-q", "-b", branch)
	Run(t, dir, "config", "user.name", "vincent-test")
	Run(t, dir, "config", "user.email", "vincent-test@example.invalid")
	Run(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// WriteFile writes content under dir, creating parents.
func WriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
