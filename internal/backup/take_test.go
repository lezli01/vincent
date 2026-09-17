package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// snapshot is a Take input over seed's directories, with a copier that writes
// seed's stand-in database.
func snapshot(t *testing.T, atomic bool) Snapshot {
	t.Helper()
	dbCopy, dirs := seed(t)
	body, err := os.ReadFile(dbCopy)
	if err != nil {
		t.Fatal(err)
	}
	return Snapshot{
		CopyDatabase: func(_ context.Context, dst string) error { return os.WriteFile(dst, body, 0o600) },
		DataDir:      dirs.Data,
		ConfigDir:    dirs.Config,
		Manifest:     Manifest{VincentVersion: "v9.9.9", SchemaVersion: 17, CreatedAt: "2026-09-17T00:00:00.000000000Z"},
		Atomic:       atomic,
	}
}

// Both modes write a readable archive and leave nothing beside it: the
// staging directory is gone on success.
func TestTakeWritesAndCleansUp(t *testing.T) {
	for _, atomic := range []bool{false, true} {
		dir := t.TempDir()
		dst := filepath.Join(dir, "vincent-backup.tar.gz")
		res, err := Take(context.Background(), dst, snapshot(t, atomic))
		if err != nil {
			t.Fatalf("atomic=%v: Take: %v", atomic, err)
		}
		if res.Path != dst || res.Bytes <= 0 {
			t.Errorf("atomic=%v: result = %+v", atomic, res)
		}
		if m, err := ReadManifest(dst); err != nil || m.SchemaVersion != 17 {
			t.Errorf("atomic=%v: manifest = %+v, %v", atomic, m, err)
		}
		if got, _ := os.ReadDir(dir); len(got) != 1 {
			t.Errorf("atomic=%v: directory holds %d entries, want only the archive", atomic, len(got))
		}
	}
}

// Rename would replace an existing file, so the atomic path refuses one
// itself — the same refusal Create's O_EXCL gives the direct path.
func TestTakeAtomicNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "vincent-backup.tar.gz")
	write(t, dst, "somebody's backup")
	_, err := Take(context.Background(), dst, snapshot(t, true))
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("Take over an existing archive: %v, want ErrDestinationExists", err)
	}
	if body, _ := os.ReadFile(dst); string(body) != "somebody's backup" {
		t.Errorf("the existing archive was replaced: %q", body)
	}
}

// A copy that fails leaves neither a final name nor the staging directory.
func TestTakeCopyFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	s := snapshot(t, true)
	s.CopyDatabase = func(_ context.Context, dst string) error {
		_ = os.WriteFile(dst, []byte("partial"), 0o600)
		return errors.New("disk full")
	}
	if _, err := Take(context.Background(), filepath.Join(dir, "a.tar.gz"), s); err == nil {
		t.Fatal("Take succeeded over a failing copy")
	}
	if got, _ := os.ReadDir(dir); len(got) != 0 {
		t.Errorf("a failed Take left %d entries", len(got))
	}
}
