package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/taskstate"
)

// Handoffs (spec §5.7, task 137 decisions 1, 2, 12 and 14; migration 0046).
// A handoff is one integration task and the issues it claimed: a task is a
// handoff task because a `handoffs` row points at it, and the claim is a
// handoff_items row the partial unique index allows only one live copy of.
// This file holds the store primitives — the create, the item-outcome write
// and the derived state. The routes and verbs over them are 137.5 and 137.6.

// Handoff events (§13.3). Each is written in the transaction that caused it
// and published after the commit, like every other.
const (
	// EventHandoffCreated is a handoff's create. Payload `{id, task_id,
	// issue_ids, created_by, trigger_id?}`.
	EventHandoffCreated = "handoff.created"
	// EventHandoffItemChanged is one item's outcome changing. Payload
	// `{handoff_id, issue_id, from, to, by}`; from and to are "" for an
	// unresolved item.
	EventHandoffItemChanged = "handoff.item_changed"
	// EventHandoffSettled is a handoff reaching its settled state — its
	// task settled and no item waiting. Payload `{id, task_id}`.
	EventHandoffSettled = "handoff.settled"
)

// HandoffOrigin is who created a handoff: the `created_by` column.
type HandoffOrigin string

// Handoff origins.
const (
	HandoffByHuman   HandoffOrigin = "human"
	HandoffByAgent   HandoffOrigin = "agent"
	HandoffByTrigger HandoffOrigin = "trigger"
)

// HandoffOutcome is what became of one claimed issue. The empty outcome is
// an unresolved item: the claim is live and nothing has been decided.
type HandoffOutcome string

// Handoff item outcomes (task 137 decision 2). The column carries no CHECK,
// so a later outcome needs a row here, not a table rebuild.
const (
	HandoffUnresolved HandoffOutcome = ""
	// HandoffDelivered is the issue's delivery integrated. An item
	// delivered against a delivery keeps the issue out of every later
	// handoff until a new delivery replaces it.
	HandoffDelivered HandoffOutcome = "delivered"
	// HandoffReturned is the issue sent back to its workshop.
	HandoffReturned HandoffOutcome = "returned"
	// HandoffWaiting is the issue parked on something outside the
	// handoff; it keeps its claim after the task settles.
	HandoffWaiting HandoffOutcome = "waiting"
	// HandoffReleased is the claim let go with no verdict.
	HandoffReleased HandoffOutcome = "released"
)

// HandoffOutcomes lists every resolved outcome.
var HandoffOutcomes = []HandoffOutcome{HandoffDelivered, HandoffReturned, HandoffWaiting, HandoffReleased}

// ValidHandoffOutcome reports whether o is a resolved outcome.
func ValidHandoffOutcome(o HandoffOutcome) bool { return slices.Contains(HandoffOutcomes, o) }

// Claims reports whether an item with outcome o holds its issue's claim —
// the predicate idx_handoff_items_claim is written over.
func (o HandoffOutcome) Claims() bool { return o == HandoffUnresolved || o == HandoffWaiting }

// HandoffState is a handoff's derived state. It is never stored: it is read
// from the handoff's task and its items, so it cannot disagree with them.
type HandoffState string

// Handoff states.
const (
	// HandoffLive is a handoff whose task has not settled.
	HandoffLive HandoffState = "live"
	// HandoffWaitingState is a settled task with at least one waiting item.
	HandoffWaitingState HandoffState = "waiting"
	// HandoffSettled is everything else.
	HandoffSettled HandoffState = "settled"
)

// HandoffStateOf is handoffStateExpr in Go, the way issuestate.LaneOf
// mirrors laneExpr: the task's state and the items' outcomes decide it.
func HandoffStateOf(task TaskState, outcomes []HandoffOutcome) HandoffState {
	if !taskstate.Settled(task) {
		return HandoffLive
	}
	if slices.Contains(outcomes, HandoffWaiting) {
		return HandoffWaitingState
	}
	return HandoffSettled
}

// Handoff is one row of `handoffs`, with its derived state.
type Handoff struct {
	ID        int64
	ProjectID int64
	TaskID    int64
	// TriggerID names the trigger that created the handoff, or "".
	TriggerID string
	// GrantJSON is the issue-write grant (137.7); `[]` until then.
	GrantJSON string
	CreatedBy HandoffOrigin
	CreatedAt time.Time
	// State is derived on read; CreateHandoff ignores it.
	State HandoffState
}

// HandoffItem is one claimed issue of a handoff.
type HandoffItem struct {
	HandoffID int64
	IssueID   int64
	// DeliveryTaskID and Delivery are the issue's latest delivery when it
	// was claimed — a snapshot, unchanged by a later follow-up.
	DeliveryTaskID *int64
	Delivery       *Delivery
	Outcome        HandoffOutcome
	OutcomeAt      *time.Time
	OutcomeBy      issuestate.Actor
	Note           string
}

// ErrHandoffNotFound is a handoff id with no row; it wraps ErrNotFound.
var ErrHandoffNotFound = fmt.Errorf("handoff: %w", ErrNotFound)

// IssueNotEligibleError is CreateHandoff refusing an issue it may not claim
// (decision 2): closed, not in `hand_off`, already claimed, or its current
// delivery already delivered. IssueID is the first such issue.
type IssueNotEligibleError struct{ IssueID int64 }

func (e *IssueNotEligibleError) Error() string {
	return fmt.Sprintf("issue %d is not eligible for a handoff", e.IssueID)
}

// ErrIssueNotEligible is what IssueNotEligibleError matches with errors.Is.
var ErrIssueNotEligible = errors.New("issue is not eligible for a handoff")

// Is lets errors.Is match ErrIssueNotEligible.
func (e *IssueNotEligibleError) Is(target error) bool { return target == ErrIssueNotEligible }

// IssueInHandoffError is a write that would add work to an issue a live
// handoff item claims (decision 14): a root task created on it, or a
// follow-up re-admitting one of its tasks. The API answers it with 409
// `issue_in_handoff`.
type IssueInHandoffError struct {
	IssueID   int64
	HandoffID int64
}

func (e *IssueInHandoffError) Error() string {
	return fmt.Sprintf("issue %d is claimed by handoff %d", e.IssueID, e.HandoffID)
}

// ErrIssueInHandoff is what IssueInHandoffError matches with errors.Is.
var ErrIssueInHandoff = errors.New("issue is claimed by a handoff")

// Is lets errors.Is match ErrIssueInHandoff.
func (e *IssueInHandoffError) Is(target error) bool { return target == ErrIssueInHandoff }

// claimCond matches a live item on alias hi: unresolved or waiting. It is
// the predicate of idx_handoff_items_claim, spelled once.
const claimCond = `(hi.outcome IS NULL OR hi.outcome = 'waiting')`

// claimedHandoffExpr is the id of the live handoff claiming issue `i`, or
// NULL. The unique index makes it at most one row.
func claimedHandoffExpr() sqlFrag {
	return sqlFrag{sql: `(SELECT hi.handoff_id FROM handoff_items hi WHERE hi.issue_id = i.id AND ` + claimCond + `)`}
}

// handoffEligibleExpr is whether a handoff may claim issue `i` (decision 2),
// as 1 or 0. It is the one rule 137.5's `select:` and 137.10's marker read:
// the issue is in the `hand_off` lane — which laneExpr only yields for an
// open issue — no live item claims it, and no `delivered` item names its
// current delivery, which is the task latestDeliveryExpr picks at the
// head_sha that delivery carries. A follow-up that moves the head makes the
// issue eligible again; one that adds no commits does not. The lane is
// laneExpr's, so when 137.15 makes `hand_off` mean readiness, eligibility
// follows without an edit here.
func handoffEligibleExpr() sqlFrag {
	lane := laneExpr()
	claimed := claimedHandoffExpr()
	taskID := latestDeliveryTaskExpr()
	delivery := latestDeliveryExpr()
	args := slices.Clone(lane.args)
	args = append(args, string(issuestate.LaneHandOff))
	args = append(args, claimed.args...)
	args = append(args, taskID.args...)
	args = append(args, delivery.args...)
	return sqlFrag{
		sql: `(` + lane.sql + ` = ? AND ` + claimed.sql + ` IS NULL
			AND NOT EXISTS (SELECT 1 FROM handoff_items hd WHERE hd.issue_id = i.id AND hd.outcome = 'delivered'
				AND hd.delivery_task_id = ` + taskID.sql + `
				AND json_extract(hd.delivery_json, '$.head_sha') IS json_extract(` + delivery.sql + `, '$.head_sha')))`,
		args: args,
	}
}

// handoffStateExpr is HandoffStateOf in SQL over the handoff alias `h`.
func handoffStateExpr() sqlFrag {
	settled := settledTaskStates()
	args := slices.Clone(settled)
	args = append(args, string(HandoffLive), string(HandoffWaiting), string(HandoffWaitingState), string(HandoffSettled))
	return sqlFrag{
		sql: `(CASE WHEN EXISTS (SELECT 1 FROM tasks ht WHERE ht.id = h.task_id AND ht.state NOT IN ` +
			placeholders(len(settled)) + `) THEN ?
			WHEN EXISTS (SELECT 1 FROM handoff_items hw WHERE hw.handoff_id = h.id AND hw.outcome = ?) THEN ?
			ELSE ? END)`,
		args: args,
	}
}

// issueClaimTx returns the live handoff claiming an issue and that
// handoff's task, or ok false when nothing claims it.
func issueClaimTx(ctx context.Context, tx *sql.Tx, issueID int64) (handoffID, taskID int64, ok bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT h.id, h.task_id FROM handoff_items hi JOIN handoffs h ON h.id = hi.handoff_id
		WHERE hi.issue_id = ? AND `+claimCond, issueID).Scan(&handoffID, &taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("read issue %d claim: %w", issueID, err)
	}
	return handoffID, taskID, true, nil
}

// refuseClaimedIssueTx is decision 14's refusal, inside the transaction of
// the write it guards, so it cannot race a handoff create that commits
// between a check and the insert. acting is the task making the write, when
// one is: the claiming handoff's own task may add work to its issues. It is
// best-effort, like every marker it is read from (spec §16) — the refusal
// is a consistency guard, not a privilege boundary.
func refuseClaimedIssueTx(ctx context.Context, tx *sql.Tx, issueID int64, acting *int64) error {
	handoffID, taskID, ok, err := issueClaimTx(ctx, tx, issueID)
	if err != nil || !ok {
		return err
	}
	if acting != nil && *acting == taskID {
		return nil
	}
	return &IssueInHandoffError{IssueID: issueID, HandoffID: handoffID}
}

// HandoffSelect is a create's `select:` (task 137 decision 8): the eligible
// issues of the handoff's project, filtered and ordered in SQL inside the
// create transaction, so the board, the CLI and a trigger cannot disagree
// about what was taken. An empty list matches everything.
type HandoffSelect struct {
	// Delivery is the latest delivery's kind, any of them.
	Delivery []string
	// Kinds is the issue's kind, any of them.
	Kinds []string
	// Labels must all be carried, case-insensitively, as IssueFilter.Labels.
	Labels []string
	// Max is a LIMIT; 0 takes every eligible issue.
	Max int
}

// ErrNothingToHandOff is a selection that found no eligible issue. The
// create rolls back, so no task is left behind; the API answers it 409
// `nothing_to_hand_off`.
var ErrNothingToHandOff = errors.New("no eligible issue to hand off")

// Capability is one entry of a handoff's grant (task 137 decision 6): a
// closed vocabulary over existing forge-write routes. 137.5 validates and
// stores it; 137.7's guard is the first reader.
type Capability string

// Grant capabilities.
const (
	CapIssueComment Capability = "issue.comment"
	CapIssueClose   Capability = "issue.close"
	CapPullCreate   Capability = "pull.create"
	CapPullComment  Capability = "pull.comment"
	CapPullMerge    Capability = "pull.merge"
	CapPullClose    Capability = "pull.close"
	CapChecksRerun  Capability = "checks.rerun"
)

// Capabilities lists the grant vocabulary in its documented order.
var Capabilities = []Capability{
	CapIssueComment, CapIssueClose, CapPullCreate, CapPullComment, CapPullMerge, CapPullClose, CapChecksRerun,
}

// GrantJSON validates a grant and encodes it for `grant_json`: every entry
// must be in Capabilities, and the result is deduplicated and in the
// vocabulary's order, so two spellings of one grant store the same text.
func GrantJSON(grant []string) (string, error) {
	out := make([]Capability, 0, len(grant))
	for _, g := range grant {
		c := Capability(strings.TrimSpace(g))
		if !slices.Contains(Capabilities, c) {
			return "", fmt.Errorf("unknown grant capability %q", g)
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Capability) int {
		return slices.Index(Capabilities, a) - slices.Index(Capabilities, b)
	})
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode grant: %w", err)
	}
	return string(b), nil
}

// Grant decodes h's grant_json; a malformed column reads as empty.
func (h *Handoff) Grant() []Capability {
	var out []Capability
	_ = json.Unmarshal([]byte(h.GrantJSON), &out)
	return out
}

// NewHandoff is one handoff create: the row to fill in, its task, and the
// issues to claim — IssueIDs or Select, exactly one.
type NewHandoff struct {
	Handoff  *Handoff
	Task     *Task
	IssueIDs []int64
	Select   *HandoffSelect
	// ResolveBranch is insertTaskTx's, for a name that needs the task id.
	ResolveBranch func(id int64) (string, error)
	// Key, when non-nil, is an Idempotency-Key recorded against the
	// handoff's task id in the create transaction (task 040's table, path
	// `/v1/handoffs`).
	Key *IdempotencyKey
	// TitleFor, when non-nil and the task has no title, names the task from
	// the number of issues claimed — known only once a selection has run.
	TitleFor func(n int) string
}

// CreateHandoff creates a handoff, its task and one item per issue in one
// transaction (decision 2), and records handoff.created with it. task is
// inserted through insertTaskTx and must carry no issue: a handoff task
// never moves an issue's lane (task 134 decision 2). Each issue is
// re-checked against handoffEligibleExpr inside the transaction, and its
// latest delivery snapshotted onto the item. The first ineligible issue —
// or one whose insert trips the claim index — fails the whole create with
// *IssueNotEligibleError.
//
// h is filled in: id, project, task id and timestamp.
func (s *Store) CreateHandoff(
	ctx context.Context, h *Handoff, issueIDs []int64, task *Task,
	resolveBranch func(id int64) (string, error),
) error {
	_, err := s.StartHandoff(ctx, NewHandoff{Handoff: h, Task: task, IssueIDs: issueIDs, ResolveBranch: resolveBranch})
	return err
}

// StartHandoff is CreateHandoff over either explicit ids or a selection,
// with an optional idempotency key, and returns the claimed issue ids in
// claim order. A selection is evaluated inside the transaction, so an issue
// another handoff claims between two creates is never taken twice; one that
// finds nothing is ErrNothingToHandOff with everything rolled back.
func (s *Store) StartHandoff(ctx context.Context, in NewHandoff) ([]int64, error) {
	h, task := in.Handoff, in.Task
	if task == nil || task.IssueID != nil || task.ParentTaskID != nil {
		return nil, errors.New("create handoff: the handoff task must be a root task with no issue")
	}
	if (in.Select == nil) == (len(in.IssueIDs) == 0) {
		return nil, errors.New("create handoff: exactly one of issue ids and a selection is required")
	}
	if h.GrantJSON == "" {
		h.GrantJSON = "[]"
	}
	now := time.Now()
	var (
		taskEv, ev *Event
		issueIDs   []int64
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		issueIDs = in.IssueIDs
		if in.Select != nil {
			if issueIDs, err = selectHandoffIssuesTx(ctx, tx, task.ProjectID, in.Select); err != nil {
				return err
			}
			if len(issueIDs) == 0 {
				return ErrNothingToHandOff
			}
		}
		if task.Title == "" && in.TitleFor != nil {
			task.Title = in.TitleFor(len(issueIDs))
		}
		if taskEv, err = insertTaskTx(ctx, tx, task, now, in.ResolveBranch); err != nil {
			return err
		}
		if in.Key != nil {
			if err := insertIdempotencyKeyTx(ctx, tx, in.Key, task.ID, now); err != nil {
				return err
			}
		}
		h.ProjectID, h.TaskID, h.CreatedAt = task.ProjectID, task.ID, now
		res, err := tx.ExecContext(ctx, `
			INSERT INTO handoffs (project_id, task_id, trigger_id, grant_json, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			h.ProjectID, h.TaskID, nullString(h.TriggerID), h.GrantJSON, string(h.CreatedBy), formatTime(now))
		if err != nil {
			return fmt.Errorf("insert handoff: %w", err)
		}
		if h.ID, err = res.LastInsertId(); err != nil {
			return fmt.Errorf("insert handoff: %w", err)
		}
		for _, issueID := range issueIDs {
			if err := insertHandoffItemTx(ctx, tx, h, issueID); err != nil {
				return err
			}
		}
		h.State = HandoffLive
		body := map[string]any{"id": h.ID, "task_id": h.TaskID, "issue_ids": issueIDs, "created_by": string(h.CreatedBy)}
		if h.TriggerID != "" {
			body["trigger_id"] = h.TriggerID
		}
		ev, err = handoffEvent(EventHandoffCreated, h, body)
		if err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return nil, err
	}
	s.notify(taskEv)
	s.notify(ev)
	return issueIDs, nil
}

// selectHandoffIssuesTx is a selection's query: the project's eligible
// issues, filtered by sel, ordered by issue priority — urgent (1) first, 4
// last of the set ones, then 0 ("none") after them — then by the oldest
// delivery, the delivering task's finished_at, then by id, so the order is
// total; and limited by sel.Max.
func selectHandoffIssuesTx(ctx context.Context, tx *sql.Tx, projectID int64, sel *HandoffSelect) ([]int64, error) {
	eligible, delivery, finished := handoffEligibleExpr(), latestDeliveryExpr(), latestDeliveryColumn("finished_at")
	//nolint:gosec // G202: the fragments are constant SQL with bind markers; every value binds
	q := `SELECT i.id FROM issues i WHERE i.project_id = ? AND ` + eligible.sql + ` = 1`
	args := []any{projectID}
	args = append(args, eligible.args...)
	if len(sel.Delivery) > 0 {
		q += ` AND json_extract(` + delivery.sql + `, '$.kind') IN ` + placeholders(len(sel.Delivery))
		args = append(args, delivery.args...)
		for _, d := range sel.Delivery {
			args = append(args, d)
		}
	}
	if len(sel.Kinds) > 0 {
		q += ` AND i.kind IN ` + placeholders(len(sel.Kinds))
		for _, k := range sel.Kinds {
			args = append(args, k)
		}
	}
	for _, name := range sel.Labels {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// labels.name is COLLATE NOCASE; one EXISTS per name is the AND, as
		// in ListIssues.
		q += ` AND EXISTS (SELECT 1 FROM issue_labels il JOIN labels l ON l.id = il.label_id
			WHERE il.issue_id = i.id AND l.name = ?)`
		args = append(args, name)
	}
	q += ` ORDER BY CASE WHEN i.priority = 0 THEN 1 ELSE 0 END, i.priority, ` + finished.sql + `, i.id`
	args = append(args, finished.args...)
	if sel.Max > 0 {
		q += ` LIMIT ?`
		args = append(args, sel.Max)
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("select handoff issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan handoff issue: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("select handoff issues: %w", err)
	}
	return ids, nil
}

// insertHandoffItemTx claims one issue for h, snapshotting its delivery.
func insertHandoffItemTx(ctx context.Context, tx *sql.Tx, h *Handoff, issueID int64) error {
	eligible, taskID, delivery := handoffEligibleExpr(), latestDeliveryTaskExpr(), latestDeliveryExpr()
	args := slices.Clone(eligible.args)
	args = append(args, taskID.args...)
	args = append(args, delivery.args...)
	args = append(args, issueID, h.ProjectID)
	var (
		ok      bool
		deliver sql.NullInt64
		raw     sql.NullString
	)
	err := tx.QueryRowContext(ctx, `SELECT `+eligible.sql+`, `+taskID.sql+`, `+delivery.sql+`
		FROM issues i WHERE i.id = ? AND i.project_id = ?`, args...).Scan(&ok, &deliver, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return &IssueNotEligibleError{IssueID: issueID}
	}
	if err != nil {
		return fmt.Errorf("read issue %d eligibility: %w", issueID, err)
	}
	if !ok {
		return &IssueNotEligibleError{IssueID: issueID}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO handoff_items (handoff_id, issue_id, delivery_task_id, delivery_json) VALUES (?, ?, ?, ?)`,
		h.ID, issueID, deliver, raw)
	if err != nil {
		// The claim index, or the same issue named twice: either way the
		// issue is not one this handoff may claim.
		if strings.Contains(err.Error(), "constraint failed") {
			return &IssueNotEligibleError{IssueID: issueID}
		}
		return fmt.Errorf("insert handoff item: %w", err)
	}
	return nil
}

// handoffEvent builds a handoff event: the project, and the handoff's task
// as the event's task, so the task's own stream carries it.
func handoffEvent(evType string, h *Handoff, body map[string]any) (*Event, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", evType, err)
	}
	pid, tid := h.ProjectID, h.TaskID
	return &Event{Type: evType, ProjectID: &pid, TaskID: &tid, Payload: payload}, nil
}

const handoffColumns = `h.id, h.project_id, h.task_id, h.trigger_id, h.grant_json, h.created_by, h.created_at`

// GetHandoff returns one handoff with its derived state.
func (s *Store) GetHandoff(ctx context.Context, id int64) (*Handoff, error) {
	return getHandoff(ctx, s.db, `h.id = ?`, id)
}

// GetHandoffByTask returns the handoff whose task is taskID.
func (s *Store) GetHandoffByTask(ctx context.Context, taskID int64) (*Handoff, error) {
	return getHandoff(ctx, s.db, `h.task_id = ?`, taskID)
}

func getHandoff(ctx context.Context, q issueQuerier, where string, arg int64) (*Handoff, error) {
	state := handoffStateExpr()
	row := q.QueryRowContext(ctx, `SELECT `+handoffColumns+`, `+state.sql+` FROM handoffs h WHERE `+where,
		append(slices.Clone(state.args), arg)...)
	h, err := scanHandoff(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w (%s %d)", ErrHandoffNotFound, where, arg)
	}
	if err != nil {
		return nil, fmt.Errorf("get handoff: %w", err)
	}
	return h, nil
}

// HandoffFilter narrows ListHandoffs; zero values match everything.
type HandoffFilter struct {
	ProjectID int64
	// States is any of the derived states.
	States []HandoffState
	// IssueID is a handoff with an item for this issue, live or not.
	IssueID int64
}

// ListHandoffs returns the handoffs f matches, newest first, without items.
func (s *Store) ListHandoffs(ctx context.Context, f HandoffFilter) ([]*Handoff, error) {
	state := handoffStateExpr()
	//nolint:gosec // G202: handoffColumns and the state fragment are constant SQL; every value binds
	q := `SELECT ` + handoffColumns + `, ` + state.sql + ` FROM handoffs h WHERE 1 = 1`
	args := slices.Clone(state.args)
	if f.ProjectID != 0 {
		q += ` AND h.project_id = ?`
		args = append(args, f.ProjectID)
	}
	if len(f.States) > 0 {
		q += ` AND ` + state.sql + ` IN ` + placeholders(len(f.States))
		args = append(args, state.args...)
		for _, st := range f.States {
			args = append(args, string(st))
		}
	}
	if f.IssueID != 0 {
		q += ` AND EXISTS (SELECT 1 FROM handoff_items hf WHERE hf.handoff_id = h.id AND hf.issue_id = ?)`
		args = append(args, f.IssueID)
	}
	q += ` ORDER BY h.created_at DESC, h.id DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list handoffs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Handoff
	for rows.Next() {
		h, err := scanHandoff(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list handoffs: %w", err)
	}
	return out, nil
}

// scanHandoff reads one handoffColumns row followed by its derived state.
func scanHandoff(row interface{ Scan(...any) error }) (*Handoff, error) {
	var (
		h              Handoff
		trigger        sql.NullString
		createdAt, st  string
		createdByValue string
	)
	if err := row.Scan(&h.ID, &h.ProjectID, &h.TaskID, &trigger, &h.GrantJSON, &createdByValue, &createdAt, &st); err != nil {
		return nil, err
	}
	h.TriggerID, h.CreatedBy, h.State = trigger.String, HandoffOrigin(createdByValue), HandoffState(st)
	var err error
	if h.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("handoff %d created_at: %w", h.ID, err)
	}
	return &h, nil
}

// ListHandoffItems returns a handoff's items, by issue id, and the
// handoff's derived state, read in the same statement.
func (s *Store) ListHandoffItems(ctx context.Context, handoffID int64) ([]*HandoffItem, HandoffState, error) {
	h, err := s.GetHandoff(ctx, handoffID)
	if err != nil {
		return nil, "", err
	}
	items, err := listHandoffItems(ctx, s.db, handoffID)
	if err != nil {
		return nil, "", err
	}
	return items, h.State, nil
}

func listHandoffItems(ctx context.Context, q issueQuerier, handoffID int64) ([]*HandoffItem, error) {
	rows, err := q.QueryContext(ctx, `SELECT handoff_id, issue_id, delivery_task_id, delivery_json,
		outcome, outcome_at, outcome_by, note FROM handoff_items WHERE handoff_id = ? ORDER BY issue_id`, handoffID)
	if err != nil {
		return nil, fmt.Errorf("list handoff %d items: %w", handoffID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []*HandoffItem
	for rows.Next() {
		var (
			it                     HandoffItem
			deliver                sql.NullInt64
			raw, outcome, at, byAs sql.NullString
		)
		if err := rows.Scan(&it.HandoffID, &it.IssueID, &deliver, &raw, &outcome, &at, &byAs, &it.Note); err != nil {
			return nil, fmt.Errorf("scan handoff item: %w", err)
		}
		if deliver.Valid {
			it.DeliveryTaskID = &deliver.Int64
		}
		if it.Delivery, err = unmarshalDelivery(raw); err != nil {
			return nil, fmt.Errorf("handoff %d issue %d: %w", it.HandoffID, it.IssueID, err)
		}
		it.Outcome, it.OutcomeBy = HandoffOutcome(outcome.String), issuestate.Actor(byAs.String)
		if at.Valid {
			ts, err := parseTime(at.String)
			if err != nil {
				return nil, fmt.Errorf("handoff %d issue %d outcome_at: %w", it.HandoffID, it.IssueID, err)
			}
			it.OutcomeAt = &ts
		}
		out = append(out, &it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list handoff %d items: %w", handoffID, err)
	}
	return out, nil
}

// SetHandoffItemOutcome resolves one item — the primitive 137.6's verbs
// call. It writes outcome_at and records handoff.item_changed in the same
// transaction and, when the write leaves the handoff settled, handoff.settled
// after it. Setting the outcome an item already has is a no-op: nothing is
// written and nothing announced. Moving an item that no longer claims its
// issue back to one that does trips the claim index when another handoff
// has claimed the issue since, and fails with *IssueNotEligibleError.
func (s *Store) SetHandoffItemOutcome(
	ctx context.Context, handoffID, issueID int64, outcome HandoffOutcome, by issuestate.Actor, note string,
) (*HandoffItem, error) {
	if !ValidHandoffOutcome(outcome) {
		return nil, fmt.Errorf("unknown handoff outcome %q", outcome)
	}
	if err := checkActor(by); err != nil {
		return nil, err
	}
	var (
		out      *HandoffItem
		ev       *Event
		settleEv *Event
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		h, err := getHandoff(ctx, tx, `h.id = ?`, handoffID)
		if err != nil {
			return err
		}
		items, err := listHandoffItems(ctx, tx, handoffID)
		if err != nil {
			return err
		}
		idx := slices.IndexFunc(items, func(it *HandoffItem) bool { return it.IssueID == issueID })
		if idx < 0 {
			return fmt.Errorf("handoff %d issue %d: %w", handoffID, issueID, ErrNotFound)
		}
		it := items[idx]
		from := it.Outcome
		if from == outcome {
			out = it
			return nil
		}
		now := time.Now()
		_, err = tx.ExecContext(ctx, `UPDATE handoff_items SET outcome = ?, outcome_at = ?, outcome_by = ?, note = ?
			WHERE handoff_id = ? AND issue_id = ?`,
			string(outcome), formatTime(now), string(by), note, handoffID, issueID)
		if err != nil {
			if strings.Contains(err.Error(), "constraint failed") {
				return &IssueNotEligibleError{IssueID: issueID}
			}
			return fmt.Errorf("set handoff %d issue %d outcome: %w", handoffID, issueID, err)
		}
		it.Outcome, it.OutcomeAt, it.OutcomeBy, it.Note = outcome, &now, by, note
		out = it
		ev, err = handoffEvent(EventHandoffItemChanged, h, map[string]any{
			"handoff_id": handoffID, "issue_id": issueID, "from": string(from), "to": string(outcome), "by": string(by),
		})
		if err != nil {
			return err
		}
		if err := appendEventTx(ctx, tx, ev); err != nil {
			return err
		}
		after, err := getHandoff(ctx, tx, `h.id = ?`, handoffID)
		if err != nil {
			return err
		}
		if h.State != HandoffSettled && after.State == HandoffSettled {
			settleEv, err = handoffSettledTx(ctx, tx, after)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	s.notify(ev)
	s.notify(settleEv)
	return out, nil
}

// handoffSettledTx appends handoff.settled for h.
func handoffSettledTx(ctx context.Context, tx *sql.Tx, h *Handoff) (*Event, error) {
	ev, err := handoffEvent(EventHandoffSettled, h, map[string]any{"id": h.ID, "task_id": h.TaskID})
	if err != nil {
		return nil, err
	}
	if err := appendEventTx(ctx, tx, ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// handoffTaskSettleTx is the settle hook of a task transition: when the
// transition settles a handoff's task and leaves the handoff settled — no
// item waiting — it appends handoff.settled after cause and chains it so the
// caller's one notify publishes it. Unresolved items keep their claim here;
// releasing them on settle is 137.6's.
func handoffTaskSettleTx(ctx context.Context, tx *sql.Tx, cause *Event, taskID int64, from, to TaskState) error {
	if taskstate.Settled(from) || !taskstate.Settled(to) {
		return nil
	}
	h, err := getHandoff(ctx, tx, `h.task_id = ?`, taskID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil || h.State != HandoffSettled {
		return err
	}
	ev, err := handoffSettledTx(ctx, tx, h)
	if err != nil {
		return err
	}
	tail := cause
	for tail.follow != nil {
		tail = tail.follow
	}
	tail.follow = ev
	return nil
}

// issueHandoffKey is the context key WithIssueHandoff stores under.
type issueHandoffKey struct{}

// WithIssueHandoff marks the issue writes made with ctx as made by handoff
// id (task 137.4): TransitionIssue and AddIssueComment then carry
// `handoff_id` on the issue event they record. It rides the context rather
// than every signature because only 137.6's and 137.7's handoff paths set
// it, and every other caller stays as it is.
func WithIssueHandoff(ctx context.Context, handoffID int64) context.Context {
	return context.WithValue(ctx, issueHandoffKey{}, handoffID)
}

// issueHandoffExtra adds ctx's handoff id, if any, to an issue event's extra
// payload.
func issueHandoffExtra(ctx context.Context, extra map[string]any) map[string]any {
	id, ok := ctx.Value(issueHandoffKey{}).(int64)
	if !ok {
		return extra
	}
	if extra == nil {
		extra = map[string]any{}
	}
	extra["handoff_id"] = id
	return extra
}

// IssueClaim returns the live handoff in projectID claiming issueID, or ok
// false when none does. It is scoped to the project so a refusal naming the
// claim cannot reveal another project's handoff.
func (s *Store) IssueClaim(ctx context.Context, projectID, issueID int64) (handoffID int64, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT h.id FROM handoff_items hi JOIN handoffs h ON h.id = hi.handoff_id
		WHERE hi.issue_id = ? AND h.project_id = ? AND `+claimCond, issueID, projectID).Scan(&handoffID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read issue %d claim: %w", issueID, err)
	}
	return handoffID, true, nil
}
