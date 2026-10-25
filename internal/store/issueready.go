package store

// A person's — or an agent's — say on whether an issue's finished work is
// ready to hand off (task 137 decision 13). The lane is pure SQL, so the
// decision is stored where laneExpr reads it: the `ready` key of the
// delivery of the issue's latest finished main-line root task, the task
// latestDeliveryExpr picks. Neither write touches the issue row; the lane
// moves because the delivery did, and issue.lane_changed says who moved it.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/lezli01/vincent/internal/issuestate"
)

// ErrIssueWorkRunning refuses a readiness write while a root task of the
// issue is unsettled: the lane is `in_progress` whatever readiness says, and
// the run in flight will write the delivery the decision belongs on.
var ErrIssueWorkRunning = errors.New("issue has work running")

// ErrIssueNoFinishedWork refuses a readiness write on an issue with no
// finished main-line root task — no task, only aborted ones, or only a side
// task's, whose work reaches the issue through a merge-back that has not
// finished. There is no delivery to mark.
var ErrIssueNoFinishedWork = errors.New("issue has no finished work to hand off")

// ErrIssueNotHandOff refuses MarkIssueUnready on an issue that is not in
// the `hand_off` lane: there is nothing to withdraw.
var ErrIssueNotHandOff = errors.New("issue is not in the hand_off lane")

// MarkIssueReady marks the issue's latest finished main-line delivery ready,
// `ready_by` the actor's (ReadyByActor), and returns the issue re-read. A
// delivery already ready — including a legacy one, which reads ready — is
// left alone and records nothing.
func (s *Store) MarkIssueReady(ctx context.Context, issueID int64, by issuestate.Actor) (*Issue, error) {
	return s.markIssueReadiness(ctx, issueID, true, by)
}

// MarkIssueUnready withdraws an issue from `hand_off`: its latest finished
// main-line delivery is marked `ready: false`, and the issue waits in
// `in_progress` until someone marks it ready again or new work finishes.
func (s *Store) MarkIssueUnready(ctx context.Context, issueID int64, by issuestate.Actor) (*Issue, error) {
	return s.markIssueReadiness(ctx, issueID, false, by)
}

// markIssueReadiness is both writes, in one transaction. The refusals come
// in a fixed order, so a caller can rely on which one it gets: unknown,
// closed, work running, claimed by a live handoff item (the claim task
// creation refuses on, task 137.4), nothing finished, and — unready only —
// not in `hand_off`.
func (s *Store) markIssueReadiness(ctx context.Context, issueID int64, ready bool, by issuestate.Actor) (*Issue, error) {
	if err := checkActor(by); err != nil {
		return nil, err
	}
	err := s.writeIssueEvents(ctx, func(tx *sql.Tx) ([]*Event, error) {
		cur, err := getIssue(ctx, tx, issueID)
		if err != nil {
			return nil, err
		}
		if issuestate.Normalize(cur.State) == issuestate.Closed {
			return nil, fmt.Errorf("issue %d is closed: %w", issueID, ErrInvalidIssueAction)
		}
		if cur.Active {
			return nil, fmt.Errorf("issue %d: %w", issueID, ErrIssueWorkRunning)
		}
		if handoffID, _, claimed, err := issueClaimTx(ctx, tx, issueID); err != nil {
			return nil, err
		} else if claimed {
			return nil, &IssueInHandoffError{IssueID: issueID, HandoffID: handoffID}
		}
		taskID, delivery, err := latestDeliveryTx(ctx, tx, issueID)
		if err != nil {
			return nil, err
		}
		if taskID == 0 {
			return nil, fmt.Errorf("issue %d: %w", issueID, ErrIssueNoFinishedWork)
		}
		if ready && delivery.IsReady() {
			return nil, nil
		}
		if !ready && cur.Lane != issuestate.LaneHandOff {
			return nil, fmt.Errorf("issue %d is %s: %w", issueID, cur.Lane, ErrIssueNotHandOff)
		}
		watch, err := watchIssueLaneTx(ctx, tx, issueID, cur.ProjectID)
		if err != nil {
			return nil, err
		}
		// json_set keeps the delivery's text untouched. A task that finished
		// before migration 0045 has none at all; it gets a readiness-only
		// object, marked implicit, which the backfill still fills
		// (missingDeliveryCond) without losing the decision.
		readyJSON := "false"
		if ready {
			readyJSON = "true"
		}
		readyBy := string(ReadyByActor(by))
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET delivery_json = CASE
				WHEN delivery_json IS NULL THEN json_object('implicit', json('true'), 'ready', json(?), 'ready_by', ?)
				ELSE json_set(delivery_json, '$.ready', json(?), '$.ready_by', ?) END
			WHERE id = ?`, readyJSON, readyBy, readyJSON, readyBy, taskID); err != nil {
			return nil, fmt.Errorf("mark task %d delivery ready=%s: %w", taskID, readyJSON, err)
		}
		t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, taskID))
		if err != nil {
			return nil, fmt.Errorf("get task %d: %w", taskID, err)
		}
		ev, err := deliveryChangedEvent(t, t.Delivery)
		if err != nil {
			return nil, err
		}
		lane, err := watch.changedTx(ctx, tx, nil, by)
		if err != nil {
			return nil, err
		}
		return []*Event{ev, lane}, nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, issueID)
}

// latestDeliveryTx reads the task latestDeliveryExpr picks for an issue and
// its delivery, or a zero id when the issue has no finished main-line root
// task.
func latestDeliveryTx(ctx context.Context, tx *sql.Tx, issueID int64) (int64, *Delivery, error) {
	id, raw := latestDeliveryTaskExpr(), latestDeliveryExpr()
	args := slices.Concat(id.args, raw.args, []any{issueID})
	var (
		taskID   sql.NullInt64
		delivery sql.NullString
	)
	err := tx.QueryRowContext(ctx, `SELECT `+id.sql+`, `+raw.sql+` FROM issues i WHERE i.id = ?`, args...).
		Scan(&taskID, &delivery)
	if err != nil {
		return 0, nil, fmt.Errorf("read issue %d latest delivery: %w", issueID, err)
	}
	if !taskID.Valid {
		return 0, nil, nil
	}
	d, err := unmarshalDelivery(delivery)
	if err != nil {
		return 0, nil, fmt.Errorf("task %d: %w", taskID.Int64, err)
	}
	return taskID.Int64, d, nil
}
