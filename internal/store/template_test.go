package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// migratedTemplate is internal/store/storetest's template for this package's
// own tests, which cannot import storetest (it imports store). See that
// package's doc for why it exists: under -race, migrating a new file costs
// ~330 ms and this package's tests open a few hundred of them.
var migratedTemplate = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "vincent-store-template-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "template.db")
	s, err := Open(path)
	if err != nil {
		return nil, err
	}
	if err := s.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
})

// seedTemplate writes the migrated template to path, which must not exist yet.
func seedTemplate(t *testing.T, path string) {
	t.Helper()
	b, err := migratedTemplate()
	if err != nil {
		t.Fatalf("migrated template: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
}
