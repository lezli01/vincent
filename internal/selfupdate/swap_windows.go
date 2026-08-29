//go:build windows

package selfupdate

import (
	"fmt"
	"os"
)

// leftoverSuffix names the file a swap renames the running binary to. It is a
// constant because two functions in this file have to agree on it: replace
// writes it and CleanLeftovers removes it on the next start.
const leftoverSuffix = ".vincent-old"

// replace moves staged over dest, renaming the running executable aside
// first.
//
// Windows refuses to overwrite or delete a file that is mapped as a running
// image, but it does allow *renaming* one — the handle follows the file, and
// the running process keeps executing from it under its new name. So: move the
// old binary to dest+leftoverSuffix, move the new one into place, and leave
// the rename-aside file for CleanLeftovers to delete on the next start, when
// nothing has it open.
//
// If the second rename fails, the first is undone. The contract is the same as
// the POSIX path's: either the new binary is in place, or the old one is where
// it was, byte-identical.
func replace(dest, staged string) error {
	leftover := dest + leftoverSuffix
	// A leftover from a previous update that never got cleaned would make the
	// rename-aside fail; it belongs to a process that has since exited, so
	// removing it here is safe and the miss is ignored.
	_ = os.Remove(leftover)
	movedAside := false
	if err := os.Rename(dest, leftover); err == nil {
		movedAside = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("move %s aside: %w", dest, err)
	}
	if err := os.Rename(staged, dest); err != nil {
		if movedAside {
			// Roll back: put the original back where it was. If this fails
			// too the binary is at `leftover` and the error says so, which is
			// recoverable by hand and is the honest report.
			if rerr := os.Rename(leftover, dest); rerr != nil {
				return fmt.Errorf("replace %s: %w (the original is at %s)", dest, err, leftover)
			}
		}
		return fmt.Errorf("replace %s: %w", dest, err)
	}
	return nil
}

// CleanLeftovers deletes the rename-aside file a previous swap left behind.
// It is called on daemon start, when the old image is no longer running and
// the delete succeeds. Failure is ignored: a leftover file is litter, not a
// fault, and refusing to start over one would be far worse than keeping it.
func CleanLeftovers(exe string) {
	if exe == "" {
		return
	}
	_ = os.Remove(exe + leftoverSuffix)
}
