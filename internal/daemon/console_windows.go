//go:build windows

package daemon

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// HideConsole hides the console window Windows allocated for this process.
//
// It exists for the Scheduled Task registration (T4.20). A task with an
// `InteractiveToken` principal runs its action in the user's own desktop
// session, and there is no setting in a task definition that suppresses a
// window: `<Hidden>` controls whether the *task* is listed in Task Scheduler,
// not whether its process draws anything. So a console-subsystem binary like
// this one got a console window at every logon — a stray terminal on the
// desktop that stopped the daemon when it was closed, because closing a console
// sends CTRL_CLOSE_EVENT to every process attached to it. The window was not
// cosmetic: it was a kill switch that looked like clutter.
//
// Only the creator of a process can suppress its console (CREATE_NO_WINDOW,
// which is what internal/procx passes for every agent and command step), and
// here the creator is the scheduler. Hiding it from the inside is therefore the
// only move available, and it is why the daemon — not the installer — carries
// this code.
//
// The console is hidden rather than released with FreeConsole because
// foreground logging writes to stderr as well as the log file through one
// io.MultiWriter, which stops at the first write error: releasing the console
// would invalidate stderr's handle and take the *file* half of every log record
// with it. A hidden console keeps both writers valid, and §12.4's shutdown
// unaffected.
func HideConsole() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return // no console: started detached, or output is redirected
	}
	if !ownsConsole() {
		return
	}
	_, _, _ = procShowWindow.Call(hwnd, swHide)
}

// ownsConsole reports whether this process is the only one attached to its
// console, which is how a console the OS created *for* us is told from one we
// inherited from a terminal that was already there.
//
// The guard is what keeps `--hide-console` from being a foot-gun: run by hand
// in a shell, the shell is attached too, and hiding that window would take the
// user's terminal with it. A failed call returns 0 and hides nothing, which is
// the safe direction.
func ownsConsole() bool {
	// GetConsoleProcessList returns the number of attached processes, and
	// returns the *required* count rather than truncating when the buffer is
	// too small — so a two-element buffer distinguishes "just us" from "more
	// than us" without asking how many more.
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}

// These three are absent from golang.org/x/sys/windows, which covers the
// non-GUI surface; GetConsoleWindow and ShowWindow are the console's window,
// not its buffer.
var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	moduser32   = windows.NewLazySystemDLL("user32.dll")

	procGetConsoleWindow      = modkernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList = modkernel32.NewProc("GetConsoleProcessList")
	procShowWindow            = moduser32.NewProc("ShowWindow")
)

const swHide = 0
