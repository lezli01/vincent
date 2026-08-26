//go:build darwin

package procx

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// StartTime reports when the process with the given PID started — the legacy
// input to the crash-recovery guard against PID reuse (spec §12.4), still
// used for rows journaled without an Identity. Returns ErrProcessGone when no
// such process exists.
func StartTime(pid int) (time.Time, error) {
	kp, err := kinfoProc(pid)
	if err != nil {
		return time.Time{}, err
	}
	tv := kp.Proc.P_starttime
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), nil
}

// kinfoProc reads one process's kernel record, mapping a vanished PID onto
// ErrProcessGone. Both StartTime and Identity read P_starttime out of it.
func kinfoProc(pid int) (*unix.KinfoProc, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		// A vanished PID surfaces as ESRCH or ENOENT depending on kernel
		// version — or as EIO, x/sys's mapping of the zero-length result
		// this sysctl returns for a process that no longer exists.
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EIO) {
			return nil, ErrProcessGone
		}
		return nil, fmt.Errorf("sysctl kern.proc.pid %d: %w", pid, err)
	}
	return kp, nil
}
