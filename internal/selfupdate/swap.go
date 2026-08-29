package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
)

// Swap replaces the file at dest with data, atomically where the platform
// allows it.
//
// The staging file is written **in dest's own directory**, never in the
// system temp dir: os.Rename is only atomic within a filesystem, and /tmp is
// routinely a different one from /usr/local/bin or a user's home. A cross-
// device rename fails, and the fallback — copy over the target in place — is
// exactly the non-atomic window this is avoiding.
//
// Mode bits are read off the file being replaced and applied to the
// replacement, because a binary that comes back 0644 stops being executable
// and the user finds out at the worst moment.
//
// On failure after the rename, the original is put back. The contract is the
// one the issue asks for: either the new binary is in place, or the old one is
// byte-identical to what it was.
func Swap(dest string, data []byte) (err error) {
	dir := filepath.Dir(dest)
	mode := currentMode(dest)

	staged, err := os.CreateTemp(dir, ".vincent-update-*")
	if err != nil {
		return fmt.Errorf("stage update in %s: %w", dir, err)
	}
	stagedPath := staged.Name()
	// Until the rename succeeds, the staging file is litter; after it, the
	// path no longer exists and the remove is a harmless miss.
	defer func() {
		_ = os.Remove(stagedPath)
	}()
	if _, werr := staged.Write(data); werr != nil {
		_ = staged.Close()
		return fmt.Errorf("write staged update: %w", werr)
	}
	// Sync before the rename: a crash between the two must not leave a
	// correctly-named file with a partially-flushed body, which would pass
	// every check that already ran and fail to execute.
	if serr := staged.Sync(); serr != nil {
		_ = staged.Close()
		return fmt.Errorf("flush staged update: %w", serr)
	}
	if cerr := staged.Close(); cerr != nil {
		return fmt.Errorf("close staged update: %w", cerr)
	}
	if cerr := os.Chmod(stagedPath, mode); cerr != nil {
		return fmt.Errorf("set mode on staged update: %w", cerr)
	}
	if qerr := clearQuarantine(stagedPath); qerr != nil {
		return qerr
	}
	return replace(dest, stagedPath)
}

// replace and CleanLeftovers are the platform-split half of the swap, in
// swap_unix.go and swap_windows.go. The split exists because Windows cannot
// overwrite a running image and every other platform can — see those files.
