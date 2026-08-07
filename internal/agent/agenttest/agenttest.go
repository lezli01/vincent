// Package agenttest builds the fakeagent test binary for adapter and
// task-run tests. It is imported only from _test files.
package agenttest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// buildOnce compiles fakeagent at most once per test process. The binary
// lands in a process-lifetime temp dir (the OS reaps it; per-test cleanup
// would break later tests in the same process).
var buildOnce = sync.OnceValues(build)

// BuildFakeAgent returns the path to a compiled cmd/fakeagent binary,
// failing (or skipping, when go itself is unavailable) the test otherwise.
func BuildFakeAgent(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	path, err := buildOnce()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}
	return path
}

func build() (string, error) {
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(self)))) // internal/agent/agenttest → repo root
	dir, err := os.MkdirTemp("", "vincent-fakeagent-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "fakeagent")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/fakeagent")
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
