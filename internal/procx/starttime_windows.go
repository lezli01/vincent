//go:build windows

package procx

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// StartTime reports when the process with the given PID started — the
// crash-recovery guard against PID reuse (spec §12.4): a recorded PID is
// only killed when its start time matches the journaled spawn time.
// Returns ErrProcessGone when no such process exists.
func StartTime(pid int) (time.Time, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return time.Time{}, ErrProcessGone
		}
		return time.Time{}, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, fmt.Errorf("process times of %d: %w", pid, err)
	}
	return time.Unix(0, creation.Nanoseconds()), nil
}
