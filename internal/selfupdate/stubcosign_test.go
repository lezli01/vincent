package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// A stub cosign whose exit code the test chooses.
//
// It is a compiled Go program rather than a shell script for the reason
// everything in this repository is: there is no portable shell. A `.sh` never
// runs on Windows and CreateProcess cannot execute a `.bat` directly, so a
// script would make the signature tests Unix-only — and Windows is exactly
// where the swap path most needs covering. This mirrors
// internal/agent/agenttest, which compiles cmd/fakeagent once per test
// process for the same reason.
var (
	stubOnce sync.Once
	stubDir  string
	stubErr  error
)

const stubSource = `package main

import "os"

// A stand-in for cosign: it ignores its arguments and exits with the code in
// VINCENT_STUB_COSIGN_EXIT, so a test can choose "verified" or "refused"
// without a Fulcio root or a Rekor lookup.
func main() {
	if os.Getenv("VINCENT_STUB_COSIGN_EXIT") == "1" {
		os.Stderr.WriteString("error: verifying blob: no matching signatures\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("Verified OK\n")
}
`

// stubCosign returns a path to the stub, configured to exit with code.
//
// The exit code rides an environment variable rather than a flag because the
// production call site controls cosign's argv completely — a stub that needed
// an extra argument would be testing a call this package never makes.
func stubCosign(t *testing.T, exit int) string {
	t.Helper()
	stubOnce.Do(buildStub)
	if stubErr != nil {
		t.Skipf("stub cosign not built (no Go toolchain?): %v", stubErr)
	}
	// Set on the test process, which is what exec.Command inherits.
	if exit == 0 {
		t.Setenv("VINCENT_STUB_COSIGN_EXIT", "0")
	} else {
		t.Setenv("VINCENT_STUB_COSIGN_EXIT", "1")
	}
	name := "cosign"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(stubDir, name)
}

func buildStub() {
	dir, err := os.MkdirTemp("", "vincent-stub-cosign-")
	if err != nil {
		stubErr = err
		return
	}
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(stubSource), 0o600); err != nil {
		stubErr = err
		return
	}
	name := "cosign"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	// Every argument is a constant or a path this function created.
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, name), src)
	cmd.Dir = dir
	// A module-less build: the stub imports only the standard library.
	cmd.Env = append(os.Environ(), "GO111MODULE=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		stubErr = wrapBuild(err, out)
		return
	}
	stubDir = dir
}

func wrapBuild(err error, out []byte) error {
	return &buildError{err: err, out: string(out)}
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return e.err.Error() + ": " + e.out }
func (e *buildError) Unwrap() error { return e.err }
