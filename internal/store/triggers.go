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
// 096 *Observability*). They are the CHECK constraint of migration 0029.
const (
	// DeliveryFired is an event whose replayed POST /v1/tasks created a task.
	DeliveryFired = "fired"
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

// TriggerKeyDelivered reports whether dedupeKey has already *fired* for this
// trigger. Only a `fired` row counts: a `deduped` row created nothing and is
// itself evidence of an earlier `fired` one, while a `filtered`,
// `rate_limited`, `refused` or `error` row is an event that did not become a
// task — a relabel after a filter change, an hour later under the limit, or
// the same event after the refusal's cause was fixed must still be able to
// fire.
func (s *Store) TriggerKeyDelivered(ctx context.Context, triggerID, dedupeKey string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trigger_deliveries
		WHERE trigger_id = ? AND dedupe_key = ? AND outcome = ?`,
		triggerID, dedupeKey, DeliveryFired).Scan(&n)
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
