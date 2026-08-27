//go:build windows

package github

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideConsole keeps `gh` off the desktop, for the reason gitx.hideConsole
// keeps git off it: the daemon runs detached with no console of its own, so
// without CREATE_NO_WINDOW every `gh` invocation would flash a console window
// at the user.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
