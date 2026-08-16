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
	// keeps one implementation for every unix.
	bsize := uint64(st.Bsize) //nolint:gosec // a block size is never negative
	return st.Bavail * bsize, st.Blocks * bsize, nil
}
