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
