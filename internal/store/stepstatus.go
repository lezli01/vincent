package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrStepNotRunning reports that no *running* step run of the task carries
// the step id a status write was addressed at (task 036). It is deliberately
// distinct from ErrNotFound, which the task lookup answers with: the API
// turns a missing task into a 404 and this into a 409, because a finished
// step is a state the caller can see and re-check rather than a typo.
var ErrStepNotRunning = errors.New("step run is not running")

// LatestStepStatuses returns, for each of ids that has one, the status
// message on the task's **newest** step run (task 036). Tasks whose newest
// row said nothing are absent from the map.
//
// The newest row rather than a search for the newest *message* is the whole
// definition, and it is what keeps a board honest: a step that spoke and
// finished must not have its line linger beside the next step, which is
// silently doing something else. A running task therefore shows its live
// step's message, a blocked one shows the failing attempt's last words, and
// a task whose current step has nothing to say shows nothing.
//
// One query regardless of task count, like TaskRollups: `GET /v1/tasks` is
// what a board polls, and it must never fan out per row (§18).
func (s *Store) LatestStepStatuses(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	inList := placeholders(len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	// The join picks each task's highest row id and reads that row's message;
	// MAX(id) with a bare column would pair a maximum with an arbitrary row.
	//nolint:gosec // G202: inList is placeholders(); the ids bind as arguments
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.task_id, r.status_message FROM step_runs r
		JOIN (SELECT task_id, MAX(id) AS id FROM step_runs
			WHERE task_id IN `+inList+` GROUP BY task_id) m
		ON r.id = m.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("read step statuses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			taskID  int64
			message sql.NullString
		)
		if err := rows.Scan(&taskID, &message); err != nil {
			return nil, fmt.Errorf("scan step status: %w", err)
		}
		if message.String != "" {
			out[taskID] = message.String
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read step statuses: %w", err)
	}
	return out, nil
}

// RunningStepRunID resolves the running step run of task taskID at step
// stepID, or ErrStepNotRunning when the task has none.
//
// It exists so a caller can answer "is this addressable at all" before
// deciding whether to write — the engine's status throttle (task 036) refuses
// a write against a finished step whether or not it would have coalesced the
// value. The resolution rule is SetStepRunStatus's, and the two are
// deliberately the same query.
func (s *Store) RunningStepRunID(ctx context.Context, taskID int64, stepID string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM step_runs
		WHERE task_id = ? AND step_id = ? AND state = ?
		ORDER BY id DESC LIMIT 1`, taskID, stepID, string(StepRunning)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("task %d step %q: %w", taskID, stepID, ErrStepNotRunning)
	}
	if err != nil {
		return 0, fmt.Errorf("resolve running step run: %w", err)
	}
	return id, nil
}

// SetStepRunStatus records message as what the running step run of task
// taskID at step stepID is saying about itself (§5.4, §13.3), appending the
// durable `task.status_changed` event in the same transaction. It returns the
// row it wrote and whether anything actually changed.
//
// The row is resolved by `(task_id, step_id, state = running)` rather than by
// run id, because the caller is the step's own process and all it knows about
// itself is §8.5's `VINCENT_TASK_ID` and `VINCENT_STEP_ID`. Keying on the
// step id and not on the task alone is what makes a `parallel` group work:
// its sub-steps share one task id and run at the same time, so a task-keyed
// write would have two live steps overwriting each other's message. Within
// one task a step id has at most one running row — a `loop` body runs its
// passes in sequence, and a group's members have distinct ids — so the newest
// match is the only match.
//
// A message byte-identical to the stored one writes no event and reports
// changed=false: the `agent.quota_changed` rule, for the same reason. It
// still returns the row, so a caller can tell "nothing changed" from "there
// was nothing to change".
//
// The whole thing is one transaction so that the resolution, the write and
// the event commit together: a status can never be visible on the row without
// the event that announced it, which is what `Last-Event-ID` recovery rests
// on.
func (s *Store) SetStepRunStatus(
	ctx context.Context, taskID int64, stepID, message string,
) (runID int64, changed bool, err error) {
	return s.setStepRunStatus(ctx, message, func(ctx context.Context, tx *sql.Tx) (statusTarget, error) {
		var t statusTarget
		err := tx.QueryRowContext(ctx, `
			SELECT id, task_id, step_id, status_message FROM step_runs
			WHERE task_id = ? AND step_id = ? AND state = ?
			ORDER BY id DESC LIMIT 1`, taskID, stepID, string(StepRunning)).
			Scan(&t.runID, &t.taskID, &t.stepID, &t.stored)
		if errors.Is(err, sql.ErrNoRows) {
			return t, fmt.Errorf("task %d step %q: %w", taskID, stepID, ErrStepNotRunning)
		}
		if err != nil {
			return t, fmt.Errorf("resolve running step run: %w", err)
		}
		return t, nil
	})
}

// SetStepRunStatusByRun is SetStepRunStatus addressed at one row, whatever
// state it is in.
//
// It exists for the engine's coalescing floor (§13.3, task 036). A message
// written inside the floor is deferred, not refused, and the step may finish
// before the deferred write lands — so requiring `running` here would silently
// drop the last thing a step said, which is precisely the value the terminal
// reading is about. The step said it while it was running; the daemon merely
// chose when to persist it.
//
// Nothing else may use this. The endpoint's own refusal (409 for a step that
// is not running) lives in SetStepRunStatus and is what a caller sees.
func (s *Store) SetStepRunStatusByRun(
	ctx context.Context, runID int64, message string,
) (changed bool, err error) {
	_, changed, err = s.setStepRunStatus(ctx, message,
		func(ctx context.Context, tx *sql.Tx) (statusTarget, error) {
			t := statusTarget{runID: runID}
			err := tx.QueryRowContext(ctx,
				`SELECT task_id, step_id, status_message FROM step_runs WHERE id = ?`, runID).
				Scan(&t.taskID, &t.stepID, &t.stored)
			if errors.Is(err, sql.ErrNoRows) {
				return t, fmt.Errorf("step run %d: %w", runID, ErrNotFound)
			}
			if err != nil {
				return t, fmt.Errorf("resolve step run %d: %w", runID, err)
			}
			return t, nil
		})
	return changed, err
}

// statusTarget is the row a status write landed on, as its resolver found it.
type statusTarget struct {
	runID  int64
	taskID int64
	stepID string
	stored sql.NullString
}

// setStepRunStatus is the shared write: resolve, dedup, update, and append the
// durable event — all in one transaction, so a status can never be visible on
// the row without the event that announced it, which is what `Last-Event-ID`
// recovery rests on.
func (s *Store) setStepRunStatus(
	ctx context.Context, message string,
	resolve func(context.Context, *sql.Tx) (statusTarget, error),
) (runID int64, changed bool, err error) {
	var ev *Event
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		target, err := resolve(ctx, tx)
		if err != nil {
			return err
		}
		runID = target.runID
		if target.stored.String == message {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE step_runs SET status_message = ? WHERE id = ?`,
			nullString(message), target.runID); err != nil {
			return fmt.Errorf("set step run status: %w", err)
		}
		changed = true

		// project_id rides along so the /v1/events project filter works on
		// this type the way it does on every other task event. It is read
		// here rather than passed in because the caller is a step process
		// that has no reason to know its project's row id.
		var projectID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT project_id FROM tasks WHERE id = ?`, target.taskID).Scan(&projectID); err != nil {
			return fmt.Errorf("read task project: %w", err)
		}
		payload, err := json.Marshal(map[string]any{
			"task_id": target.taskID, "step_id": target.stepID, "message": message,
		})
		if err != nil {
			return fmt.Errorf("marshal %s event: %w", EventTaskStatusChanged, err)
		}
		id, pid := target.taskID, projectID
		ev = &Event{Type: EventTaskStatusChanged, TaskID: &id, ProjectID: &pid, Payload: payload}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return 0, false, err
	}
	if ev != nil {
		s.notify(ev)
	}
	return runID, changed, nil
}
