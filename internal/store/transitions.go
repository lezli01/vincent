package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/taskstate"
)

// Durable task events written outside the engine (spec §13.3).
const (
	// EventTaskStateChanged is written by every transition.
	EventTaskStateChanged = "task.state_changed"
	// EventTaskPriorityChanged is written by PATCH /v1/tasks/{id}. Priority
	// is not a transition — the state is unchanged — so without its own
	// event a reorder would be invisible to clients watching the queue.
	EventTaskPriorityChanged = "task.priority_changed"
	// EventTaskStepAdvanced is written when the engine moves a running task's
	// step cursor. Also not a transition — the task stays `running` for its
	// whole multi-step life — so without it a board's k/n would sit at the
	// step the task started on until the run ended (§13.3, PR I decision).
	// It deliberately does not wake the scheduler: advancing a step changes
	// nothing about what may be admitted (see scheduler.WakeOn).
	EventTaskStepAdvanced = "task.step_advanced"
)

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
	// PauseRequested sets or clears the pending-pause flag. Engine
	// transitions leave it alone; every human action but `pause` clears it,
	// because each of them is a human saying "go" (§6).
	PauseRequested *bool
	// RetryCursorAt moves the retry-budget cursor — a human `retry` stamps
	// it with now, and the budget then counts only later failures (§7.2).
	RetryCursorAt *time.Time
	// PendingOverride hands edit+retry text to the actor that will create
	// the next attempt's step run; it clears when drained.
	PendingOverride *Override
	// PendingRepair hands an ad-hoc repair request to the actor the
	// admission it produces will start (§6, task 025). An empty request
	// clears the column, which is how the re-block drains it.
	//
	// Clearing is otherwise not the caller's job: TransitionTask NULLs the
	// column on any transition *out of* blocked that does not set one, so
	// `retry`, `skip` and `cancel` can never hand a stale request to a later
	// admission. The carve-out is the repair action itself, which is exactly
	// the blocked → queued transition that writes one.
	PendingRepair *RepairRequest
	// PendingFollowUp hands a follow-up run request to the actor the
	// admission it produces will start, and later records that run's own step
	// cursor (§6, task 027). An empty request clears the column.
	//
	// Its drain rule is neither the override's nor the repair's. A follow-up
	// survives the `fail` that blocks one of its steps and the `retry` that
	// re-runs it — the request is what makes the retry a follow-up retry
	// rather than a plain one — and TransitionTask drops it on any transition
	// into a settled state, which is exactly the Complete or Restore that
	// returns the task to its origin and the `cancel` that ends it
	// (decision 6). `skip` sets `abandoned` on the request instead, so the
	// next admission restores the origin without running anything.
	PendingFollowUp *FollowUpRequest
	// PendingInput stores the normalized InputRequest JSON with the
	// transition into awaiting_input (§7.4). Clearing is not the caller's
	// job: TransitionTask NULLs the column on any transition out of
	// awaiting_input, so "non-null iff awaiting_input" holds by construction.
	PendingInput *string
	// AdmitNotBefore holds admission of the re-queued task until an instant
	// (§11, task 003), and QueuedReason says why it is waiting. Both are
	// written with a transition *into* queued and, by the same construction
	// PendingInput uses, cleared by TransitionTask on any transition out of
	// it — so admission, parking and cancel all drop the hold without a
	// caller remembering to.
	AdmitNotBefore *time.Time
	QueuedReason   *string
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
		if from == TaskAwaitingInput {
			// The §7.4 invariant, enforced in one place: leaving
			// awaiting_input always discards the pending request.
			t.PendingInputJSON = ""
		}
		if from == TaskBlocked && ch.PendingRepair == nil {
			// A repair request describes *this* block, so any other way out
			// of blocked drops it — retry means retry, and skip must not
			// hand a repair aimed at one step to the step after it. The one
			// transition that sets a request is the repair action itself,
			// and it is carved out by having supplied one (task 025).
			t.PendingRepair = nil
		}
		if taskstate.Settled(to) {
			// A follow-up request describes a *run*, and reaching done,
			// aborted or archived is that run ending however it ended (task
			// 027 decision 6). One rule covers all three ways out: the
			// Complete that returns a done-origin follow-up, the Restore that
			// returns an aborted-origin one, and the cancel that abandons
			// either. Nothing else can leave a request behind on a finished
			// task for the next follow-up to inherit.
			t.PendingFollowUp = nil
		}
		if from == TaskQueued {
			// The §11 admission-hold invariant, same construction: a hold
			// describes *this* queued period, so leaving queued always drops
			// it. Admission, parking and cancel are all covered by the one
			// rule, and a caller cannot forget it.
			//
			// One consequence, recorded rather than fixed (task 003): pausing
			// a held task and resuming it re-admits at once and re-discovers
			// the wall. That costs one process spawn, and buys the rule §6
			// already applies to every other pending flag — a human action
			// means go.
			t.AdmitNotBefore, t.QueuedReason = nil, ""
		}
		switch to {
		case TaskRunning:
			if t.StartedAt == nil {
				t.StartedAt = &now
			}
		case TaskDone, TaskAborted:
			t.FinishedAt = &now
		case TaskArchived:
			t.ArchivedAt = &now
		case TaskQueued, TaskAwaitingGate, TaskAwaitingInput, TaskAwaitingChildren, TaskBlocked, TaskPaused:
			// No timestamp of their own; updated_at covers them.
		}
		if to != TaskBlocked {
			t.BlockReason = ""
		}

		pendingOverride, err := marshalOverride(t.PendingOverride)
		if err != nil {
			return err
		}
		pendingRepair, err := marshalRepair(t.PendingRepair)
		if err != nil {
			return err
		}
		pendingFollowUp, err := marshalFollowUp(t.PendingFollowUp)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE tasks SET state = ?, current_step = ?, block_reason = ?, worktree_path = ?,
				workflow_snapshot = ?, pause_requested = ?, retry_cursor_at = ?,
				pending_override_json = ?, pending_repair_json = ?,
				pending_follow_up_json = ?, pending_input_json = ?,
				admit_not_before = ?, queued_reason = ?,
				updated_at = ?, started_at = ?, finished_at = ?, archived_at = ?
			WHERE id = ? AND state = ?`,
			string(t.State), t.CurrentStep, nullString(t.BlockReason), nullString(t.WorktreePath),
			t.WorkflowSnapshot, t.PauseRequested, formatTimePtr(t.RetryCursorAt), pendingOverride,
			pendingRepair, pendingFollowUp,
			nullString(t.PendingInputJSON),
			formatTimePtr(t.AdmitNotBefore), nullString(t.QueuedReason),
			formatTime(t.UpdatedAt),
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
		// Copied, not aliased: t is handed back to the caller as the updated
		// task, and this event outlives that hand-off inside the broker.
		taskID, projectID := t.ID, t.ProjectID
		e := &Event{
			TS:        now,
			Type:      EventTaskStateChanged,
			TaskID:    &taskID,
			ProjectID: &projectID,
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
	s.notify(ev)
	return task, ev, nil
}

// SetTaskProgress writes the step cursor and worktree path without changing
// state — the engine advancing through steps, and recording the worktree it
// created (§10). Nil fields are left unchanged.
//
// Moving the cursor appends a task.step_advanced event in the same
// transaction, so a client watching the stream sees k/n track a run instead
// of freezing at the step the task started on. A worktree-path-only write
// emits nothing: it is bookkeeping no client renders.
func (s *Store) SetTaskProgress(ctx context.Context, id int64, currentStep *int, worktreePath *string) error {
	if currentStep == nil && worktreePath == nil {
		return nil
	}
	var ev *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		now := time.Now()
		sets := []string{"updated_at = ?"}
		args := []any{formatTime(now)}
		if currentStep != nil {
			sets = append(sets, "current_step = ?")
			args = append(args, *currentStep)
		}
		if worktreePath != nil {
			sets = append(sets, "worktree_path = ?")
			args = append(args, nullString(*worktreePath))
		}
		args = append(args, id)
		res, err := tx.ExecContext(ctx,
			`UPDATE tasks SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
		if err != nil {
			return fmt.Errorf("set task %d progress: %w", id, err)
		}
		if err := oneRowAffected(res, fmt.Sprintf("task %d", id)); err != nil {
			return err
		}
		if currentStep == nil {
			return nil
		}
		// The project id rides the event so /v1/events?project_id= can filter
		// it like every other task event; it is not on the write path, so it
		// costs one read of a row the transaction already touched.
		var projectID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT project_id FROM tasks WHERE id = ?`, id).Scan(&projectID); err != nil {
			return fmt.Errorf("read task %d project: %w", id, err)
		}
		payload, err := json.Marshal(map[string]any{"current_step": *currentStep})
		if err != nil {
			return fmt.Errorf("marshal step advance event: %w", err)
		}
		e := &Event{
			TS:        now,
			Type:      EventTaskStepAdvanced,
			TaskID:    &id,
			ProjectID: &projectID,
			Payload:   payload,
		}
		if err := appendEventTx(ctx, tx, e); err != nil {
			return err
		}
		ev = e
		return nil
	})
	if err != nil {
		return err
	}
	if ev != nil {
		s.notify(ev)
	}
	return nil
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

// CountStepAttempts summarizes attempts for one step. `since` bounds Failed
// to attempts started after a point in time — the hook a human retry uses to
// reset the retry budget (§6); the zero time counts all failures. Last is
// never bounded within the position ref names: attempt numbers must stay
// monotonic over that position's whole history, or transcript names would
// collide and truncate earlier attempts (phase 2 decision).
//
// The position is the whole of ref — index, id and iteration. The id narrows
// the count to one member of a `parallel` group, whose sub-steps share the
// group's index (task 014 decision 16); the iteration narrows it further to
// one pass of a `loop` body, which is what makes retries and iterations
// orthogonal: a body step spends a fresh budget in each iteration, because
// each iteration is a different piece of work (§7.8, task 016 decision 6).
// For an ordinary step both filters change nothing — one step owns the index
// and its rows carry iteration 0.
func (s *Store) CountStepAttempts(ctx context.Context, ref StepRef, since time.Time) (StepAttempts, error) {
	var (
		out    StepAttempts
		last   sql.NullInt64
		failed sql.NullInt64
	)
	// The empty string sorts before every formatted time, so a zero cursor
	// counts all failures.
	sinceArg := ""
	if !since.IsZero() {
		sinceArg = formatTime(since)
	}
	q := `SELECT MAX(attempt),
			SUM(CASE WHEN state = ? AND started_at > ? THEN 1 ELSE 0 END)
		FROM step_runs WHERE task_id = ? AND step_index = ? AND step_id = ? AND iteration = ?`
	args := []any{string(StepFailed), sinceArg, ref.TaskID, ref.StepIndex, ref.StepID, ref.Iteration}
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
	if ch.PauseRequested != nil {
		t.PauseRequested = *ch.PauseRequested
	}
	if ch.RetryCursorAt != nil {
		t.RetryCursorAt = ch.RetryCursorAt
	}
	if ch.PendingOverride != nil {
		if ch.PendingOverride.Empty() {
			t.PendingOverride = nil
		} else {
			t.PendingOverride = ch.PendingOverride
		}
	}
	if ch.PendingRepair != nil {
		if ch.PendingRepair.Empty() {
			t.PendingRepair = nil
		} else {
			t.PendingRepair = ch.PendingRepair
		}
	}
	if ch.PendingFollowUp != nil {
		if ch.PendingFollowUp.Empty() {
			t.PendingFollowUp = nil
		} else {
			t.PendingFollowUp = ch.PendingFollowUp
		}
	}
	if ch.PendingInput != nil {
		t.PendingInputJSON = *ch.PendingInput
	}
	if ch.AdmitNotBefore != nil {
		t.AdmitNotBefore = ch.AdmitNotBefore
	}
	if ch.QueuedReason != nil {
		t.QueuedReason = *ch.QueuedReason
	}
}

// marshalOverride renders a pending override for storage; none is SQL NULL.
func marshalOverride(o *Override) (any, error) {
	if o == nil || o.Empty() {
		return nil, nil
	}
	b, err := json.Marshal(o)
	if err != nil {
		return nil, fmt.Errorf("marshal pending override: %w", err)
	}
	return string(b), nil
}

// marshalRepair renders a pending repair request for storage; none is SQL
// NULL.
func marshalRepair(r *RepairRequest) (any, error) {
	if r == nil || r.Empty() {
		return nil, nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("marshal pending repair: %w", err)
	}
	return string(b), nil
}

// marshalFollowUp renders a pending follow-up request for storage; none is
// SQL NULL.
func marshalFollowUp(r *FollowUpRequest) (any, error) {
	if r == nil || r.Empty() {
		return nil, nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("marshal pending follow-up: %w", err)
	}
	return string(b), nil
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
