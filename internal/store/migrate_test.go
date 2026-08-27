package store

import (
	"path/filepath"
	"testing"
)

// openTest opens a store on a fresh temp database. The path deliberately
// contains a space to guard the DSN URI escaping.
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "with space", "vincent.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// latestSchemaVersion tracks the newest migration file; bump alongside new
// migrations.
const latestSchemaVersion = 15

func schemaVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return v
}

func TestOpenAppliesSchema(t *testing.T) {
	s := openTest(t)

	for _, table := range []string{
		"projects", "tasks", "step_runs", "events", "agent_quota", "schema_migrations",
	} {
		var n int
		err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
		if err != nil {
			t.Fatalf("sqlite_master: %v", err)
		}
		if n != 1 {
			t.Errorf("table %s missing after migration", table)
		}
	}
	if v := schemaVersion(t, s); v != latestSchemaVersion {
		t.Errorf("schema version = %d, want %d", v, latestSchemaVersion)
	}
}

func TestOpenAppliesPragmas(t *testing.T) {
	s := openTest(t)

	var journal string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// A second migrate run on a live handle must be a no-op.
	if err := migrate(s.db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if v := schemaVersion(t, s); v != latestSchemaVersion {
		t.Errorf("schema version after re-migrate = %d, want %d", v, latestSchemaVersion)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening the same file must also be a no-op.
	s, err = Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if v := schemaVersion(t, s); v != latestSchemaVersion {
		t.Errorf("schema version after reopen = %d, want %d", v, latestSchemaVersion)
	}
}

func TestMigrationVersionParsing(t *testing.T) {
	if v, err := migrationVersion("0001_init.sql"); err != nil || v != 1 {
		t.Errorf("migrationVersion(0001_init.sql) = %d, %v; want 1, nil", v, err)
	}
	for _, bad := range []string{"init.sql", "x_init.sql", "0_init.sql", "-1_x.sql"} {
		if _, err := migrationVersion(bad); err == nil {
			t.Errorf("migrationVersion(%q) accepted a bad name", bad)
		}
	}
}
