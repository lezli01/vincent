package storetest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/lezli01/vincent/internal/store"
)

// Open is store.Open for tests: when path does not exist yet it is first
// seeded with a copy of the migrated template, so the open finds every
// migration already applied. An existing path is opened as it is — a test
// reopening its own database, or one written by a backup or an import, gets
// exactly what store.Open would give it.
func Open(path string) (*store.Store, error) {
	if err := Seed(path); err != nil {
		return nil, err
	}
	return store.Open(path)
}

// Seed writes the migrated template to path unless something is already
// there, creating the parent directory the way store.Open does. It is for
// tests that hand a database path to code that opens it itself — a daemon
// started in-process, say — rather than opening it through Open.
func Seed(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("seed %s: %w", path, err)
	}
	b, err := template()
	if err != nil {
		return err
	}
	// Owner-only, as store.Open makes it (spec §12.2, #367).
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("seed %s: %w", path, err)
	}
	// 0644 before the umask is the mode SQLite itself creates a database
	// with, so a seeded file is indistinguishable from an opened one.
	//nolint:gosec // G306: SQLite's own create mode, in a test's temp dir
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("seed %s: %w", path, err)
	}
	return nil
}

// template returns the bytes of a database store.Open has migrated, built once
// per test process. Closing the only connection checkpoints the WAL into the
// main file and deletes it, so the one file is the whole database; template
// refuses to hand out bytes if a -wal file survived the close, rather than
// seed every test with a database missing its last pages.
var template = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "vincent-storetest-")
	if err != nil {
		return nil, fmt.Errorf("storetest template: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "template.db")
	s, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("storetest template: %w", err)
	}
	if err := s.Close(); err != nil {
		return nil, fmt.Errorf("storetest template: %w", err)
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return nil, fmt.Errorf("storetest template: %s-wal survived the close (%d bytes)", path, fi.Size())
	}
	//nolint:gosec // G304: the template this function just created in its own temp dir
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("storetest template: %w", err)
	}
	return b, nil
})
