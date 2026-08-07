package store

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrate applies embedded migrations newer than the schema_migrations
// high-water mark, each inside its own transaction (phase 1 decision:
// hand-rolled, up-only, spec §14 table shape).
func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if version <= current {
			continue
		}
		if err := applyMigration(db, name, version); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(db *sql.DB, name string, version int) error {
	raw, err := migrationsFS.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("apply %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(string(raw)); err != nil {
		return fmt.Errorf("apply %s: %w", name, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		version, formatTime(time.Now())); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", name, err)
	}
	return nil
}

// migrationVersion parses the numeric prefix of "NNNN_description.sql".
func migrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %s: name must be NNNN_description.sql", name)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("migration %s: bad version prefix %q", name, prefix)
	}
	return v, nil
}
