package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
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

// ReadyBy is who made a delivery ready — or, on a `ready: false` one, who
// withdrew it (task 137 decision 13). It is the attribution a board shows
// beside a `hand_off` card.
type ReadyBy string

// Who can make a delivery ready. A legacy delivery carries none: an absent
// `ready` reads ready, today's automatic rule, so no migration writes one.
const (
	// ReadyByWorkflow is a `hand_off: auto` workflow's `→ done`.
	ReadyByWorkflow ReadyBy = "workflow"
	// ReadyByTask is a step's `vincent task deliver --ready` while the task
	// runs.
	ReadyByTask ReadyBy = "task"
	// ReadyByHuman is a person's `vincent issue ready`/`unready`, or a
	// correction marking a done task's delivery ready.
	ReadyByHuman ReadyBy = "human"
	// ReadyByAgent is the same from an agent outside the task's own run — a
	// step or MCP caller acting on the issue.
	ReadyByAgent ReadyBy = "agent"
)

// ReadyByActor is the attribution an issue actor's readiness write carries:
// an agent's is agent, and anyone else's — a human, or sync, which never
// marks readiness — is human.
func ReadyByActor(a issuestate.Actor) ReadyBy {
	if a == issuestate.Agent {
		return ReadyByAgent
	}
	return ReadyByHuman
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
	Implicit bool `json:"implicit,omitempty"`
	// Ready is whether the delivery is ready to hand off (task 137 decision
	// 13): the issue lane reads `hand_off` only from a ready latest
	// delivery. A pointer, because absent is not false: a delivery recorded
	// before readiness existed carries no key and reads ready, which is the
	// rule every lane had then. ReadyBy says who set it.
	Ready      *bool     `json:"ready,omitempty"`
	ReadyBy    ReadyBy   `json:"ready_by,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

// IsReady reports whether d reads ready: a nil delivery or one without a
// `ready` key is, the legacy reading readyExpr spells in SQL.
func (d *Delivery) IsReady() bool { return d == nil || d.Ready == nil || *d.Ready }

// Clone returns a copy of d, or nil.
func (d *Delivery) Clone() *Delivery {
	if d == nil {
		return nil
	}
	c := *d
	if d.Ready != nil {
		r := *d.Ready
		c.Ready = &r
	}
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
// task's delivery (task 137.2). Payload `{kind, implicit, ready}` — never
// the text, which the detail route serves; `ready` is the stored key, so it
// is absent on a delivery that reads ready only by the legacy rule (task 137
// decision 13). Like task.github_pull_changed it is not a transition: the
// state is unchanged and `updated_at` is untouched. The implicit delivery a
// `→ done` writes rides that transition's own task.state_changed instead.
const EventTaskDeliveryChanged = "task.delivery_changed"

// deliveryChangedEvent is the task.delivery_changed event for d on t.
func deliveryChangedEvent(t *Task, d *Delivery) (*Event, error) {
	body := map[string]any{"kind": d.Kind, "implicit": d.Implicit}
	if d.Ready != nil {
		body["ready"] = *d.Ready
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventTaskDeliveryChanged, err)
	}
	taskID, projectID := t.ID, t.ProjectID
	return &Event{Type: EventTaskDeliveryChanged, TaskID: &taskID, ProjectID: &projectID, Payload: payload}, nil
}

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
//
// Readiness follows the state found, also inside the transaction (task 137
// decision 2, "a correction keeps readiness"). On a running task the last
// call wins: d.Ready true is stored as `ready: true, ready_by: task`
// whatever ReadyBy the caller passed, and anything else stores no
// readiness, so a later deliver without --ready clears an earlier one. On a
// done task a correction replaces only the text: d.Ready nil or false keeps
// the stored readiness, and d.Ready true stores it with the caller's
// ReadyBy — on this task only, without vincent issue ready's refusals, so
// it moves the issue's lane only when this task holds the issue's latest
// finished main-line delivery (laneExpr reads no other). When it does, it
// records issue.lane_changed in the same commit, by the actor ReadyBy names.
func (s *Store) SetTaskDelivery(ctx context.Context, id int64, d *Delivery) (*Task, error) {
	if d == nil {
		return nil, fmt.Errorf("set task %d delivery: no delivery", id)
	}
	d = d.Clone()
	var (
		out *Task
		ev  *Event
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
		if !DeliverableState(t.State) {
			return &DeliveryStateError{TaskID: id, State: t.State}
		}
		markReady := d.Ready != nil && *d.Ready
		var watch laneWatch
		switch {
		case t.State == TaskRunning && markReady:
			d.ReadyBy = ReadyByTask
		case t.State == TaskRunning:
			d.Ready, d.ReadyBy = nil, ""
		case markReady:
			// A done task's lane is already settled, so marking it ready is
			// the one delivery write that can move it.
			if d.ReadyBy == "" {
				d.ReadyBy = ReadyByHuman
			}
			if watch, err = watchTaskLaneTx(ctx, tx, t.IssueID, t.ParentTaskID, t.ProjectID); err != nil {
				return err
			}
		case t.Delivery != nil:
			d.Ready, d.ReadyBy = t.Delivery.Ready, t.Delivery.ReadyBy
		default:
			d.Ready, d.ReadyBy = nil, ""
		}
		raw, err := marshalDelivery(d)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET delivery_json = ? WHERE id = ?`, raw, id); err != nil {
			return fmt.Errorf("set task %d delivery: %w", id, err)
		}
		t.Delivery = d.Clone()
		out = t
		if ev, err = deliveryChangedEvent(t, d); err != nil {
			return err
		}
		if err := appendEventTx(ctx, tx, ev); err != nil {
			return err
		}
		lane, err := watch.changedTx(ctx, tx, nil, readyActor(d.ReadyBy))
		if err != nil || lane == nil {
			return err
		}
		if err := appendEventTx(ctx, tx, lane); err != nil {
			return err
		}
		ev.follow = lane
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notify(ev)
	return out, nil
}

// readyActor is the issue actor a readiness write is attributed to on
// issue.lane_changed: ReadyByActor's inverse, where only an agent's is the
// agent's.
func readyActor(by ReadyBy) issuestate.Actor {
	if by == ReadyByAgent {
		return issuestate.Agent
	}
	return issuestate.Human
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

// missingDeliveryCond matches a task over alias t whose delivery is missing:
// NULL, or a readiness-only object with no `kind` — what MarkIssueReady and
// MarkIssueUnready write on a task that finished before migration 0045
// (task 137 decision 13). Both still want the backfill's delivery.
const missingDeliveryCond = `(t.delivery_json IS NULL OR json_extract(t.delivery_json, '$.kind') IS NULL)`

// ListTasksMissingDelivery lists the finished root tasks of issues that carry
// no delivery: every task that reached `done` before migration 0045, or was
// archived from it — including one a human marked ready or not ready since,
// which holds readiness and nothing else. They are what
// taskrun.BackfillDeliveries fills, once, so every issue in `hand_off` has a
// delivery to show (task 137 decision 3). Other historical tasks keep NULL:
// nothing reads their delivery but a human looking at one.
func (s *Store) ListTasksMissingDelivery(ctx context.Context) ([]*Task, error) {
	h := handedOffCond()
	//nolint:gosec // G202: taskColumns, missingDeliveryCond and handedOffCond are constant SQL; every value binds
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks t
		WHERE `+missingDeliveryCond+` AND t.issue_id IS NOT NULL AND t.parent_task_id IS NULL
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
// whether it did. It is the backfill's write: guarded on
// missingDeliveryCond, so a human's delivery recorded while the backfill ran
// is never overwritten, and a second pass changes nothing. A readiness-only
// delivery keeps its `ready` and `ready_by` (task 137 decision 13): the
// backfill supplies the text, never the decision. It emits no event — no
// client is watching a task that finished before the daemon started.
func (s *Store) FillTaskDelivery(ctx context.Context, id int64, d *Delivery) (bool, error) {
	if d == nil {
		return false, fmt.Errorf("fill task %d delivery: no delivery", id)
	}
	d = d.Clone()
	var filled bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var cur sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT t.delivery_json FROM tasks t WHERE t.id = ? AND `+missingDeliveryCond, id).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("fill task %d delivery: %w", id, err)
		}
		if stored, err := unmarshalDelivery(cur); err != nil {
			return fmt.Errorf("fill task %d delivery: %w", id, err)
		} else if stored != nil {
			d.Ready, d.ReadyBy = stored.Ready, stored.ReadyBy
		}
		raw, err := marshalDelivery(d)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET delivery_json = ? WHERE id = ?`, raw, id); err != nil {
			return fmt.Errorf("fill task %d delivery: %w", id, err)
		}
		filled = true
		return nil
	})
	return filled, err
}
