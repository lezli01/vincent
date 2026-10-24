package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DeliveryKind is what a run delivered (§5.3, task 137 decision 3).
type DeliveryKind string

// Delivery kinds. The set is closed: a handoff's `select:` and a board's
// card branch on it, so a free-form value would be one nobody can match.
const (
	// DeliveryChange is commits on the task's branch, to be integrated.
	DeliveryChange DeliveryKind = "change"
	// DeliveryReply is an answer, a refusal or questions, to be posted where
	// the work came from — the "needs info" and "won't fix" endings a
	// workshop can reach without changing any code.
	DeliveryReply DeliveryKind = "reply"
	// DeliveryNone is a run that produced nothing to hand on.
	DeliveryNone DeliveryKind = "none"
)

// DeliveryKinds lists every kind.
var DeliveryKinds = []DeliveryKind{DeliveryChange, DeliveryReply, DeliveryNone}

// ValidDeliveryKind reports whether k is a known kind.
func ValidDeliveryKind(k DeliveryKind) bool {
	for _, v := range DeliveryKinds {
		if v == k {
			return true
		}
	}
	return false
}

// Delivery is the workshop's result: what a task's run produced, recorded on
// the task (§5.3, task 137 decision 3). It is the contract a handoff reads,
// replacing the resolve workflows' `vincent-delivered/` file convention.
//
// A step records one explicitly with `vincent task deliver`. A run that
// reaches `done` without one gets an Implicit delivery written in the same
// transition, because the issue lane is SQL and can ask git nothing at read
// time (decision 3's 2026-10-24 amendment). A follow-up that re-admits the
// task clears it, so a delivery always describes the run that last finished.
type Delivery struct {
	Kind DeliveryKind `json:"kind"`
	// Title and Body are what a pull request or a comment would say: a
	// plain-language title and a Markdown body.
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	// Summary is what the change is for — the brief, a conflict resolver's
	// best input.
	Summary string `json:"summary,omitempty"`
	// HeadSHA is the commit the run verified; Branch and Base are where it
	// sits, filled by the daemon from the task.
	HeadSHA string `json:"head_sha,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Base    string `json:"base,omitempty"`
	// CloseReason is an optional suggestion for the issue's close, in
	// issuestate's vocabulary; Proof says what proved the change.
	CloseReason string `json:"close_reason,omitempty"`
	Proof       string `json:"proof,omitempty"`
	// Implicit marks a delivery the daemon wrote because the run recorded
	// none.
	Implicit   bool      `json:"implicit,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

// Clone returns a copy of d, or nil.
func (d *Delivery) Clone() *Delivery {
	if d == nil {
		return nil
	}
	c := *d
	return &c
}

func marshalDelivery(d *Delivery) (sql.NullString, error) {
	if d == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(d)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("marshal delivery: %w", err)
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func unmarshalDelivery(raw sql.NullString) (*Delivery, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	var d Delivery
	if err := json.Unmarshal([]byte(raw.String), &d); err != nil {
		return nil, fmt.Errorf("delivery_json: %w", err)
	}
	return &d, nil
}

// EventTaskDeliveryChanged announces that a step or a human recorded a
// task's delivery (task 137.2). Payload `{kind, implicit}` — never the
// text, which the detail route serves. Like task.github_pull_changed it is
// not a transition: the state is unchanged and `updated_at` is untouched. The
// implicit delivery a `→ done` writes rides that transition's own
// task.state_changed instead.
const EventTaskDeliveryChanged = "task.delivery_changed"

// ErrNotDeliverable is SetTaskDelivery refusing a task that is neither
// running nor done: a delivery describes a run, so there is nothing to
// record before one starts, and an aborted run delivered nothing.
var ErrNotDeliverable = errors.New("task is not running or done")

// DeliverableState reports whether a delivery may be recorded on a task in
// state s: while it runs, from one of its steps, or once it is done, as a
// correction.
func DeliverableState(s TaskState) bool { return s == TaskRunning || s == TaskDone }

// SetTaskDelivery replaces a task's delivery and records the durable
// task.delivery_changed event. The state check is inside the transaction, so
// a task that settles between the caller's read and this write is refused,
// not silently annotated. The returned error wraps ErrNotDeliverable with the
// state found.
func (s *Store) SetTaskDelivery(ctx context.Context, id int64, d *Delivery) (*Task, error) {
	if d == nil {
		return nil, fmt.Errorf("set task %d delivery: no delivery", id)
	}
	raw, err := marshalDelivery(d)
	if err != nil {
		return nil, err
	}
	var (
		out *Task
		ev  *Event
	)
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
		t, err := scanTask(row)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("task %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get task %d: %w", id, err)
		}
		if !DeliverableState(t.State) {
			return &DeliveryStateError{TaskID: id, State: t.State}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET delivery_json = ? WHERE id = ?`, raw, id); err != nil {
			return fmt.Errorf("set task %d delivery: %w", id, err)
		}
		t.Delivery = d.Clone()
		out = t
		body, err := json.Marshal(map[string]any{"kind": d.Kind, "implicit": d.Implicit})
		if err != nil {
			return fmt.Errorf("marshal task.delivery_changed event: %w", err)
		}
		taskID, projectID := t.ID, t.ProjectID
		ev = &Event{Type: EventTaskDeliveryChanged, TaskID: &taskID, ProjectID: &projectID, Payload: body}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return nil, err
	}
	s.notify(ev)
	return out, nil
}

// DeliveryStateError is ErrNotDeliverable with the state the task was in.
type DeliveryStateError struct {
	TaskID int64
	State  TaskState
}

func (e *DeliveryStateError) Error() string {
	return fmt.Sprintf("task %d is %s: %v", e.TaskID, e.State, ErrNotDeliverable)
}

// Unwrap lets errors.Is match ErrNotDeliverable.
func (e *DeliveryStateError) Unwrap() error { return ErrNotDeliverable }

// ListTasksMissingDelivery lists the finished root tasks of issues that carry
// no delivery: every task that reached `done` before migration 0045, or was
// archived from it. They are what taskrun.BackfillDeliveries fills, once, so
// every issue in `hand_off` has a delivery to show (task 137 decision 3).
// Other historical tasks keep NULL: nothing reads their delivery but a human
// looking at one.
func (s *Store) ListTasksMissingDelivery(ctx context.Context) ([]*Task, error) {
	h := handedOffCond()
	//nolint:gosec // G202: taskColumns and handedOffCond are constant SQL; every value binds
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks t
		WHERE t.delivery_json IS NULL AND t.issue_id IS NOT NULL AND t.parent_task_id IS NULL
		  AND `+h.sql+` ORDER BY t.id`, h.args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks missing a delivery: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks missing a delivery: %w", err)
	}
	return out, nil
}

// FillTaskDelivery writes d on a task that still has none, and reports
// whether it did. It is the backfill's write: guarded on `delivery_json IS
// NULL`, so a human's delivery recorded while the backfill ran is never
// overwritten, and a second pass changes nothing. It emits no event — no
// client is watching a task that finished before the daemon started.
func (s *Store) FillTaskDelivery(ctx context.Context, id int64, d *Delivery) (bool, error) {
	raw, err := marshalDelivery(d)
	if err != nil {
		return false, err
	}
	var filled bool
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE tasks SET delivery_json = ? WHERE id = ? AND delivery_json IS NULL`, raw, id)
		if err != nil {
			return fmt.Errorf("fill task %d delivery: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("fill task %d delivery: %w", id, err)
		}
		filled = n == 1
		return nil
	})
	return filled, err
}
