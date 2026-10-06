//go:build darwin

package procx

// sZomb is SZOMB from <sys/proc.h>: the p_stat of a process that has exited
// and waits for its parent to reap it. x/sys/unix does not export it.
const sZomb = 5

// zombie reports whether pid has terminated but not yet been reaped. Returns
// ErrProcessGone when no such process exists.
func zombie(pid int) (bool, error) {
	kp, err := kinfoProc(pid)
	if err != nil {
		return false, err
	}
	return kp.Proc.P_stat == sZomb, nil
}
