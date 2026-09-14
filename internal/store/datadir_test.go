package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestOpenCreatesDataDirOwnerOnly pins issue #367: the directory Open creates
// holds vincent.db — every prompt, result summary and workflow snapshot — so
// it is owner-only like every directory beside it (logs, worktrees,
// transcripts, and the same data dir when the TUI creates it first), not 0755.
func TestOpenCreatesDataDirOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only; Windows access comes from the per-user ACL (§12.2)")
	}
	dir := filepath.Join(t.TempDir(), "data")
	s, err := Open(filepath.Join(dir, "vincent.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("data dir mode = %04o, want no group or other access (0700)", perm)
	}
}
