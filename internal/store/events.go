package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Durable event types written outside the engine and the transition path
// (spec §13.3). An archive is visible as task.state_changed with
// `to: archived`; there is no separate task.archived type (PR D decision).
const (
	EventTaskCreated             = "task.created"
	EventProjectCreated          = "project.created"
	EventProjectUpdated          = "project.updated"
	EventProjectDeleted          = "project.deleted"
	EventWorkflowRegistryChanged = "workflow.registry_changed"
	EventDaemonShuttingDown      = "daemon.shutting_down"
	// EventAgentQuotaChanged announces that what the daemon knows about an
	// adapter's usage window changed (task 026): a quota stop was observed,
	// or a successful run proved the window reopened. Payload
	// `{agent, spent, resets_at, source}`; it carries no task_id, like
	// workflow.registry_changed, because the fact is about an adapter rather
	// than about any one task.
	//
	// It is emitted only on a real change — never on a re-observation
	// identical to what is stored — and `scheduler.WakeOn` is false for it:
	// nothing about admission changes when an agent is near-spent. The
	// near-exhausted agent is displayed, never withheld.
	EventAgentQuotaChanged = "agent.quota_changed"
	// EventTaskGitHubPullChanged announces that a task's pull-request link
	// changed (task 052): the reconciler matched one, or a human linked or
	// unlinked one. Payload `{repo, number, source, suppressed}`, empty when
	// the link was cleared.
	//
	// It carries a task_id and is not a transition: the task's state is
	// unchanged, `updated_at` is untouched, and `scheduler.WakeOn` is false
	// for it, because nothing about admission depends on a pull request. It
	// exists so a running TUI re-renders a task whose pull request appeared
	// without polling the endpoint.
	EventTaskGitHubPullChanged = "task.github_pull_changed"
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
	TaskID    int64    // 0 = all tasks (the per-task stream's resume query)
	Limit     int      // 0 = unlimited
}

// MaxEventID returns the largest committed event id, or 0 when no event has
// been written yet. It is the high-water mark an SSE replay pages up to
// (§13.3): a resume walks only to the id that existed when the stream opened
// and lets the subscription carry everything after it, so a replay on a busy
// daemon cannot chase a tail that keeps moving.
func (s *Store) MaxEventID(ctx context.Context) (int64, error) {
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&id); err != nil {
		return 0, fmt.Errorf("max event id: %w", err)
	}
	return id, nil
}

// ListEvents returns events matching f in id order — the catch-up query
// behind SSE Last-Event-ID resume (spec §13.3).
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	q := `SELECT id, ts, type, task_id, project_id, payload_json FROM events WHERE id > ?`
	args := []any{f.AfterID}
	if len(f.Types) > 0 {
		//nolint:gosec // G202: placeholders() emits bind markers; the types bind below
		q += ` AND type IN ` + placeholders(len(f.Types))
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	if f.ProjectID != 0 {
		q += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	if f.TaskID != 0 {
		q += ` AND task_id = ?`
		args = append(args, f.TaskID)
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
