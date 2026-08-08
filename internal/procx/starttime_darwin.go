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
		// The kernel answers ESRCH or ENOENT for a PID that does not exist,
		// depending on version.
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
			return time.Time{}, ErrProcessGone
		}
		return time.Time{}, fmt.Errorf("sysctl kern.proc.pid %d: %w", pid, err)
	}
	tv := kp.Proc.P_starttime
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), nil
}
