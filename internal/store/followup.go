package store

// The pending follow-up request (task 027, spec §6/§14). Two writes that are
// deliberately *not* transitions live here: recording a follow-up's own step
// cursor as it advances, and reading how many rounds a task has already had.
//
// Neither changes state, so neither goes through TransitionTask — the same
// separation SetTaskProgress already makes for `current_step`.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SetPendingFollowUp writes a task's pending follow-up request without
// changing its state — the actor persisting the run's own step cursor as it
// advances through the follow-up workflow (task 027 decision 4).
//
// A nil or empty request clears the column. It emits no event: the follow-up
// cursor is not `current_step`, no client renders it, and a durable event per
// step of a follow-up would double the stream for something nobody reads. The
// state changes a follow-up produces — the block, the restore — carry their
// own events as usual.
func (s *Store) SetPendingFollowUp(ctx context.Context, id int64, req *FollowUpRequest) error {
	value, err := marshalFollowUp(req)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET pending_follow_up_json = ?, updated_at = ? WHERE id = ?`,
		value, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("set task %d follow-up: %w", id, err)
	}
	return oneRowAffected(res, fmt.Sprintf("task %d", id))
}

// MaxStepIndex is the highest step_index this task has any row at, and false
// when it has no rows at all.
//
// It is what numbers a follow-up round. Rows at indices below the snapshot's
// length are the workflow's own; anything at or past it is a previous
// follow-up round, and the next round is one past the highest (decision 2).
// Deriving it from the rows rather than storing a counter keeps the round
// numbering and the rows it places one truth — a stored counter that drifted
// would put two rounds at one index and merge their attempt numbering.
func (s *Store) MaxStepIndex(ctx context.Context, taskID int64) (int, bool, error) {
	var highest sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(step_index) FROM step_runs WHERE task_id = ?`, taskID).Scan(&highest); err != nil {
		return 0, false, fmt.Errorf("max step index for task %d: %w", taskID, err)
	}
	if !highest.Valid {
		return 0, false, nil
	}
	return int(highest.Int64), true, nil
}
