package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SetEventHook registers fn to run after an event's transaction commits.
// It is the daemon's single notification seam: the scheduler wakes from it
// (spec §11) and T2.7's SSE broker will publish from it, which is why it
// fires strictly post-commit — a subscriber must never observe an event the
// database has not durably recorded.
//
// fn runs synchronously on the writing goroutine and must not block.
func (s *Store) SetEventHook(fn func(*Event)) { s.eventHook.Store(&fn) }

// notify runs the event hook, if one is registered.
func (s *Store) notify(e *Event) {
	if fn := s.eventHook.Load(); fn != nil && *fn != nil {
		(*fn)(e)
	}
}

// AppendEvent inserts e and assigns its ID (the SSE Last-Event-ID cursor).
// A zero TS means now; a nil Payload is stored as "{}".
func (s *Store) AppendEvent(ctx context.Context, e *Event) error {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage("{}")
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO events (ts, type, task_id, project_id, payload_json) VALUES (?, ?, ?, ?, ?)`,
		formatTime(e.TS), e.Type, e.TaskID, e.ProjectID, string(e.Payload))
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	e.ID = id
	s.notify(e)
	return nil
}

// EventFilter narrows ListEvents. Zero values mean "no filter".
type EventFilter struct {
	AfterID   int64    // only events with id > AfterID (SSE resume cursor)
	Types     []string // empty = all types
	ProjectID int64    // 0 = all projects
	Limit     int      // 0 = unlimited
}

// ListEvents returns events matching f in id order — the catch-up query
// behind SSE Last-Event-ID resume (spec §13.3).
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	q := `SELECT id, ts, type, task_id, project_id, payload_json FROM events WHERE id > ?`
	args := []any{f.AfterID}
	if len(f.Types) > 0 {
		q += ` AND type IN (` + strings.Repeat("?,", len(f.Types)-1) + `?)`
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	if f.ProjectID != 0 {
		q += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	q += ` ORDER BY id ASC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	return out, nil
}

func scanEvent(r rowScanner) (*Event, error) {
	var (
		e                 Event
		taskID, projectID sql.NullInt64
		ts, payload       string
	)
	if err := r.Scan(&e.ID, &ts, &e.Type, &taskID, &projectID, &payload); err != nil {
		return nil, err
	}
	if taskID.Valid {
		e.TaskID = &taskID.Int64
	}
	if projectID.Valid {
		e.ProjectID = &projectID.Int64
	}
	e.Payload = json.RawMessage(payload)
	var err error
	if e.TS, err = parseTime(ts); err != nil {
		return nil, err
	}
	return &e, nil
}
