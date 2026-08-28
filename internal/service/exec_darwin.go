//go:build darwin

package service

import (
	"context"
	"os/exec"
	"strings"
)

// launchctl runs the one tool the macOS backend drives.
func launchctl(ctx context.Context, args ...string) (string, error) {
	return combined(
		// G204: fixed tool name, arguments built by this package. No shell.
		exec.CommandContext(ctx, "launchctl", args...), //nolint:gosec // G204: see above
		"launchctl "+strings.Join(args, " "),
	)
}
