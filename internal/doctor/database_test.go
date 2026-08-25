package doctor

import (
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// TestDatabaseStaysUnknownWithoutADaemon is the ownership invariant as a test,
// with the temptation put right in front of the code: a real, populated
// database sits at the path the report names, and every figure still comes
// back unset.
//
// "Only the daemon opens SQLite" is not a convenience — a second process
// reading a WAL-mode database out from under its single writer is exactly what
// the invariant exists to prevent — so the client-side report says unknown
// rather than answering from the file it can plainly see (task 029, task 005
// decision 3).
func TestDatabaseStaysUnknownWithoutADaemon(t *testing.T) {
	d := dirs(t)
	path := filepath.Join(d.Data, "vincent.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for range 3 {
		if err := st.AppendEvent(t.Context(), &store.Event{Type: "daemon.started"}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	rep := Compose(t.Context(), Options{Dirs: d, LogPath: filepath.Join(d.Data, "daemon.log")})
	db := rep.Database
	if db.Known {
		t.Fatalf("database.known = true with no daemon: %+v", db)
	}
	if db.Path != path {
		t.Errorf("database.path = %q, want %q — the path is derivable, the contents are not",
			db.Path, path)
	}
	if db.SizeBytes != 0 || db.WALBytes != 0 || db.SHMBytes != 0 || db.TotalBytes != 0 {
		t.Errorf("byte figures were read from the file behind the daemon's back: %+v", db)
	}
	if db.TableRows != nil {
		t.Errorf("table_rows = %v, want nil — a client never counts rows", db.TableRows)
	}
	if db.OldestEventAt != nil {
		t.Errorf("oldest_event_at = %v, want nil", *db.OldestEventAt)
	}
	if db.WorkflowSnapshotBytes != 0 {
		t.Errorf("workflow_snapshot_bytes = %d, want 0", db.WorkflowSnapshotBytes)
	}
	// The migration high-water mark is the one database row a client may
	// answer, because it comes from the binary rather than from the file.
	if db.NewestMigration != store.NewestMigration() {
		t.Errorf("newest_migration = %d, want %d", db.NewestMigration, store.NewestMigration())
	}
}
