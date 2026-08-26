//go:build !windows

package github

import "os/exec"

// hideConsole is Windows-only: nothing else puts a window on screen for a
// child process.
func hideConsole(*exec.Cmd) {}
