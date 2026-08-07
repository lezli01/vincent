package gitx

import (
	"context"
	"errors"
	"os/exec"
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
