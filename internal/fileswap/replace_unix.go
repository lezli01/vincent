//go:build unix

package workflow

import "os"

// replaceFile puts tmp in place of path with mode perm, completing the atomic
// write WriteFile started.
//
// On unix this is one attempt and nothing else: a rename operates on the name,
// not on the file, so another process holding the target open — the registry's
// own reload, an editor, a backup — cannot stand in the way of replacing it.
// Windows is the platform where that is not true; see replace_windows.go.
func replaceFile(tmp, path string, perm os.FileMode) error {
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
