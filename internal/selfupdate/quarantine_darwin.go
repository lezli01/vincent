package selfupdate

import (
	"context"
	"os/exec"
	"time"
)

// clearQuarantine removes com.apple.quarantine from a freshly-written file.
//
// Gatekeeper blocks an unsigned binary carrying that attribute, which is the
// prompt docs/getting-started/installation.md already documents for a direct
// download. A swap that leaves it on produces a binary that verified perfectly
// and refuses to run.
//
// Failure is deliberately not an error. `xattr` is missing on a stripped
// system, the attribute is usually absent in the first place — Go's own
// os.CreateTemp does not set one — and refusing an otherwise-verified update
// because a cleanup step had nothing to clean would be the wrong trade.
func clearQuarantine(path string) error {
	bin, err := exec.LookPath("xattr")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// G204: bin is resolved from PATH; path is the staging file this package
	// just created in a directory it chose.
	_ = exec.CommandContext(ctx, bin, "-d", "com.apple.quarantine", path).Run() //nolint:gosec // G204: see above
	return nil
}
