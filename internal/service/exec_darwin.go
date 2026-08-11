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
		exec.CommandContext(ctx, "launchctl", args...),
		"launchctl "+strings.Join(args, " "),
	)
}
