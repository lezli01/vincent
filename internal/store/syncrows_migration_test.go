package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestMigration0052DropsNotGitHubRows proves task 142.7's migration: a
// project resolving to none has no sync row, so every 'not_github' row
// written before it is deleted, and a row carrying any other reason — a
// real attempt's outcome — survives untouched.
func TestMigration0052DropsNotGitHubRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 51)
	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		ts := formatTime(time.Now())
		if _, err := db.Exec(`INSERT INTO projects (id, name, path, default_branch, created_at, updated_at)
			VALUES (1, 'gone', '/a', 'main', ?, ?), (2, 'kept', '/b', 'main', ?, ?), (3, 'fine', '/c', 'main', ?, ?)`,
			ts, ts, ts, ts, ts, ts); err != nil {
			t.Fatalf("seed projects: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO issue_sync_state (project_id, provider, ok, reason) VALUES
			(1, 'github', 0, 'not_github'), (2, 'github', 0, 'auth'), (3, 'github', 1, '')`); err != nil {
			t.Fatalf("seed sync rows: %v", err)
		}
	}()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating to 0052): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	if _, err := s.GetIssueSyncState(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("not_github row after migrate: err = %v, want ErrNotFound", err)
	}
	if st, err := s.GetIssueSyncState(ctx, 2); err != nil || st.Reason != "auth" || st.OK {
		t.Errorf("auth row after migrate = %+v, %v", st, err)
	}
	if st, err := s.GetIssueSyncState(ctx, 3); err != nil || !st.OK {
		t.Errorf("ok row after migrate = %+v, %v", st, err)
	}
}
