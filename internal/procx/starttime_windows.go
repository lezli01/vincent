//go:build windows

package procx

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// StartTime reports when the process with the given PID started — the legacy
// input to the crash-recovery guard against PID reuse (spec §12.4), still
// used for rows journaled without an Identity. Returns ErrProcessGone when no
// such process exists.
func StartTime(pid int) (time.Time, error) {
	creation, err := creationTime(pid)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, creation.Nanoseconds()), nil
}

// creationTime reads a process's creation FILETIME, mapping a vanished PID
// onto ErrProcessGone. StartTime converts it to an instant; Identity keeps the
// raw 100 ns unit.
func creationTime(pid int) (windows.Filetime, error) {
	var creation, exit, kernel, user windows.Filetime
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return creation, ErrProcessGone
		}
		return creation, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return creation, fmt.Errorf("process times of %d: %w", pid, err)
	}
	return creation, nil
}
