package store

import (
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
	// eventHook fires after an event's transaction commits; see SetEventHook.
	eventHook atomic.Pointer[func(*Event)]
}

// Open opens (creating if needed) the SQLite database at path, applies the
// connection pragmas (WAL, busy timeout, foreign keys), and runs any pending
// embedded migrations. The parent directory is created if missing.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
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
	return &Store{db: db}, nil
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

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}
