//go:build !windows

package doctor

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// diskUsage reports free and total bytes on the filesystem holding path.
//
// Bavail rather than Bfree: the blocks reserved for root are not space
// vincent can write into, and a diagnostic that reports capacity the daemon
// cannot use would be wrong in the one direction that matters — a task
// failing to write a transcript on a "healthy" disk.
func diskUsage(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	// Bsize is int64 on Linux and uint32 on Darwin; the conversion is what
	// keeps one implementation for every unix. A block size is never negative,
	// so gosec's G115 here is a false positive — and it fires on Linux only,
	// which is why the suppression is an exclusion rule in .golangci.yml rather
	// than a //nolint that nolintlint would call stale on Darwin (task 042).
	bsize := uint64(st.Bsize)
	return st.Bavail * bsize, st.Blocks * bsize, nil
}
