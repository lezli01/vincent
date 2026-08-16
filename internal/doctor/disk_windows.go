package doctor

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// diskUsage reports free and total bytes on the volume holding path.
//
// GetDiskFreeSpaceEx's first out-parameter is the space available *to the
// calling user*, which is what per-user quotas make different from the
// volume's free space — and the daemon runs as the invoking user (§16), so
// that is the number worth reporting.
func diskUsage(path string) (free, total uint64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, fmt.Errorf("disk free %s: %w", path, err)
	}
	var availToCaller, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &availToCaller, &totalBytes, &totalFree); err != nil {
		return 0, 0, fmt.Errorf("disk free %s: %w", path, err)
	}
	return availToCaller, totalBytes, nil
}
