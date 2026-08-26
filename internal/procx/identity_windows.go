//go:build windows

package procx

import "fmt"

// identityScheme versions the Windows token format. Bumping it retires every
// token already journaled, which is the safe direction: an unrecognised token
// compares unequal and nothing is killed.
const identityScheme = "windows1"

// identity is Identity on Windows: the creation FILETIME kept in its raw
// 100 ns unit rather than converted to a time.Time, so the token is whatever
// the kernel recorded and nothing else, qualified by the PID that holds it.
//
// The PID matters most here: the unit is finer than the value, because the
// system clock advances on a tick of roughly 15 ms, so processes created in
// the same tick genuinely share a creation stamp. Pairing it with the PID is
// what makes the token name one process rather than a batch of them.
func identity(pid int) (string, error) {
	ft, err := creationTime(pid)
	if err != nil {
		return "", err
	}
	raw := uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
	return fmt.Sprintf("%s:%d:%d", identityScheme, raw, pid), nil
}
