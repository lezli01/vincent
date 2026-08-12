//go:build windows

package procx

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// NoWindow keeps a child process off the desktop.
//
// It is the reason a console-less daemon can spawn console binaries silently:
// CreateProcess gives a console-subsystem child a console of its own — and a
// console is a window — when the parent has none, which is the daemon's normal
// state (a detached `daemon start`, or the Windows Scheduled Task once the
// daemon has released its own console; T4.21). CREATE_NO_WINDOW is the
// creator's only chance to prevent that: a process cannot suppress a console it
// has already been given a window for.
//
// Exported for the short-lived probes in internal/agent, which want the flag
// without a Job object — they capture output and exit, so there is no process
// tree to contain (T3.8 covered step processes and git and missed the probes).
func NoWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
