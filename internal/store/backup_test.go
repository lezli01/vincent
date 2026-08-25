package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBackupToProducesASelfContainedFile pins the property the whole feature
// rests on: the copy is one file that opens on its own. A `cp vincent.db`
// under WAL is missing everything since the last checkpoint, and the three
// files copied separately are not a consistent set.
func TestBackupToProducesASelfContainedFile(t *testing.T) {
	s := openTest(t)
	if err := s.AppendEvent(t.Context(), &Event{Type: EventDaemonShuttingDown}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "copy.db")
	if err := s.BackupTo(t.Context(), dst); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	for _, sidecar := range []string{dst + "-wal", dst + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			t.Errorf("VACUUM INTO left %s beside the copy", sidecar)
		}
	}

	copied, err := Open(dst)
	if err != nil {
		t.Fatalf("the copy does not open: %v", err)
	}
	t.Cleanup(func() { _ = copied.Close() })
	if verdict, err := copied.IntegrityCheck(t.Context()); err != nil || verdict != "ok" {
		t.Fatalf("integrity_check = %q (%v), want ok", verdict, err)
	}
	if got, want := schemaVersion(t, copied), schemaVersion(t, s); got != want {
		t.Errorf("copied schema version = %d, want %d", got, want)
	}
	events, err := copied.ListEvents(t.Context(), EventFilter{Limit: 10})
	if err != nil || len(events) != 1 {
		t.Fatalf("copied events = %d (%v), want the one that was committed", len(events), err)
	}
}

// TestBackupToRefusesAnExistingDestination is why the daemon stages the copy
// under a fresh name: SQLite will not overwrite, so a caller that reused a
// path would fail at the last moment instead of the first.
func TestBackupToRefusesAnExistingDestination(t *testing.T) {
	s := openTest(t)
	dst := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(dst, []byte("in the way"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := s.BackupTo(t.Context(), dst); err == nil {
		t.Fatal("BackupTo over an existing file = nil, want an error")
	}
	body, err := os.ReadFile(dst)
	if err != nil || string(body) != "in the way" {
		t.Fatalf("the existing file was modified: %q (%v)", body, err)
	}
}
