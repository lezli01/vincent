//go:build !windows

package procx

import "errors"

// exited is Exited on POSIX: the identity read back no longer matching, or
// the PID gone outright. A zombie still reads as running, which is right for
// the caller it serves — a detached daemon is reaped by init at once.
func exited(pid int, ident string) (bool, error) {
	cur, err := Identity(pid)
	if errors.Is(err, ErrProcessGone) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return cur != ident, nil
}
