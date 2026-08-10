//go:build windows

package gitx

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideConsole keeps git off the desktop: the daemon runs detached with no
// console of its own, so without CREATE_NO_WINDOW every git invocation — a
// diff fetch runs one per call — would flash a console window at the user
// (T3.8 finding).
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
