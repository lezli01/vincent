//go:build !windows

package procx

import "os/exec"

// NoWindow is Windows-only: nothing else puts a window on screen for a child
// process. It exists so callers stay platform-agnostic.
func NoWindow(*exec.Cmd) {}
