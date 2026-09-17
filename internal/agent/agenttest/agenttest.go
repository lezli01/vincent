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
	name := "fakeagent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return compile(name, nil)
}

// linuxBuilds caches one cross-compiled fakeagent per architecture.
var linuxBuilds sync.Map // arch → func() (string, error)

// BuildFakeAgentLinux returns a cmd/fakeagent cross-compiled for linux/arch —
// the binary a containerized agent step runs inside a Linux image (task
// 062.2). arch "" means amd64; Docker Desktop on Apple Silicon runs arm64
// images, which is why it is a parameter rather than a constant. cgo is off,
// so the binary is static and needs nothing from the image but a kernel.
func BuildFakeAgentLinux(t *testing.T, arch string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	if arch == "" {
		arch = "amd64"
	}
	once, _ := linuxBuilds.LoadOrStore(arch, sync.OnceValues(func() (string, error) {
		return compile("fakeagent-linux-"+arch, []string{"GOOS=linux", "GOARCH=" + arch, "CGO_ENABLED=0"})
	}))
	path, err := once.(func() (string, error))()
	if err != nil {
		t.Fatalf("cross-compile fakeagent for linux/%s: %v", arch, err)
	}
	return path
}

// compile builds cmd/fakeagent into a fresh process-lifetime temp dir, with
// env layered over the test process's own.
func compile(name string, env []string) (string, error) {
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(self)))) // internal/agent/agenttest → repo root
	dir, err := os.MkdirTemp("", "vincent-fakeagent-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", out, "./cmd/fakeagent")
	cmd.Dir = root
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
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
