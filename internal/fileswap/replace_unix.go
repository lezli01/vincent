//go:build unix

package fileswap

import "os"

// Replace puts tmp in place of path with mode perm, completing the atomic
// write the caller started (workflow.WriteFile, config.WriteFile).
//
// On unix this is one attempt and nothing else: a rename operates on the name,
// not on the file, so another process holding the target open — the daemon's
// own reload, an editor, a backup — cannot stand in the way of replacing it.
// Windows is the platform where that is not true; see replace_windows.go.
func Replace(tmp, path string, perm os.FileMode) error {
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
