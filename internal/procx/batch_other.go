//go:build !windows

package procx

import "os/exec"

// BatchCmdLine is Windows-only: no other platform runs a script through
// cmd.exe. It exists so callers stay platform-agnostic.
func BatchCmdLine(*exec.Cmd) {}
