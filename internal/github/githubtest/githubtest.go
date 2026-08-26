// Package githubtest builds the fakegh test binary for GitHub tests. It is
// imported only from _test files, and exists for the reason agenttest does:
// the `gh` leg must be exercised on Windows, macOS and Linux without a real
// `gh` and without the network.
package githubtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// buildOnce compiles fakegh at most once per test process. The binary lands
// in a process-lifetime temp dir (the OS reaps it; per-test cleanup would
// break later tests in the same process).
var buildOnce = sync.OnceValues(build)

// BuildFakeGH returns the path to a compiled cmd/fakegh binary, failing (or
// skipping, when go itself is unavailable) the test otherwise.
func BuildFakeGH(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	path, err := buildOnce()
	if err != nil {
		t.Fatalf("build fakegh: %v", err)
	}
	return path
}

func build() (string, error) {
	_, self, _, _ := runtime.Caller(0)
	// internal/github/githubtest → repo root
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(self))))
	dir, err := os.MkdirTemp("", "vincent-fakegh-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "gh")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/fakegh")
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", &buildError{output: string(b), err: err}
	}
	return out, nil
}

type buildError struct {
	output string
	err    error
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.output }

func (e *buildError) Unwrap() error { return e.err }
