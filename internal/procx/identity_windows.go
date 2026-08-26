//go:build windows

package procx

import "fmt"

// identityScheme versions the Windows token format. Bumping it retires every
// token already journaled, which is the safe direction: an unrecognised token
// compares unequal and nothing is killed.
const identityScheme = "windows1"

// identity is Identity on Windows: the creation FILETIME kept in its raw
// 100 ns unit rather than converted to a time.Time, so the token is whatever
// the kernel recorded and nothing else.
func identity(pid int) (string, error) {
	ft, err := creationTime(pid)
	if err != nil {
		return "", err
	}
	raw := uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
	return fmt.Sprintf("%s:%d", identityScheme, raw), nil
}
