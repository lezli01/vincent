//go:build !windows

package service

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// run executes a service-manager command and returns its combined output,
// folding that output into the error — launchctl and systemctl put their
// diagnosis on stdout as often as stderr, and losing it turns a fixable
// problem into "exit status 1".
//
// It lives in a non-Windows file because the Windows backend talks to the SCM
// through its API rather than a subprocess, and an unused helper there is
// dead code the linter is right to flag.
func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed tool names, arguments built here
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, text)
		}
		return text, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return text, nil
}
