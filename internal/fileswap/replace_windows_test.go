package fileswap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestReplaceDoesNotRetryAnErrorNobodyWillClear: the wait is for a handle
// that is about to close, so an error that says something else is returned on
// the first attempt rather than after the window.
func TestReplaceDoesNotRetryAnErrorNobodyWillClear(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	err := Replace(filepath.Join(dir, "absent.vincent-tmp"), filepath.Join(dir, "absent.yaml"), 0o600)
	if err == nil {
		t.Fatal("Replace succeeded with no source file")
	}
	if waited := time.Since(start); waited >= replaceWaitFor {
		t.Errorf("waited %s on an error that is not contention", waited)
	}
}

func TestContended(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"sharing violation", windows.ERROR_SHARING_VIOLATION, true},
		{"lock violation", windows.ERROR_LOCK_VIOLATION, true},
		{"access denied", windows.ERROR_ACCESS_DENIED, true},
		{"wrapped in a LinkError", &os.LinkError{Err: windows.ERROR_SHARING_VIOLATION}, true},
		{"file not found", windows.ERROR_FILE_NOT_FOUND, false},
		{"disk full", windows.ERROR_DISK_FULL, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := contended(tc.err); got != tc.want {
				t.Errorf("contended(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
