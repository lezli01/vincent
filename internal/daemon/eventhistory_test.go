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
	if gen, _ := st.EventGeneration(ctx); gen != 2 {
		t.Fatalf("generation after a restore = %d, want 2", gen)
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the marker survived the start that consumed it: %v", err)
	}

	// The next start is an ordinary one.
	if err := startRestoredHistory(ctx, st, dir, logger); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if gen, _ := st.EventGeneration(ctx); gen != 2 {
		t.Fatalf("generation after an ordinary restart = %d, want 2", gen)
	}
}
