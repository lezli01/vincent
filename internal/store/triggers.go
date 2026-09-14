package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Runtime state of event triggers (task 096.2, spec §14): the poll cursor with
// its health, and the delivery ledger. The definitions are files under
// {config_dir}/triggers/ and never reach this package.

// Delivery outcomes: what the firing pipeline decided about one event (task
// 096 *Observability*). They are the CHECK constraint of migration 0030.
const (
	// DeliveryFired is an event whose replayed route created or acted on a
	// task.
	DeliveryFired = "fired"
	// DeliverySeeded is an event the first poll after arming was shown (task
	// 096 decision 31B). It fired nothing — recording is not firing — and it
	// dedupes like DeliveryFired, so a source that keeps no cursor of its own
	// does not flood on its second poll with the backlog its seed saw.
	DeliverySeeded = "seeded"
	// DeliveryDeduped is an event whose dedupe key had already fired.
	DeliveryDeduped = "deduped"
	// DeliveryFiltered is an event `match:` or `if:` rejected.
	DeliveryFiltered = "filtered"
	// DeliveryRateLimited is an event over `limits.max_per_hour`, dropped
	// rather than queued.
	DeliveryRateLimited = "rate_limited"
	// DeliveryRefused is an event the replayed route answered with a 4xx; the
	// row keeps the §13.1 envelope.
	DeliveryRefused = "refused"
	// DeliveryError is a template that failed to render, or a replay that
	// answered 5xx or never reached the handler.
	DeliveryError = "error"
)

// Trigger events on §13.3's fan-out (task 096 decisions 24 and 31H).
const (
	// EventTriggerFired is a delivery whose outcome is `fired`. Payload
	// `{trigger_id, delivery_id, action}`, with the event's project_id and
	// task_id set: the task created or acted on. Only `fired` publishes — the
	// other outcomes are ledger rows the triggers view refreshes on its own
	// timer, and a durable event per filtered poll would grow the events table
	// with nothing anyone reacts to.
	EventTriggerFired = "trigger.fired"
	// EventTriggerPollChanged is a trigger's poll health on its first poll
	// after arming, and on every ok → failing and failing → ok transition —
	// never per poll. Payload `{trigger_id, ok, error}`.
	EventTriggerPollChanged = "trigger.poll_changed"
)

// FindTaskForBranch returns the task in projectID whose branch_name is branch,
// ignoring archived tasks and preferring the newest when more than one row
// holds the name (task 096 decision 31C: a reaction's `target: branch`). §10's
// claim keeps that to one unarchived row in practice; the ordering is what
// makes the answer deterministic if it ever is not. ErrNotFound when none.
func (s *Store) FindTaskForBranch(ctx context.Context, projectID int64, branch string) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE project_id = ? AND branch_name = ? AND archived_at IS NULL
		ORDER BY id DESC LIMIT 1`, projectID, branch)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no task on branch %q: %w", branch, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("find task for branch: %w", err)
	}
	return t, nil
}

// TriggerCursor is one trigger's poll watermark and health.
type TriggerCursor struct {
	TriggerID string
	// Cursor is nil for an unseeded trigger: its next poll seeds and fires
	// nothing (task 096 decisions 6 and 16).
	Cursor *string
	// LastPollAt is nil before the first poll.
	LastPollAt    *time.Time
	LastPollOK    bool
	LastPollError string
	LastFireAt    *time.Time
}

// TriggerDelivery is one ledger row.
type TriggerDelivery struct {
	ID        int64
	TriggerID string
	EventID   string
	DedupeKey string
	Outcome   string
	// TaskID is the task a `fired` delivery created, nil otherwise and nil
	// once that task has been deleted.
	TaskID *int64
	// Detail is a `refused` delivery's §13.1 envelope or an `error`
	// delivery's message.
	Detail    string
	CreatedAt time.Time
}

// GetTriggerCursor returns a trigger's cursor row, or ErrNotFound when it has
// never polled or its file has left the registry.
func (s *Store) GetTriggerCursor(ctx context.Context, triggerID string) (*TriggerCursor, error) {
	c := TriggerCursor{TriggerID: triggerID}
	var cursor, pollAt, fireAt sql.NullString
	var ok int
	err := s.db.QueryRowContext(ctx, `
		SELECT cursor, last_poll_at, last_poll_ok, last_poll_error, last_fire_at
		FROM trigger_cursors WHERE trigger_id = ?`, triggerID).
		Scan(&cursor, &pollAt, &ok, &c.LastPollError, &fireAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get trigger cursor: %w", err)
	}
	c.Cursor = stringPtr(cursor)
	c.LastPollOK = ok != 0
	if c.LastPollAt, err = parseTimePtr(pollAt); err != nil {
		return nil, fmt.Errorf("parse trigger last_poll_at: %w", err)
	}
	if c.LastFireAt, err = parseTimePtr(fireAt); err != nil {
		return nil, fmt.Errorf("parse trigger last_fire_at: %w", err)
	}
	return &c, nil
}

// ListTriggerCursors returns every cursor row, keyed by trigger id. The
// registry view reads it once per listing rather than once per trigger.
func (s *Store) ListTriggerCursors(ctx context.Context) (map[string]*TriggerCursor, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT trigger_id, cursor, last_poll_at, last_poll_ok, last_poll_error, last_fire_at
		FROM trigger_cursors`)
	if err != nil {
		return nil, fmt.Errorf("list trigger cursors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]*TriggerCursor{}
	for rows.Next() {
		var c TriggerCursor
		var cursor, pollAt, fireAt sql.NullString
		var ok int
		if err := rows.Scan(&c.TriggerID, &cursor, &pollAt, &ok, &c.LastPollError, &fireAt); err != nil {
			return nil, fmt.Errorf("scan trigger cursor: %w", err)
		}
		c.Cursor = stringPtr(cursor)
		c.LastPollOK = ok != 0
		if c.LastPollAt, err = parseTimePtr(pollAt); err != nil {
			return nil, fmt.Errorf("parse trigger last_poll_at: %w", err)
		}
		if c.LastFireAt, err = parseTimePtr(fireAt); err != nil {
			return nil, fmt.Errorf("parse trigger last_fire_at: %w", err)
		}
		out[c.TriggerID] = &c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list trigger cursors: %w", err)
	}
	return out, nil
}

// PutTriggerCursor writes a trigger's whole cursor row, inserting it on the
// first poll. The poller is the only writer, one goroutine per trigger, so a
// whole-row upsert cannot lose a concurrent field update.
func (s *Store) PutTriggerCursor(ctx context.Context, c *TriggerCursor) error {
	var cursor any
	if c.Cursor != nil {
		cursor = *c.Cursor
	}
	ok := 0
	if c.LastPollOK {
		ok = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO trigger_cursors (trigger_id, cursor, last_poll_at, last_poll_ok, last_poll_error, last_fire_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (trigger_id) DO UPDATE SET
			cursor = excluded.cursor,
			last_poll_at = excluded.last_poll_at,
			last_poll_ok = excluded.last_poll_ok,
			last_poll_error = excluded.last_poll_error,
			last_fire_at = excluded.last_fire_at`,
		c.TriggerID, cursor, formatTimePtr(c.LastPollAt), ok, c.LastPollError, formatTimePtr(c.LastFireAt))
	if err != nil {
		return fmt.Errorf("put trigger cursor: %w", err)
	}
	return nil
}

// DeleteTriggerCursor drops a trigger's cursor and poll status. It is what
// "the cursor follows the file" means (task 096 decision 16): a trigger whose
// file left the registry, or that was disarmed, seeds again on its next poll.
// Deleting a row that is not there is not an error.
func (s *Store) DeleteTriggerCursor(ctx context.Context, triggerID string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM trigger_cursors WHERE trigger_id = ?`, triggerID); err != nil {
		return fmt.Errorf("delete trigger cursor: %w", err)
	}
	return nil
}

// RecordTriggerDelivery appends a ledger row and returns it with its id and,
// when unset, its creation time filled in.
func (s *Store) RecordTriggerDelivery(ctx context.Context, d *TriggerDelivery) (*TriggerDelivery, error) {
	out := *d
	if out.CreatedAt.IsZero() {
		out.CreatedAt = time.Now()
	}
	var taskID any
	if out.TaskID != nil {
		taskID = *out.TaskID
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO trigger_deliveries (trigger_id, event_id, dedupe_key, outcome, task_id, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		out.TriggerID, out.EventID, out.DedupeKey, out.Outcome, taskID, out.Detail, formatTime(out.CreatedAt))
	if err != nil {
		return nil, fmt.Errorf("record trigger delivery: %w", err)
	}
	if out.ID, err = res.LastInsertId(); err != nil {
		return nil, fmt.Errorf("record trigger delivery: %w", err)
	}
	return &out, nil
}

// TriggerKeyDelivered reports whether dedupeKey has already *fired* or been
// *seeded* for this trigger. A `seeded` row counts because it is the record of
// an event that existed before the trigger was armed, which decision 16 says
// must never fire. A `deduped` row created nothing and is itself evidence of
// an earlier one, while a `filtered`, `rate_limited`, `refused` or `error` row
// is an event that did not become a task — a relabel after a filter change, an
// hour later under the limit, or the same event after the refusal's cause was
// fixed must still be able to fire.
func (s *Store) TriggerKeyDelivered(ctx context.Context, triggerID, dedupeKey string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trigger_deliveries
		WHERE trigger_id = ? AND dedupe_key = ? AND outcome IN (?, ?)`,
		triggerID, dedupeKey, DeliveryFired, DeliverySeeded).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("trigger key delivered: %w", err)
	}
	return n > 0, nil
}

// CountTriggerFiredSince counts a trigger's `fired` deliveries at or after
// since — `limits.max_per_hour`'s trailing-hour window when since is an hour
// ago.
func (s *Store) CountTriggerFiredSince(ctx context.Context, triggerID string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trigger_deliveries
		WHERE trigger_id = ? AND outcome = ? AND created_at >= ?`,
		triggerID, DeliveryFired, formatTime(since)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count trigger fires: %w", err)
	}
	return n, nil
}

// ListTriggerDeliveries returns a trigger's ledger newest first, at most limit
// rows. Rows of a trigger whose file is gone are still listed: the ledger
// outlives the definition.
func (s *Store) ListTriggerDeliveries(ctx context.Context, triggerID string, limit int) ([]TriggerDelivery, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, trigger_id, event_id, dedupe_key, outcome, task_id, detail, created_at
		FROM trigger_deliveries WHERE trigger_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, triggerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list trigger deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []TriggerDelivery
	for rows.Next() {
		var d TriggerDelivery
		var taskID sql.NullInt64
		var created string
		if err := rows.Scan(&d.ID, &d.TriggerID, &d.EventID, &d.DedupeKey, &d.Outcome,
			&taskID, &d.Detail, &created); err != nil {
			return nil, fmt.Errorf("scan trigger delivery: %w", err)
		}
		if taskID.Valid {
			id := taskID.Int64
			d.TaskID = &id
		}
		if d.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("parse trigger delivery created_at: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list trigger deliveries: %w", err)
	}
	return out, nil
}

// PruneTriggerDeliveries deletes ledger rows recorded before cutoff, returning
// how many went. The window is fixed at 30 days by the caller (§17, task 096
// decision 13): long enough that an issue relabelled next week does not
// refire, which is the horizon task 040's 24-hour keys were never meant to
// cover (decision 5).
//
// cutoff is a parameter so tests can age rows without sleeping.
func (s *Store) PruneTriggerDeliveries(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM trigger_deliveries WHERE created_at < ?`, formatTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("prune trigger deliveries: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune trigger deliveries: %w", err)
	}
	return n, nil
}
