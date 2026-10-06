//go:build windows

package procx

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// exited is Exited on Windows. Identity alone cannot answer it: a process
// object outlives the process for as long as anyone holds a handle to it, and
// OpenProcess and GetProcessTimes keep succeeding on it with the same
// creation time. The process object is signaled once the process has
// terminated — after the kernel has closed every handle it held, which is
// what a caller about to delete its files needs — so after the identity
// guard this asks the object itself. The handle is opened first: while it is
// held the PID cannot be reused, so the identity read next is this object's.
func exited(pid int, ident string) (bool, error) {
	//nolint:gosec // G115: a Windows PID is a DWORD widened to int; this narrows it back
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return true, nil // no such process
		}
		return false, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	cur, err := Identity(pid)
	if errors.Is(err, ErrProcessGone) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if cur != ident {
		return true, nil
	}
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false, fmt.Errorf("wait on process %d: %w", pid, err)
	}
	return ev == windows.WAIT_OBJECT_0, nil
}
