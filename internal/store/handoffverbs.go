package store

// The handoff item verbs of task 137.6 (spec §5.7, task 137 decision 4 and
// its 2026-10-28 amendment): deliver, return, wait and release, each one
// transaction that records the outcome together with every side effect it
// asks for — a comment, a close, readiness withdrawn, a follow-up queued, a
// side task created — so a refused side effect records nothing at all.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// HandoffOutcomeBy is who resolved a handoff item: the `outcome_by` column.
// It is wider than issuestate.Actor by one — `settle`, the release of every
// unresolved item when the handoff's task settles.
type HandoffOutcomeBy string

// Outcome actors. The first four spell issuestate's actors, so the closing
// actor of a wait resolved on close converts directly.
const (
	HandoffByHumanActor HandoffOutcomeBy = HandoffOutcomeBy(issuestate.Human)
	HandoffByAgentActor HandoffOutcomeBy = HandoffOutcomeBy(issuestate.Agent)
	HandoffBySync       HandoffOutcomeBy = HandoffOutcomeBy(issuestate.Sync)
	// HandoffByHandoff is the handoff's own task resolving its item.
	HandoffByHandoff HandoffOutcomeBy = HandoffOutcomeBy(issuestate.Handoff)
	// HandoffBySettle is release on settle.
	HandoffBySettle HandoffOutcomeBy = "settle"
)

// HandoffOutcomeBys lists every outcome actor.
var HandoffOutcomeBys = []HandoffOutcomeBy{
	HandoffByHumanActor, HandoffByAgentActor, HandoffBySync, HandoffByHandoff, HandoffBySettle,
}

// ValidHandoffOutcomeBy reports whether b is a known outcome actor.
func ValidHandoffOutcomeBy(b HandoffOutcomeBy) bool { return slices.Contains(HandoffOutcomeBys, b) }

// HandoffItemResolvedError refuses a verb on an item that is already final —
// delivered, returned or released — or, on a handoff no longer live, one
// that is not waiting. The API answers it 409 `handoff_item_resolved`.
type HandoffItemResolvedError struct {
	HandoffID int64
	IssueID   int64
	Outcome   HandoffOutcome
}

func (e *HandoffItemResolvedError) Error() string {
	if e.Outcome == HandoffUnresolved {
		return fmt.Sprintf("handoff %d is settled: issue %d can no longer be resolved", e.HandoffID, e.IssueID)
	}
	return fmt.Sprintf("handoff %d issue %d is already %s", e.HandoffID, e.IssueID, e.Outcome)
}

// ErrHandoffItemResolved is what HandoffItemResolvedError matches with
// errors.Is.
var ErrHandoffItemResolved = errors.New("handoff item is already resolved")

// Is lets errors.Is match ErrHandoffItemResolved.
func (e *HandoffItemResolvedError) Is(target error) bool { return target == ErrHandoffItemResolved }

// HandoffFollowUp is a return's follow-up, prepared by the caller: the §6
// transition of the issue's latest main-role task, applied inside the
// return's transaction after the claim is released.
type HandoffFollowUp struct {
	TaskID   int64
	From, To TaskState
	Change   TaskChange
}

// HandoffSideTask is a return's `--side` task, prepared by the caller as a
// task create is: inserted inside the return's transaction.
type HandoffSideTask struct {
	Task          *Task
	ResolveBranch func(id int64) (string, error)
}

// HandoffVerb is one verb over one or more items of a handoff.
type HandoffVerb struct {
	HandoffID int64
	// IssueIDs are the items, in the order given; All takes every
	// unresolved item instead (`waiting` ones are left alone).
	IssueIDs []int64
	All      bool
	Outcome  HandoffOutcome
	By       HandoffOutcomeBy
	// IssueActor is the actor of the issue writes the verb makes.
	IssueActor issuestate.Actor
	Note       string
	// Comment, when not empty, is posted on each issue as CommentAuthor
	// before anything else is written to it.
	Comment, CommentAuthor string
	// Close closes each issue after the comment, with CloseReason, or —
	// when that is empty — the delivery's suggested close_reason, else
	// completed.
	Close       bool
	CloseReason issuestate.Reason
	// Check, when set, is asked about every item's issue before anything is
	// written; an error refuses the whole verb.
	Check func(iss *Issue) error
	// FollowUps and SideTasks are a return's, by issue id.
	FollowUps map[int64]*HandoffFollowUp
	SideTasks map[int64]*HandoffSideTask
}

// HandoffVerbResult is what a verb did: the handoff re-read and the items
// it moved.
type HandoffVerbResult struct {
	Handoff *Handoff
	Items   []*HandoffItem
	// Kick reports an outbox write-back was enqueued.
	Kick bool
}

// HandoffVerbItems resolves the items v names before any write — the
// read-only half of a verb, so a caller can prepare per-issue follow-ups
// against the same set. A repeat of an item's own outcome is included; the
// verb itself leaves it alone.
func (s *Store) HandoffVerbItems(ctx context.Context, v HandoffVerb) ([]*HandoffItem, error) {
	var out []*HandoffItem
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		h, err := getHandoff(ctx, tx, `h.id = ?`, v.HandoffID)
		if err != nil {
			return err
		}
		out, err = verbItemsTx(ctx, tx, h, v)
		return err
	})
	return out, err
}

// verbItemsTx picks and judges v's items.
func verbItemsTx(ctx context.Context, tx *sql.Tx, h *Handoff, v HandoffVerb) ([]*HandoffItem, error) {
	items, err := listHandoffItems(ctx, tx, h.ID)
	if err != nil {
		return nil, err
	}
	var picked []*HandoffItem
	if v.All {
		for _, it := range items {
			if it.Outcome == HandoffUnresolved {
				picked = append(picked, it)
			}
		}
	} else {
		for _, id := range v.IssueIDs {
			idx := slices.IndexFunc(items, func(it *HandoffItem) bool { return it.IssueID == id })
			if idx < 0 {
				return nil, fmt.Errorf("handoff %d has no item for issue %d: %w", h.ID, id, ErrNotFound)
			}
			if !slices.Contains(picked, items[idx]) {
				picked = append(picked, items[idx])
			}
		}
	}
	for _, it := range picked {
		switch {
		case it.Outcome == v.Outcome:
		case it.Outcome != HandoffUnresolved && it.Outcome != HandoffWaiting:
			return nil, &HandoffItemResolvedError{HandoffID: h.ID, IssueID: it.IssueID, Outcome: it.Outcome}
		case h.State != HandoffLive && it.Outcome != HandoffWaiting:
			return nil, &HandoffItemResolvedError{HandoffID: h.ID, IssueID: it.IssueID, Outcome: it.Outcome}
		}
	}
	return picked, nil
}

// ResolveHandoffItems applies v in one transaction (task 137.6 decision 1):
// every item is judged and checked first, then, item by item, the comment,
// the close, the outcome, and — for a return — readiness withdrawn, the
// follow-up and the side task. Any failure rolls back all of it. An item
// whose outcome is already v's is left untouched, side effects included.
func (s *Store) ResolveHandoffItems(ctx context.Context, v HandoffVerb) (*HandoffVerbResult, error) {
	if !ValidHandoffOutcome(v.Outcome) {
		return nil, fmt.Errorf("unknown handoff outcome %q", v.Outcome)
	}
	if !ValidHandoffOutcomeBy(v.By) {
		return nil, fmt.Errorf("unknown handoff outcome actor %q", v.By)
	}
	if err := checkActor(v.IssueActor); err != nil {
		return nil, err
	}
	ctx = WithIssueHandoff(ctx, v.HandoffID)
	res := &HandoffVerbResult{}
	var evs []*Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		evs, res.Items, res.Kick = nil, nil, false
		// publish takes an event its writer already appended; emit appends.
		publish := func(e *Event) { evs = append(evs, e) }
		emit := func(es ...*Event) error {
			for _, e := range es {
				if e == nil {
					continue
				}
				if err := appendEventTx(ctx, tx, e); err != nil {
					return err
				}
				evs = append(evs, e)
			}
			return nil
		}
		h, err := getHandoff(ctx, tx, `h.id = ?`, v.HandoffID)
		if err != nil {
			return err
		}
		picked, err := verbItemsTx(ctx, tx, h, v)
		if err != nil {
			return err
		}
		issues := make(map[int64]*Issue, len(picked))
		for _, it := range picked {
			if it.Outcome == v.Outcome {
				continue
			}
			iss, err := getIssue(ctx, tx, it.IssueID)
			if err != nil {
				return err
			}
			if v.Check != nil {
				if err := v.Check(iss); err != nil {
					return err
				}
			}
			issues[it.IssueID] = iss
		}
		now := time.Now()
		for _, it := range picked {
			iss, moved := issues[it.IssueID]
			if !moved {
				res.Items = append(res.Items, it)
				continue
			}
			if v.Comment != "" {
				c := &IssueComment{IssueID: iss.ID, Author: v.CommentAuthor, Body: v.Comment, CreatedAt: now, UpdatedAt: now}
				ev, enq, err := addIssueCommentTx(ctx, tx, c, v.IssueActor)
				if err != nil {
					return err
				}
				res.Kick = res.Kick || enq
				if err := emit(ev); err != nil {
					return err
				}
			}
			if v.Close && issuestate.Normalize(iss.State) != issuestate.Closed {
				reason := v.CloseReason
				if reason == "" && it.Delivery != nil {
					reason = issuestate.Reason(it.Delivery.CloseReason)
				}
				if reason == "" {
					reason = issuestate.Completed
				}
				closed, enq, err := transitionIssueTx(ctx, tx, iss.ID, issuestate.Close, reason, nil, v.IssueActor, false)
				if err != nil {
					return err
				}
				res.Kick = res.Kick || enq
				if err := emit(closed...); err != nil {
					return err
				}
			}
			ev, err := updateItemOutcomeTx(ctx, tx, h, it, v.Outcome, v.By, v.Note, now)
			if err != nil {
				return err
			}
			if err := emit(ev); err != nil {
				return err
			}
			if v.Outcome == HandoffReturned {
				if err := returnItemTx(ctx, tx, v, iss, emit, publish); err != nil {
					return err
				}
			}
			res.Items = append(res.Items, it)
		}
		after, err := getHandoff(ctx, tx, `h.id = ?`, h.ID)
		if err != nil {
			return err
		}
		res.Handoff = after
		if h.State != HandoffSettled && after.State == HandoffSettled {
			ev, err := handoffEvent(EventHandoffSettled, after, map[string]any{"id": after.ID, "task_id": after.TaskID})
			if err != nil {
				return err
			}
			return emit(ev)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, e := range evs {
		s.notify(e)
	}
	if res.Kick {
		s.kickOutbox()
	}
	return res, nil
}

// returnItemTx is a return's tail for one issue, after its claim is
// released: readiness withdrawn, then the follow-up and the side task. The
// claim is gone by now, so neither trips task 137 decision 14's
// issue_in_handoff.
func returnItemTx(
	ctx context.Context, tx *sql.Tx, v HandoffVerb, iss *Issue, emit func(...*Event) error, publish func(*Event),
) error {
	withdrawn, err := withdrawReadinessTx(ctx, tx, iss.ID, iss.ProjectID, v.IssueActor)
	if err != nil {
		return err
	}
	if err := emit(withdrawn...); err != nil {
		return err
	}
	// transitionTaskTx and insertTaskTx append their own events, chained;
	// they are only published after the commit.
	if fu := v.FollowUps[iss.ID]; fu != nil {
		_, ev, err := transitionTaskTx(ctx, tx, fu.TaskID, fu.From, fu.To, fu.Change)
		if err != nil {
			return err
		}
		publish(ev)
	}
	if side := v.SideTasks[iss.ID]; side != nil {
		ev, err := insertTaskTx(ctx, tx, side.Task, time.Now(), side.ResolveBranch)
		if err != nil {
			return err
		}
		publish(ev)
	}
	return nil
}

// updateItemOutcomeTx writes one item's outcome and returns its
// handoff.item_changed, not yet appended. it is updated in place. A note
// left empty keeps the one the item had.
func updateItemOutcomeTx(
	ctx context.Context, tx *sql.Tx, h *Handoff, it *HandoffItem, to HandoffOutcome, by HandoffOutcomeBy,
	note string, now time.Time,
) (*Event, error) {
	if note == "" {
		note = it.Note
	}
	from := it.Outcome
	_, err := tx.ExecContext(ctx, `UPDATE handoff_items SET outcome = ?, outcome_at = ?, outcome_by = ?, note = ?
		WHERE handoff_id = ? AND issue_id = ?`,
		string(to), formatTime(now), string(by), note, it.HandoffID, it.IssueID)
	if err != nil {
		if strings.Contains(err.Error(), "constraint failed") {
			return nil, &IssueNotEligibleError{IssueID: it.IssueID}
		}
		return nil, fmt.Errorf("set handoff %d issue %d outcome: %w", it.HandoffID, it.IssueID, err)
	}
	it.Outcome, it.OutcomeAt, it.OutcomeBy, it.Note = to, &now, by, note
	return handoffEvent(EventHandoffItemChanged, h, map[string]any{
		"handoff_id": it.HandoffID, "issue_id": it.IssueID, "from": string(from), "to": string(to), "by": string(by),
	})
}

// resolveWaitingOnCloseTx resolves the issue's waiting item, if any, as
// delivered by the closing actor (task 137.6), and returns its
// handoff.item_changed and — when that settles its handoff —
// handoff.settled, not yet appended.
func resolveWaitingOnCloseTx(ctx context.Context, tx *sql.Tx, issueID int64, by HandoffOutcomeBy) ([]*Event, error) {
	var handoffID int64
	err := tx.QueryRowContext(ctx, `SELECT handoff_id FROM handoff_items WHERE issue_id = ? AND outcome = ?`,
		issueID, string(HandoffWaiting)).Scan(&handoffID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read issue %d waiting item: %w", issueID, err)
	}
	h, err := getHandoff(ctx, tx, `h.id = ?`, handoffID)
	if err != nil {
		return nil, err
	}
	items, err := listHandoffItems(ctx, tx, handoffID)
	if err != nil {
		return nil, err
	}
	idx := slices.IndexFunc(items, func(it *HandoffItem) bool { return it.IssueID == issueID })
	if idx < 0 {
		return nil, fmt.Errorf("handoff %d issue %d: %w", handoffID, issueID, ErrNotFound)
	}
	ev, err := updateItemOutcomeTx(ctx, tx, h, items[idx], HandoffDelivered, by, "", time.Now())
	if err != nil {
		return nil, err
	}
	out := []*Event{ev}
	after, err := getHandoff(ctx, tx, `h.id = ?`, handoffID)
	if err != nil {
		return nil, err
	}
	if h.State != HandoffSettled && after.State == HandoffSettled {
		settled, err := handoffEvent(EventHandoffSettled, after, map[string]any{"id": after.ID, "task_id": after.TaskID})
		if err != nil {
			return nil, err
		}
		out = append(out, settled)
	}
	return out, nil
}

// LatestIssueMainTask returns the issue's latest main-role root task — a
// merge-back included — or ErrNotFound: the task a return's follow-up lands
// on (task 137.6 decision 2).
func (s *Store) LatestIssueMainTask(ctx context.Context, issueID int64) (*Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE issue_id = ? AND parent_task_id IS NULL AND issue_worktree = ? ORDER BY id DESC LIMIT 1`,
		issueID, IssueWorktreeMain))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("issue %d has no main task: %w", issueID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("latest main task of issue %d: %w", issueID, err)
	}
	return t, nil
}
