//go:build windows

package daemon

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DetachConsole releases the console Windows allocated for this process, and
// returns one word naming what it did — logged on the startup line, so the next
// stray window at logon is diagnosable from the log rather than from a
// screenshot.
//
// It exists for the Scheduled Task registration. A task with an
// `InteractiveToken` principal runs its action on the user's own desktop, and
// no setting in a task definition suppresses a window (`<Hidden>` governs
// whether the *task* is listed in Task Scheduler, not whether its process draws
// anything), so a console-subsystem binary like this one is handed a console at
// every logon. That window is not clutter: closing a console sends
// CTRL_CLOSE_EVENT to every process attached to it, so it is an unlabelled kill
// switch for the daemon.
//
// T4.20 hid the window with ShowWindow(SW_HIDE) and T4.21 replaced that with
// FreeConsole, because hiding lost a race it cannot win. On Windows 11 the
// default terminal is Windows Terminal, so the console conhost created is
// handed off to it: the handoff replaces the console window, and Windows
// Terminal's cold start at logon takes far longer than the daemon takes to
// reach this line. The hide therefore applied to a window that the handoff then
// superseded, and the surviving window — an ordinary Windows Terminal tab in
// another process, which no in-process ShowWindow can reach — was still on the
// desktop after a reboot. Verified on the reported machine: the daemon's own
// console tab, visible, titled `C:\windows\system32\cmd.exe`, because npm's
// `codex.cmd` shim runs `title %COMSPEC%` and the agent probes inherit this
// console.
//
// Releasing the console is not a race, because it is not a property of a
// window. The daemon stops being a console client; the last client leaving ends
// the console session, so conhost exits and whatever terminal was hosting it
// closes the window — including a handoff that had not finished yet. What
// remains is one flash at logon, in the window between the scheduler creating
// the process and this call, which no in-process fix can remove: only the
// creator of a process can pass CREATE_NO_WINDOW, and here the creator is the
// scheduler.
//
// The daemon keeps the console when it is not the only process attached to it,
// which is what keeps `--hide-console` from being a foot-gun: typed by hand in
// a shell, releasing the console would detach the daemon from the terminal
// reporting its output.
func DetachConsole() string {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return "none" // started detached, or stdio is redirected
	}
	if !ownsConsole() {
		return "shared"
	}
	// Ordering matters: the standard handles are console handles until this
	// call replaces them, and FreeConsole would leave them dangling.
	if !redirectStdioToNull() {
		return "kept" // no null device: a live console beats a dead stderr
	}
	if ok, _, _ := procFreeConsole.Call(); ok == 0 {
		return "attached"
	}
	return "detached"
}

// redirectStdioToNull points this process's standard handles at the null device.
//
// Two things break the moment FreeConsole invalidates them, and both are why
// T4.20 chose to hide the window rather than release it: foreground logging
// writes stderr and the log file through one io.MultiWriter, which stops at the
// first write error and would silently take the *file* half of every record
// with it; and every child process inherits these handles, so a git call or an
// agent step would be handed a dead console. Pointed at NUL, both keep working
// and write nothing — the log file is the record a background daemon has.
func redirectStdioToNull() bool {
	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	h := windows.Handle(f.Fd())
	for _, std := range []uint32{
		windows.STD_INPUT_HANDLE, windows.STD_OUTPUT_HANDLE, windows.STD_ERROR_HANDLE,
	} {
		// SetStdHandle is what a child process inherits; assigning the os
		// package's files is what this process's own writes go through. Both
		// are needed, and neither implies the other.
		_ = windows.SetStdHandle(std, h)
	}
	os.Stdin, os.Stdout, os.Stderr = f, f, f
	return true
}

// ownsConsole reports whether this process is the only one attached to its
// console, which is how a console the OS created *for* us is told from one we
// inherited from a terminal that was already there.
//
// A failed call returns 0 and reports "not ours", which is the safe direction.
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
// non-GUI surface: GetConsoleWindow is the console's *window* rather than its
// buffer, and neither GetConsoleProcessList nor FreeConsole has a wrapper.
var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetConsoleWindow      = modkernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList = modkernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = modkernel32.NewProc("FreeConsole")
)
