package tui

import (
	"context"
	"os/exec"
)

// openURLPlatform uses macOS's own `open`, which hands the URL to whatever
// the user set as their default handler.
func openURLPlatform(ctx context.Context, url string) error {
	if _, err := exec.LookPath("open"); err != nil {
		return errNoOpener
	}
	// The URL is a separate argv element, never a shell string, and
	// openURLCmd has already refused every scheme but http and https.
	out, err := exec.CommandContext(ctx, "open", url).CombinedOutput() //nolint:gosec // a validated http(s) URL, passed as argv
	if err != nil {
		return openerError(err, out)
	}
	return nil
}
