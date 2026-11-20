package fileswap

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile writes data to path atomically: into a temporary file beside it,
// then Replace. A reader — or a crash — sees the old file or the new one,
// never half of either. `vincent chat export -o` and the TUI's export form
// write through it (task 159), because a half-written export is worse than
// none.
//
// The temporary file is created in path's own directory so the rename never
// crosses a filesystem, and it is removed on any failure.
func WriteFile(path string, data []byte, perm os.FileMode) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = Replace(tmp, path, perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
