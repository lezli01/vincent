//go:build linux

package procx

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// identityScheme versions the Linux token format. Bumping it retires every
// token already journaled, which is the safe direction: an unrecognised token
// compares unequal and nothing is killed.
const identityScheme = "linux1"

// identity is Identity on Linux: the raw start-tick count of the process,
// qualified by the boot it was measured in and by the PID that holds it.
//
// The PID is part of the token because the tick count alone does not name a
// process: USER_HZ is 100, so every process started within the same 10 ms
// window carries the same count, and two of them alive at once would answer
// with the same bytes. Pairing it with the PID is the kernel's own notion of
// a unique process — the pid/start-time pair — and costs the guard nothing,
// since a token is only ever compared against one read back for that same PID.
func identity(pid int) (string, error) {
	ticks, err := startTicks(pid)
	if err != nil {
		return "", err
	}
	boot, err := bootID()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%s:%s:%d", identityScheme, boot, ticks, pid), nil
}

// bootID reads the kernel's random per-boot identifier. It is what makes a
// reboot a certain mismatch: start ticks are counted from boot, so after a
// restart a fresh process can legitimately carry the very tick count a
// journaled one had, and only the boot id tells the two boots apart.
func bootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("read boot id: %w", err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", errors.New("boot id is empty")
	}
	return id, nil
}
