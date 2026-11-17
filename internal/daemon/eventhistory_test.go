package daemon

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/backup"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestStartRestoredHistory is issue #1001: the daemon's first start after
// `vincent daemon restore` begins a new event history and consumes the
// marker, and a start without one leaves the generation alone.
func TestStartRestoredHistory(t *testing.T) {
	dir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dir, "vincent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := t.Context()

	if err := startRestoredHistory(ctx, st, dir, logger); err != nil {
		t.Fatalf("start without a marker: %v", err)
	}
	if gen, _ := st.EventGeneration(ctx); gen != 1 {
		t.Fatalf("generation without a marker = %d, want 1", gen)
	}

	marker := filepath.Join(dir, backup.RestoredMarker)
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := startRestoredHistory(ctx, st, dir, logger); err != nil {
		t.Fatalf("start after a restore: %v", err)
	}
	restored, _ := st.EventGeneration(ctx)
	if restored == 1 {
		t.Fatalf("generation after a restore = %d, want a new one", restored)
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the marker survived the start that consumed it: %v", err)
	}

	// The next start is an ordinary one.
	if err := startRestoredHistory(ctx, st, dir, logger); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if gen, _ := st.EventGeneration(ctx); gen != restored {
		t.Fatalf("generation after an ordinary restart = %d, want %d", gen, restored)
	}
}

// TestRestoringTheSameBackupTwiceStartsDistinctHistories is review F1 of
// issue #1001's train: the generation lives in the very file a restore
// replaces, so a bump derived from the restored row gave the second restore
// of one backup the generation the first one's clients already held, and
// their stale cursors were trusted again. Each restore must start a history
// no client has seen.
func TestRestoringTheSameBackupTwiceStartsDistinctHistories(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "vincent.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := t.Context()

	// The backup: a database as `vincent daemon backup` would capture it.
	st, err := storetest.Open(db)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	backupDB, err := os.ReadFile(db)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}

	// restore puts the backup in place and leaves its marker; the daemon's
	// next start consumes it. Returns the generation that start serves.
	restoreAndStart := func() int64 {
		t.Helper()
		for _, suffix := range []string{"-wal", "-shm"} {
			if err := os.Remove(db + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("remove %s: %v", suffix, err)
			}
		}
		if err := os.WriteFile(db, backupDB, 0o600); err != nil {
			t.Fatalf("restore database: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, backup.RestoredMarker), nil, 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		st, err := storetest.Open(db)
		if err != nil {
			t.Fatalf("open restored store: %v", err)
		}
		defer func() { _ = st.Close() }()
		if err := startRestoredHistory(ctx, st, dir, logger); err != nil {
			t.Fatalf("start after a restore: %v", err)
		}
		gen, err := st.EventGeneration(ctx)
		if err != nil {
			t.Fatalf("EventGeneration: %v", err)
		}
		return gen
	}

	first := restoreAndStart()
	second := restoreAndStart()
	if first == 1 || second == 1 || first == second {
		t.Fatalf("generations after restoring one backup twice = %d, %d; "+
			"want two new, distinct histories (the backup's is 1)", first, second)
	}
}
