//go:build darwin

package procx

import "fmt"

// identityScheme versions the macOS token format. Bumping it retires every
// token already journaled, which is the safe direction: an unrecognised token
// compares unequal and nothing is killed.
const identityScheme = "darwin1"

// identity is Identity on macOS: the fork-time stamp the kernel keeps in
// kinfo_proc, to the microsecond, qualified by the PID that holds it. It is
// recorded once, so a later clock adjustment moves neither the journaled token
// nor the one read back.
//
// The PID is part of the token for the reason spelled out in identity_linux.go:
// a stamp names an instant, and two processes forked inside the same one would
// otherwise share a token. A microsecond is a small window, but it is not an
// empty one, and the guard should not rest on that.
func identity(pid int) (string, error) {
	kp, err := kinfoProc(pid)
	if err != nil {
		return "", err
	}
	tv := kp.Proc.P_starttime
	return fmt.Sprintf("%s:%d.%06d:%d", identityScheme, tv.Sec, tv.Usec, pid), nil
}
