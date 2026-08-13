//go:build mage

// Build targets for vincent. Run via `go run mage.go <target> [<target>...]`
// (zero-install) or `go tool mage <target>`; list targets with -l.
package main

import (
	"fmt"
	"path/filepath"
	"runtime"
	"time"

	"github.com/magefile/mage/sh"
)

const versionPkg = "github.com/lezli01/vincent/internal/version"

// Build compiles the vincent binary into bin/ with build info injected.
func Build() error {
	out := filepath.Join("bin", "vincent")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	return sh.RunV("go", "build", "-trimpath", "-ldflags", ldflags(), "-o", out, "./cmd/vincent")
}

// Test runs all tests.
func Test() error {
	return sh.RunV("go", "test", "./...")
}

// TestRace runs all tests with the race detector (needs a C toolchain; CI has one).
func TestRace() error {
	return sh.RunV("go", "test", "-race", "./...")
}

// Lint runs golangci-lint, pinned via the go.mod tool directive.
func Lint() error {
	return sh.RunV("go", "tool", "golangci-lint", "run")
}

// Vuln reports known vulnerabilities reachable from this module's code, for
// every platform vincent ships on, via govulncheck pinned by the go.mod tool
// directive. Needs network access to fetch the Go vulnerability database.
//
// The GOOS sweep is not ceremony: 15 packages reach the binary only on Windows
// (golang.org/x/sys/windows/svc, modernc.org/libc/*), so a host-only run would
// report a vulnerability in one of those as unreachable. And it has to invoke
// one host-built binary with GOOS set in its environment rather than
// `GOOS=… go tool govulncheck`, which cross-builds the tool and then cannot
// execute it — the same trap `go tool golangci-lint` has (see CLAUDE.md).
func Vuln() error {
	bin, err := sh.Output("go", "tool", "-n", "govulncheck")
	if err != nil {
		return fmt.Errorf("locating govulncheck: %w", err)
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		fmt.Printf("== govulncheck GOOS=%s\n", goos)
		if err := sh.RunWithV(map[string]string{"GOOS": goos}, bin, "./..."); err != nil {
			return fmt.Errorf("govulncheck GOOS=%s: %w", goos, err)
		}
	}
	return nil
}

func ldflags() string {
	version := "dev"
	if v, err := sh.Output("git", "describe", "--tags", "--always", "--dirty"); err == nil && v != "" {
		version = v
	}
	commit, _ := sh.Output("git", "rev-parse", "--short", "HEAD")
	date := time.Now().UTC().Format("2006-01-02")
	return fmt.Sprintf("-X %[1]s.version=%[2]s -X %[1]s.commit=%[3]s -X %[1]s.date=%[4]s",
		versionPkg, version, commit, date)
}
