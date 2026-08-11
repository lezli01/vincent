//go:build !windows

package service

import (
	"fmt"
	"os/exec"
	"strings"
)

// combined runs cmd and folds its output into any error — launchctl and
// systemctl put their diagnosis on stdout as often as stderr, and losing it
// turns a fixable problem into "exit status 1".
//
// The command is built by the caller rather than named here: macOS only ever
// shells out to launchctl, while Linux drives both systemctl and loginctl, so
// a single "run this tool by name" helper would have a constant argument on
// one platform and a varying one on the other.
func combined(cmd *exec.Cmd, label string) (string, error) {
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("%s: %w: %s", label, err, text)
		}
		return text, fmt.Errorf("%s: %w", label, err)
	}
	return text, nil
}
