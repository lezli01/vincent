package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// EventTaskStateChanged is the durable event every transition writes
// (spec §13.3).
const EventTaskStateChanged = "task.state_changed"

// StateConflictError reports that a task was not in the expected state when
// a transition was attempted — the API answers 409 with the current state
// (spec §13.1).
type StateConflictError struct {
	TaskID int64
	Want   TaskState
	Got    TaskState
}

func (e *StateConflictError) Error() string {
	return fmt.Sprintf("task %d is %s, not %s", e.TaskID, e.Got, e.Want)
}

// AsStateConflict extracts a *StateConflictError from err, if that is what
// it is.
func AsStateConflict(err error) (*StateConflictError, bool) {
	var conflict *StateConflictError
	ok := errors.As(err, &conflict)
	return conflict, ok
}

// TaskChange are optional field writes applied atomically with a transition.
// A nil field is left unchanged.
type TaskChange struct {
	// BlockReason is recorded when moving to blocked; leaving blocked always
	// clears it, whatever this holds.
	BlockReason *string
	// CurrentStep moves the step cursor (advance, or rewind on retry).
	CurrentStep *int
	// WorktreePath records the worktree once created (§10).
	WorktreePath *string
	// Snapshot replaces the workflow snapshot — edit+retry, which overrides
	// a step in this task's snapshot only (§6).
	Snapshot *string
	// EventPayload carries extra fields into the state-change event, merged
	// with from/to. Reserved keys (from, to) are overwritten.
	EventPayload map[string]any
}

// TransitionTask moves a task from one state to another, writing the state
// change and its durable event in a single transaction (phase 2 decision):
// an event never exists without the state change that produced it, and the
// event id is the SSE cursor a client can resume from.
//
// The update is a compare-and-swap on `from`: if the task moved in the
// meantime the transaction rolls back and a *StateConflictError is returned
// carrying the state actually found. Callers should validate the action
// against taskstate first; this is the race guard, not the rulebook.
//
// Timestamps follow §6 by convention: started_at is stamped the first time a
// task runs, finished_at when it reaches done or aborted, archived_at on
// archive.
func (s *Store) TransitionTask(
	ctx context.Context, id int64, from, to TaskState, ch TaskChange,
) (*Task, *Event, error) {
	var (
		task *Task
		ev   *Event
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
		t, err := scanTask(row)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("task %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get task %d: %w", id, err)
		}
		if t.State != from {
			return &StateConflictError{TaskID: id, Want: from, Got: t.State}
		}

		now := time.Now()
		t.State = to
		t.UpdatedAt = now
		applyChange(t, ch)
		switch to {
		case TaskRunning:
			if t.StartedAt == nil {
				t.StartedAt = &now
			}
		case TaskDone, TaskAborted:
			t.FinishedAt = &now
		case TaskArchived:
			t.ArchivedAt = &now
		case TaskQueued, TaskAwaitingGate, TaskBlocked, TaskPaused:
			// No timestamp of their own; updated_at covers them.
		}
		if to != TaskBlocked {
			t.BlockReason = ""
		}

		res, err := tx.ExecContext(ctx, `
			UPDATE tasks SET state = ?, current_step = ?, block_reason = ?, worktree_path = ?,
				workflow_snapshot = ?, updated_at = ?, started_at = ?, finished_at = ?, archived_at = ?
			WHERE id = ? AND state = ?`,
			string(t.State), t.CurrentStep, nullString(t.BlockReason), nullString(t.WorktreePath),
			t.WorkflowSnapshot, formatTime(t.UpdatedAt),
			formatTimePtr(t.StartedAt), formatTimePtr(t.FinishedAt), formatTimePtr(t.ArchivedAt),
			id, string(from))
		if err != nil {
			return fmt.Errorf("transition task %d: %w", id, err)
		}
		if err := oneRowAffected(res, fmt.Sprintf("task %d", id)); err != nil {
			return err
		}

		payload, err := statePayload(from, to, t, ch.EventPayload)
		if err != nil {
			return err
		}
		e := &Event{
			TS:        now,
			Type:      EventTaskStateChanged,
			TaskID:    &t.ID,
			ProjectID: &t.ProjectID,
			Payload:   payload,
		}
		if err := appendEventTx(ctx, tx, e); err != nil {
			return err
		}
		task, ev = t, e
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return task, ev, nil
}

// SetTaskProgress writes the step cursor and worktree path without changing
// state — the engine advancing through steps, and recording the worktree it
// created (§10). Nil fields are left unchanged.
func (s *Store) SetTaskProgress(ctx context.Context, id int64, currentStep *int, worktreePath *string) error {
	if currentStep == nil && worktreePath == nil {
		return nil
	}
	sets := []string{"updated_at = ?"}
	args := []any{formatTime(time.Now())}
	if currentStep != nil {
		sets = append(sets, "current_step = ?")
		args = append(args, *currentStep)
	}
	if worktreePath != nil {
		sets = append(sets, "worktree_path = ?")
		args = append(args, nullString(*worktreePath))
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("set task %d progress: %w", id, err)
	}
	return oneRowAffected(res, fmt.Sprintf("task %d", id))
}

// StepAttempts summarizes the attempts recorded for one step of one task.
type StepAttempts struct {
	// Last is the highest attempt number written so far; the next attempt is
	// Last+1. Attempt numbers are monotonic per (task, step) so history stays
	// append-only and transcript files never collide (phase 2 decision).
	Last int
	// Failed counts attempts that failed. Interrupted attempts are excluded:
	// an interruption is not a failure and consumes no retry (§7.2).
	Failed int
}

// CountStepAttempts summarizes attempts for one step. `since` bounds the
// count to attempts started after a point in time — the hook a human retry
// uses to reset the retry budget (§6); the zero time counts all attempts.
func (s *Store) CountStepAttempts(ctx context.Context, taskID int64, stepIndex int, since time.Time) (StepAttempts, error) {
	var (
		out      StepAttempts
		last     sql.NullInt64
		failed   sql.NullInt64
		sinceArg any
	)
	q := `SELECT MAX(attempt), SUM(CASE WHEN state = ? THEN 1 ELSE 0 END)
		FROM step_runs WHERE task_id = ? AND step_index = ?`
	args := []any{string(StepFailed), taskID, stepIndex}
	if !since.IsZero() {
		q += ` AND started_at > ?`
		sinceArg = formatTime(since)
		args = append(args, sinceArg)
	}
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&last, &failed); err != nil {
		return out, fmt.Errorf("count step attempts: %w", err)
	}
	out.Last = int(last.Int64)
	out.Failed = int(failed.Int64)
	return out, nil
}

func applyChange(t *Task, ch TaskChange) {
	if ch.CurrentStep != nil {
		t.CurrentStep = *ch.CurrentStep
	}
	if ch.WorktreePath != nil {
		t.WorktreePath = *ch.WorktreePath
	}
	if ch.Snapshot != nil {
		t.WorkflowSnapshot = *ch.Snapshot
	}
	if ch.BlockReason != nil {
		t.BlockReason = *ch.BlockReason
	}
}

// statePayload builds the state-change event body: ids plus the new state,
// not the full object — clients re-fetch what they need (§13.3).
func statePayload(from, to TaskState, t *Task, extra map[string]any) (json.RawMessage, error) {
	payload := map[string]any{}
	for k, v := range extra {
		payload[k] = v
	}
	payload["from"] = string(from)
	payload["to"] = string(to)
	payload["current_step"] = t.CurrentStep
	if t.BlockReason != "" {
		payload["block_reason"] = t.BlockReason
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal state event: %w", err)
	}
	return b, nil
}

// appendEventTx inserts an event inside an open transaction, assigning its
// id — the same insert AppendEvent performs standalone.
func appendEventTx(ctx context.Context, tx *sql.Tx, e *Event) error {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage("{}")
	}
	res, err := tx.ExecContext(ctx, `
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
	return nil
}

// withTx runs fn in a transaction, committing on success and rolling back on
// any error or panic.
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
