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
// qualified by the boot it was measured in.
func identity(pid int) (string, error) {
	ticks, err := startTicks(pid)
	if err != nil {
		return "", err
	}
	boot, err := bootID()
	if err != nil {
		return "", err
	}
	return identityScheme + ":" + boot + ":" + ticks, nil
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
