package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// EventGlobalPauseChanged announces that the daemon-wide pause was taken or
// lifted (task 149, §13.3). Payload `{paused, paused_at, reason}`, with
// `paused_at` null once lifted. Like agent.quota_changed it carries no
// task_id: the fact is about the daemon, not about any one task. The
// scheduler wakes on it — lifting the pause is exactly the moment a queue it
// has been holding becomes admissible.
const EventGlobalPauseChanged = "daemon.pause_changed"

// GlobalPause is the daemon-wide pause (task 149): while one is recorded the
// scheduler admits nothing, and every task that reaches a step boundary
// parks there. A nil *GlobalPause is "active".
type GlobalPause struct {
	PausedAt time.Time
	// Reason is the sentence the human gave when pausing; "" is none.
	Reason string
}

// GlobalPauseTally is the work a pause is waiting on or holding, which is
// what turns the one stored fact into §11's three displayed states: a pause
// with Running > 0 is still "pausing".
type GlobalPauseTally struct {
	// Running is the tasks still executing a step. Each will park at its
	// next step boundary.
	Running int
	// AwaitingInput is the tasks holding a live agent idle on a question.
	// They are not waited on — a human's answer, not the pause, decides
	// when they move — and park at the step boundary after the answer.
	AwaitingInput int
	// Queued is the tasks the scheduler is holding back.
	Queued int
	// Paused is the tasks the pause parked, which `continue` resumes.
	Paused int
}

// GetGlobalPause reads the daemon-wide pause; nil means active.
func (s *Store) GetGlobalPause(ctx context.Context) (*GlobalPause, error) {
	return getGlobalPause(ctx, s.db)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getGlobalPause(ctx context.Context, q queryRower) (*GlobalPause, error) {
	var at, reason string
	err := q.QueryRowContext(ctx, `SELECT paused_at, reason FROM global_pause WHERE id = 1`).Scan(&at, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil is the documented "active"
	}
	if err != nil {
		return nil, fmt.Errorf("get global pause: %w", err)
	}
	t, err := parseTime(at)
	if err != nil {
		return nil, fmt.Errorf("parse global pause time: %w", err)
	}
	return &GlobalPause{PausedAt: t, Reason: reason}, nil
}

// SetGlobalPause takes the daemon-wide pause and, in the same transaction,
// requests a pause on every running task a human has not already asked to
// pause, marking each as the global pause's (task 149). The bool reports
// whether anything changed: pausing an already-paused daemon keeps the
// original time and reason and writes no event, so a second `pause` is a
// harmless repeat rather than a way to lose when the window began.
//
// The marks are what a client renders as "pausing"; they are not what makes
// a running task stop. The engine consults the pause itself at each step
// boundary, so a task admitted in the instant before this commit parks too.
func (s *Store) SetGlobalPause(ctx context.Context, reason string, now time.Time) (*GlobalPause, bool, error) {
	var (
		out *GlobalPause
		ev  *Event
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		prev, err := getGlobalPause(ctx, tx)
		if err != nil {
			return err
		}
		if prev != nil {
			out = prev
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO global_pause (id, paused_at, reason) VALUES (1, ?, ?)`,
			formatTime(now), reason); err != nil {
			return fmt.Errorf("set global pause: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE tasks SET pause_requested = 1, global_pause = 1, updated_at = ?
			WHERE state = ? AND pause_requested = 0`,
			formatTime(now), string(TaskRunning)); err != nil {
			return fmt.Errorf("mark running tasks for the global pause: %w", err)
		}
		out = &GlobalPause{PausedAt: now, Reason: reason}
		if ev, err = globalPauseEvent(out); err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return nil, false, err
	}
	if ev != nil {
		s.notify(ev)
	}
	return out, ev != nil, nil
}

// ClearGlobalPause lifts the daemon-wide pause (task 149) and returns the
// tasks it parked, for the caller to resume through §6's `resume` — the one
// path a paused task leaves by, so each still gets its own transition and
// event. In the same transaction it withdraws every pause it requested but
// the task had not yet taken: a task still running its step, or one a crash
// re-queued before it could park. The bool reports whether a pause was
// lifted; lifting none returns no ids, since nothing should be resumed on
// the say-so of a pause that does not exist.
func (s *Store) ClearGlobalPause(ctx context.Context) ([]int64, bool, error) {
	var (
		ids []int64
		ev  *Event
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		prev, err := getGlobalPause(ctx, tx)
		if err != nil || prev == nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM global_pause WHERE id = 1`); err != nil {
			return fmt.Errorf("clear global pause: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE tasks SET pause_requested = 0, global_pause = 0, updated_at = ?
			WHERE global_pause = 1 AND state <> ?`,
			formatTime(time.Now()), string(TaskPaused)); err != nil {
			return fmt.Errorf("withdraw global pause requests: %w", err)
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM tasks WHERE global_pause = 1 AND state = ? ORDER BY priority DESC, created_at, id`,
			string(TaskPaused))
		if err != nil {
			return fmt.Errorf("list globally paused tasks: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan globally paused task: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("list globally paused tasks: %w", err)
		}
		if ev, err = globalPauseEvent(nil); err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return nil, false, err
	}
	if ev != nil {
		s.notify(ev)
	}
	return ids, ev != nil, nil
}

// TallyGlobalPause counts what a pause is waiting on and holding (see
// GlobalPauseTally). Archived rows are never counted.
func (s *Store) TallyGlobalPause(ctx context.Context) (GlobalPauseTally, error) {
	var t GlobalPauseTally
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(state = ?), 0),
			COALESCE(SUM(state = ?), 0),
			COALESCE(SUM(state = ?), 0),
			COALESCE(SUM(state = ? AND global_pause = 1), 0)
		FROM tasks WHERE state IN (?, ?, ?, ?)`,
		string(TaskRunning), string(TaskAwaitingInput), string(TaskQueued), string(TaskPaused),
		string(TaskRunning), string(TaskAwaitingInput), string(TaskQueued), string(TaskPaused),
	).Scan(&t.Running, &t.AwaitingInput, &t.Queued, &t.Paused)
	if err != nil {
		return GlobalPauseTally{}, fmt.Errorf("tally global pause: %w", err)
	}
	return t, nil
}

func globalPauseEvent(p *GlobalPause) (*Event, error) {
	body := map[string]any{"paused": p != nil, "paused_at": nil, "reason": ""}
	if p != nil {
		body["paused_at"] = p.PausedAt.UTC().Format(time.RFC3339)
		body["reason"] = p.Reason
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventGlobalPauseChanged, err)
	}
	return &Event{Type: EventGlobalPauseChanged, Payload: payload}, nil
}
