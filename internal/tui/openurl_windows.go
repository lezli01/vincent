package tui

import (
	"context"
	"os/exec"
)

// openURLPlatform uses the shell's own protocol handler through rundll32
// rather than `cmd /c start`. `start` is a cmd builtin, so it needs a shell,
// and cmd treats `&` inside a URL as a command separator — a compare URL
// carries a query string full of them.
func openURLPlatform(ctx context.Context, url string) error {
	exe, err := exec.LookPath("rundll32.exe")
	if err != nil {
		return errNoOpener
	}
	// The URL is a separate argv element, never a shell string, and
	// openURLCmd has already refused every scheme but http and https.
	out, err := exec.CommandContext(ctx, exe, "url.dll,FileProtocolHandler", url).CombinedOutput() //nolint:gosec // a validated http(s) URL, passed as argv
	if err != nil {
		return openerError(err, out)
	}
	return nil
}
