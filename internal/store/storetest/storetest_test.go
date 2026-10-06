package storetest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// TestSeededMatchesAFreshOpen is the package's whole promise: a seeded
// database is the one store.Open would have migrated, sound and at the same
// schema version.
func TestSeededMatchesAFreshOpen(t *testing.T) {
	fresh, err := store.Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	seeded, err := Open(filepath.Join(t.TempDir(), "with space", "seeded.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = seeded.Close() })

	want, err := fresh.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("fresh SchemaVersion: %v", err)
	}
	got, err := seeded.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("seeded SchemaVersion: %v", err)
	}
	if got != want || got == 0 {
		t.Errorf("seeded schema version = %d, want %d", got, want)
	}
	if v, err := seeded.IntegrityCheck(t.Context()); err != nil || v != "ok" {
		t.Errorf("seeded integrity_check = %q, %v; want ok", v, err)
	}
	if err := seeded.AppendEvent(t.Context(), &store.Event{Type: "daemon.started"}); err != nil {
		t.Errorf("a seeded store is not writable: %v", err)
	}
}

// TestSeedLeavesAnExistingFileAlone keeps Open safe to use for a reopen: a
// test's own data must never be replaced by an empty template.
func TestSeedLeavesAnExistingFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	if err := os.WriteFile(path, []byte("not a template"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Seed(path); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "not a template" {
		t.Errorf("Seed overwrote an existing file")
	}
}

// TestSeparateSeedsAreSeparateDatabases guards against sharing: every seed is
// its own copy, so one test's writes never show up in another's database.
func TestSeparateSeedsAreSeparateDatabases(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if err := a.AppendEvent(t.Context(), &store.Event{Type: "daemon.started"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	b, err := Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	evs, err := b.ListEvents(t.Context(), store.EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("b sees %d events written to a", len(evs))
	}
}
