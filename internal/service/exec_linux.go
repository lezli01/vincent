//go:build linux

package service

import (
	"context"
	"os/exec"
	"strings"
)

// run drives the two tools the Linux backend needs: systemctl for the unit
// and loginctl for lingering.
func run(ctx context.Context, name string, args ...string) (string, error) {
	return combined(
		exec.CommandContext(ctx, name, args...), //nolint:gosec // fixed tool names, arguments built here
		name+" "+strings.Join(args, " "),
	)
}
