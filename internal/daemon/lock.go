package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

const lockFile = "daemon.lock"

// LockPath returns the single-instance lock file path inside dataDir.
func LockPath(dataDir string) string { return filepath.Join(dataDir, lockFile) }

// ProbeRunning reports whether a daemon currently holds the single-instance
// lock. It is the authoritative liveness check: flock releases on process
// death, so a crashed daemon never reads as running. A missing data dir means
// no daemon ever started there.
func ProbeRunning(dataDir string) (bool, error) {
	l := flock.New(LockPath(dataDir))
	ok, err := l.TryLock()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("probe daemon lock: %w", err)
	}
	if ok {
		_ = l.Unlock()
		return false, nil
	}
	return true, nil
}
