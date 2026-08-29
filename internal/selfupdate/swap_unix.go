//go:build !windows

package selfupdate

import (
	"fmt"
	"os"
)

// replace moves staged over dest.
//
// POSIX rename over a running executable is safe: the kernel holds the open
// inode, the running process keeps executing the old image, and the next exec
// picks up the new one. There is nothing to rename aside and nothing to clean
// up later — that whole dance is Windows-only.
//
// The rollback is real rather than theoretical: rename can fail on a
// read-only mount or an immutable file, and it fails *before* dest changes,
// so "leave the old binary byte-identical" is satisfied by returning.
func replace(dest, staged string) error {
	if err := os.Rename(staged, dest); err != nil {
		return fmt.Errorf("replace %s: %w", dest, err)
	}
	return nil
}

// CleanLeftovers is a no-op off Windows: no leftover is ever created.
func CleanLeftovers(string) {}
