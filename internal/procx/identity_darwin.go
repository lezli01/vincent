//go:build darwin

package procx

import "fmt"

// identityScheme versions the macOS token format. Bumping it retires every
// token already journaled, which is the safe direction: an unrecognised token
// compares unequal and nothing is killed.
const identityScheme = "darwin1"

// identity is Identity on macOS: the fork-time stamp the kernel keeps in
// kinfo_proc, to the microsecond. It is recorded once, so a later clock
// adjustment moves neither the journaled token nor the one read back.
func identity(pid int) (string, error) {
	kp, err := kinfoProc(pid)
	if err != nil {
		return "", err
	}
	tv := kp.Proc.P_starttime
	return fmt.Sprintf("%s:%d.%06d", identityScheme, tv.Sec, tv.Usec), nil
}
