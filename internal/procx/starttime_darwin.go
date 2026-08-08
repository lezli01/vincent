//go:build darwin

package procx

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// StartTime reports when the process with the given PID started — the
// crash-recovery guard against PID reuse (spec §12.4): a recorded PID is
// only killed when its start time matches the journaled spawn time.
// Returns ErrProcessGone when no such process exists.
func StartTime(pid int) (time.Time, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		// A vanished PID surfaces as ESRCH or ENOENT depending on kernel
		// version — or as EIO, x/sys's mapping of the zero-length result
		// this sysctl returns for a process that no longer exists.
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EIO) {
			return time.Time{}, ErrProcessGone
		}
		return time.Time{}, fmt.Errorf("sysctl kern.proc.pid %d: %w", pid, err)
	}
	tv := kp.Proc.P_starttime
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), nil
}
