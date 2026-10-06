//go:build !windows

package procx

import "errors"

// exited is Exited on POSIX: the PID gone outright, the identity read back no
// longer matching, or the process a zombie. A zombie has exited — it has
// released every file it held, which is all the #732 wait needs — and it may
// stay one for good: a daemon whose parent never waits (a container whose
// PID 1 is no init, a supervisor that does not reap) is never collected, and
// reading it as running would fail `daemon stop`, and `--force` with it, on
// a process that is already gone (review F2 on #741).
func exited(pid int, ident string) (bool, error) {
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
	z, err := zombie(pid)
	if errors.Is(err, ErrProcessGone) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return z, nil
}
