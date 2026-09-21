package gitx

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		raw          string
		major, minor int
		ok           bool
	}{
		{"git version 2.43.0.windows.1", 2, 43, true},
		{"git version 2.31.1", 2, 31, true},
		{"git version 3.0.0-rc1", 3, 0, true},
		{"no digits here", 0, 0, false},
	}
	for _, tt := range tests {
		major, minor, ok := parseVersion(tt.raw)
		if major != tt.major || minor != tt.minor || ok != tt.ok {
			t.Errorf("parseVersion(%q) = %d, %d, %v; want %d, %d, %v",
				tt.raw, major, minor, ok, tt.major, tt.minor, tt.ok)
		}
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func TestRunSuccess(t *testing.T) {
	requireGit(t)
	out, err := New().Run(context.Background(), t.TempDir(), "version")
	if err != nil {
		t.Fatalf("Run(version): %v", err)
	}
	if !strings.HasPrefix(out, "git version") {
		t.Errorf("output = %q, want git version prefix", out)
	}
}

func TestRunErrorMapping(t *testing.T) {
	requireGit(t)
	_, err := New().Run(context.Background(), t.TempDir(), "rev-parse", "--verify", "refs/heads/nope")
	if err == nil {
		t.Fatal("expected an error outside a repository")
	}
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("error is %T, want *gitx.Error", err)
	}
	if ge.ExitCode <= 0 {
		t.Errorf("ExitCode = %d, want > 0", ge.ExitCode)
	}
	if !strings.Contains(ge.Error(), "git rev-parse") {
		t.Errorf("Error() = %q, want the git command named", ge.Error())
	}
}

func TestRunBinaryMissing(t *testing.T) {
	g := &Git{path: "definitely-not-a-git-binary"}
	_, err := g.Run(context.Background(), "", "version")
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("error is %T, want *gitx.Error", err)
	}
	if ge.ExitCode != -1 || ge.Err == nil {
		t.Errorf("ExitCode = %d, Err = %v; want -1 and a cause", ge.ExitCode, ge.Err)
	}
}

func TestVersion(t *testing.T) {
	requireGit(t)
	raw, major, _, err := New().Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if major < 2 {
		t.Errorf("major = %d (raw %q), want >= 2", major, raw)
	}
}

// TestRunRawPreservesWhitespace is the guard behind RunRaw's reason for
// existing: the bytes git wrote are the bytes the caller gets, and Run and
// RunEnv still trim. It is written against `git config`, whose values carry a
// leading or trailing space through a file the same way a filename does, so
// the assertion needs no repository and no unusual filename.
func TestRunRawPreservesWhitespace(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	g := New()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "raw.config")
	for _, kv := range [][2]string{{"raw.leading", " leading.txt"}, {"raw.trailing", "trailing.txt "}} {
		if _, err := g.Run(ctx, dir, "config", "-f", cfg, kv[0], kv[1]); err != nil {
			t.Fatalf("config %s: %v", kv[0], err)
		}
	}

	raw, err := g.RunRaw(ctx, dir, "config", "-f", cfg, "--get", "raw.leading")
	if err != nil {
		t.Fatalf("RunRaw(--get raw.leading): %v", err)
	}
	if got, want := string(raw), " leading.txt\n"; got != want {
		t.Errorf("RunRaw = %q, want %q", got, want)
	}

	// -z terminates with NUL rather than newline, which is the shape
	// `ls-files -z` produces: the terminator has to survive too, or the split
	// that follows sees one row where git wrote one row and a tail.
	rawz, err := g.RunRaw(ctx, dir, "config", "-f", cfg, "-z", "--get", "raw.trailing")
	if err != nil {
		t.Fatalf("RunRaw(-z --get raw.trailing): %v", err)
	}
	if got, want := string(rawz), "trailing.txt \x00"; got != want {
		t.Errorf("RunRaw(-z) = %q, want %q", got, want)
	}

	// Run and RunEnv keep the trim every other caller depends on.
	trimmed, err := g.Run(ctx, dir, "config", "-f", cfg, "--get", "raw.leading")
	if err != nil {
		t.Fatalf("Run(--get raw.leading): %v", err)
	}
	if got, want := trimmed, "leading.txt"; got != want {
		t.Errorf("Run = %q, want %q", got, want)
	}
	env, err := g.RunEnv(ctx, dir, []string{"GIT_TERMINAL_PROMPT=0"},
		"config", "-f", cfg, "--get", "raw.trailing")
	if err != nil {
		t.Fatalf("RunEnv(--get raw.trailing): %v", err)
	}
	if got, want := env, "trailing.txt"; got != want {
		t.Errorf("RunEnv = %q, want %q", got, want)
	}
}

func TestRunRawErrorMapping(t *testing.T) {
	requireGit(t)
	out, err := New().RunRaw(context.Background(), t.TempDir(), "rev-parse", "--verify", "refs/heads/nope")
	if err == nil {
		t.Fatal("expected an error outside a repository")
	}
	if out != nil {
		t.Errorf("stdout = %q, want nil on failure", out)
	}
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("error is %T, want *gitx.Error", err)
	}
	if ge.ExitCode <= 0 {
		t.Errorf("ExitCode = %d, want > 0", ge.ExitCode)
	}
}
