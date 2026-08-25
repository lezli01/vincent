package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// The measurement half of §17's retention decision (task 029).
//
// §17 keeps DB rows indefinitely because "rows are small, history is
// valuable". That decision stands; what was missing was any way to check it on
// a real installation after six months of use. These four accessors are that
// check, and nothing here prunes, warns or vacuums.

// FileSizes is the database's on-disk footprint: the main file plus the two
// sidecars WAL mode keeps beside it.
//
// The main file alone understates the footprint between checkpoints — pages
// live in `-wal` until one moves them — which is why the total is the figure
// worth quoting rather than the headline `MainBytes`.
type FileSizes struct {
	MainBytes  int64
	WALBytes   int64
	SHMBytes   int64
	TotalBytes int64
}

// FileSizes stats the database file and its WAL and SHM sidecars.
//
// It lives here rather than in the callers because the store owns both `path`
// and the `_journal_mode=WAL` DSN that creates those sidecars: that they exist
// at all is this package's knowledge, and two callers deriving the names
// themselves is how they would come to disagree.
//
// A missing sidecar is zero, not an error. WAL and SHM are removed on a clean
// checkpoint, so their absence is the ordinary state of a database nobody
// currently has open.
func (s *Store) FileSizes() (FileSizes, error) {
	var out FileSizes
	for _, f := range []struct {
		path string
		into *int64
	}{
		{s.path, &out.MainBytes},
		{s.path + "-wal", &out.WALBytes},
		{s.path + "-shm", &out.SHMBytes},
	} {
		fi, err := os.Stat(f.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return FileSizes{}, fmt.Errorf("stat %s: %w", f.path, err)
		}
		*f.into = fi.Size()
		out.TotalBytes += fi.Size()
	}
	return out, nil
}

// TableRows counts the rows in every table the schema currently holds, keyed
// by table name.
//
// The set is enumerated from `sqlite_master`, never listed in code: a table a
// later migration adds is counted with no edit here, and a diagnostic that
// silently omits the one table that is growing is worse than no diagnostic.
// The cost is a key set that shifts between versions, which is correct for a
// figure describing *this* binary's database rather than a fixed contract.
// SQLite's own `sqlite_%` tables are excluded — they are the engine's
// bookkeeping, not vincent's data.
//
// This is a scan per table, which is why it is a `GET /v1/doctor` figure and
// not a `/v1/info` one (task 029 decision 1).
func (s *Store) TableRows(ctx context.Context) (map[string]int64, error) {
	names, err := s.tableNames(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(names))
	for _, name := range names {
		var n int64
		// A table name cannot be a bind parameter. It comes from
		// sqlite_master rather than from any caller, and is quoted anyway so
		// a migration free to name a table `order` stays countable.
		q := `SELECT COUNT(*) FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`
		if err := s.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return nil, fmt.Errorf("count rows in %s: %w", name, err)
		}
		out[name] = n
	}
	return out, nil
}

// tableNames reads the enumeration to completion before any counting starts.
// The store holds a single connection (phase 1 decision), so counting inside
// the open cursor would deadlock the query against itself.
func (s *Store) tableNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name FROM sqlite_master
		  WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		  ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tables: %w", err)
	}
	return names, nil
}

// OldestEventAt is the timestamp of the first event still on record — the span
// the database covers, which is what makes a row count extrapolable ("1.2M
// events over 14 months" rather than "1.2M events").
//
// `ORDER BY id LIMIT 1` is an index seek on the AUTOINCREMENT primary key, not
// a scan of `ts`: ids are handed out in insert order, so the lowest id is the
// oldest row.
//
// Nil on an empty events table. That is a fact, not an error — a fresh install
// has no span, and reporting the zero time would invent one.
func (s *Store) OldestEventAt(ctx context.Context) (*time.Time, error) {
	var ts sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT ts FROM events ORDER BY id LIMIT 1`).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read oldest event: %w", err)
	}
	return parseTimePtr(ts)
}

// WorkflowSnapshotBytes totals every task's `workflow_snapshot` — the full
// workflow YAML as it stood at task creation (§14).
//
// It is the second growth driver beside `events`, and having it separately is
// what tells "many small events" apart from "a few enormous snapshots". Those
// two databases weigh the same and would argue for opposite retention
// decisions, which is the discrimination a single byte total cannot make.
//
// The CAST is load-bearing: SQLite's LENGTH() counts *characters* on a TEXT
// value and bytes only on a BLOB, so without it a snapshot carrying any
// non-ASCII character would be under-reported by a figure claiming bytes.
func (s *Store) WorkflowSnapshotBytes(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(LENGTH(CAST(workflow_snapshot AS BLOB))), 0) FROM tasks`,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("sum workflow snapshots: %w", err)
	}
	return n, nil
}
