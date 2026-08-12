//go:build windows

package service

import (
	"context"
	"os/exec"
	"strings"
)

// schtasks drives the Task Scheduler CLI, which registers, starts, stops and
// removes the task. It is preferred over the scheduler's COM API for the same
// reason the POSIX backends shell out to launchctl and systemctl rather than
// linking a service library: one subprocess call per verb, and the tool's own
// diagnosis when something is rejected (W decision).
func schtasks(ctx context.Context, args ...string) (string, error) {
	return combined(
		exec.CommandContext(ctx, "schtasks.exe", args...), //nolint:gosec // fixed tool name, arguments built here
		"schtasks "+strings.Join(args, " "),
	)
}

// powershell runs one expression, used only to read a task's state as an
// invariant string (see taskRunning). -NoProfile keeps a user's profile script
// out of the answer and off the startup cost.
func powershell(ctx context.Context, expr string) (string, error) {
	return combined(
		exec.CommandContext(ctx, "powershell.exe", //nolint:gosec // fixed tool name, expression built here
			"-NoProfile", "-NonInteractive", "-Command", expr),
		"powershell "+expr,
	)
}
