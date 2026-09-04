package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// timeFormat is RFC3339 UTC with a fixed-width nanosecond fraction so that
// lexicographic TEXT comparison in SQL matches chronological order (plain
// RFC3339Nano trims trailing zeros, which breaks lexicographic ordering).
const timeFormat = "2006-01-02T15:04:05.000000000Z07:00"

// Store is the daemon's SQLite persistence layer (spec §14). All access goes
// through a single connection (phase 1 decision), so writes are serialized
// and SQLITE_BUSY cannot occur within the daemon.
type Store struct {
	db *sql.DB
	// path is the database file, kept so diagnostics can stat it without the
	// caller re-deriving a path the store already resolved (task 005, §17).
	path string
	// eventHook fires after an event's transaction commits; see SetEventHook.
	eventHook atomic.Pointer[func(*Event)]
}

// Open opens (creating if needed) the SQLite database at path, applies the
// connection pragmas (WAL, busy timeout, foreign keys), and runs any pending
// embedded migrations. The parent directory is created if missing.
func Open(path string) (*Store, error) {
	// G301: the data dir keeps the platform-default mode. Tightening it is a
	// user-visible change to an existing installation and needs its own spec
	// amendment; task 040 records it as follow-up rather than smuggling it in.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: see above
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return &Store{db: db, path: path}, nil
}

// Path returns the database file this store was opened on (§17: doctor
// reports its size, and the daemon is the only process that may say so).
func (s *Store) Path() string { return s.path }

// SchemaVersion returns the applied-migration high-water mark recorded in
// schema_migrations. It is the number doctor compares against the newest
// migration this binary embeds: a database ahead of the binary was written by
// a newer vincent, and running the older one against it is the one database
// state §18 calls out as needing a human (task 005).
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
}

// IntegrityCheck runs `PRAGMA integrity_check` and returns its verdict —
// "ok" on a healthy file, otherwise SQLite's own description of the damage,
// joined one finding per line. The strings are passed through untouched:
// §18's rule for a corrupt database is to point at it and never act on it.
func (s *Store) IntegrityCheck(ctx context.Context) (string, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return "", fmt.Errorf("integrity check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var findings []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", fmt.Errorf("scan integrity check: %w", err)
		}
		findings = append(findings, line)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate integrity check: %w", err)
	}
	return strings.Join(findings, "\n"), nil
}

// Vacuum rewrites the database file, reclaiming the pages that transcript
// pruning and task deletion freed. It takes an exclusive lock for the whole
// rewrite, so the caller is responsible for choosing a moment when no step is
// mid-write (task 005 decision 4).
func (s *Store) Vacuum(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}

// dsn builds a file: URI enabling WAL, a busy timeout, and foreign-key
// enforcement on every connection the pool opens. The path is URI-escaped so
// spaces and other special characters survive (e.g. "Application Support").
func dsn(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows drive-letter paths become file:///C:/…
	}
	u := url.URL{Path: p}
	return "file://" + u.EscapedPath() +
		"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1&_synchronous=NORMAL"
}

func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeFormat, s)
	if err != nil {
		// Tolerate plain RFC3339 variants (hand-edited or legacy rows).
		if t, err2 := time.Parse(time.RFC3339Nano, s); err2 == nil {
			return t, nil
		}
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t, nil
}

func parseTimePtr(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// nullString maps "" to SQL NULL.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullInt maps a nil *int to SQL NULL. Unlike nullString it distinguishes
// "unset" from zero: a watermark of 0 is a real wake position — an eager
// parent parked before any lane settled.
func nullInt(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}

// stringPtr maps a scanned column back to a *string, keeping SQL NULL and the
// empty string apart. It is nullString's inverse, for the columns where that
// distinction is the point: a NULL `rendered_prompt` is "nothing was
// recorded", an empty one is "the render produced nothing" (migration 0027).
func stringPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// placeholders renders the parenthesised bind-marker list for an n-value SQL
// `IN` clause: `(?)`, `(?, ?)`, and so on. n must be positive; callers return
// early on an empty set, since `IN ()` is not valid SQLite.
//
// Every `IN` list in this package goes through here, which is what makes the
// gosec G202 suppressions at the query sites checkable: the result is
// punctuation and bind markers only, derived from a count and never from a
// value, so the values themselves still travel as query arguments.
func placeholders(n int) string {
	return "(?" + strings.Repeat(", ?", n-1) + ")"
}
