//go:build darwin

package service

import (
	"context"
	"os/exec"
	"strings"
)

// launchctl runs the one tool the macOS backend drives. A variable so a test
// can record the commands a verb issues without a launchd to receive them.
var launchctl = func(ctx context.Context, args ...string) (string, error) {
	return combined(
		// G204: fixed tool name, arguments built by this package. No shell.
		exec.CommandContext(ctx, "launchctl", args...), //nolint:gosec // G204: see above
		"launchctl "+strings.Join(args, " "),
	)
}
