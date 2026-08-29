//go:build !darwin && !windows

package tui

import (
	"context"
	"os/exec"
)

// openURLPlatform uses xdg-open, the freedesktop.org convention every
// desktop environment implements. A machine without it — a server, a bare
// container — has nothing to open a browser with, and says so rather than
// guessing at firefox.
func openURLPlatform(ctx context.Context, url string) error {
	if _, err := exec.LookPath("xdg-open"); err != nil {
		return errNoOpener
	}
	// The URL is a separate argv element, never a shell string, and
	// openURLCmd has already refused every scheme but http and https.
	out, err := exec.CommandContext(ctx, "xdg-open", url).CombinedOutput() //nolint:gosec // a validated http(s) URL, passed as argv
	if err != nil {
		return openerError(err, out)
	}
	return nil
}
