package taskrun

// Lifecycle over a fan-out subtree (spec §7.6, §10, task 014 decision 11).
//
// Cancel cascades; archive refuses while anything is unfinished and then
// cascades. Both walk depth-first, deepest first, so a parent is never ended
// before its own children are.
//
// Neither destroys work. A cancelled lane keeps its branch and its worktree —
// it is stopped, not erased — and archive removes worktrees under exactly the
// rules §10 already applies to any task, dirty-worktree confirmation
// included.

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// UnfinishedDescendantsError reports an archive refused because part of the
// subtree is still working. It is its own type because the API answers 409
// with the ids: "this task has two lanes still running" is something the
// caller can act on, unlike a bare conflict.
type UnfinishedDescendantsError struct {
	TaskID int64
	IDs    []int64
}

func (e *UnfinishedDescendantsError) Error() string {
	return fmt.Sprintf("task %d has %d unfinished fan-out %s: %v",
		e.TaskID, len(e.IDs), plural("lane", len(e.IDs)), e.IDs)
}

func plural(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// AsUnfinishedDescendants extracts an *UnfinishedDescendantsError from err.
func AsUnfinishedDescendants(err error) (*UnfinishedDescendantsError, bool) {
	var e *UnfinishedDescendantsError
	return e, errors.As(err, &e)
}

// cascadeCancel cancels every unsettled descendant of a task, deepest first.
//
// Nothing should keep burning agent time for a join that will never happen.
// The branches and worktrees survive, so no work is destroyed — only stopped.
func (r *Runner) cascadeCancel(ctx context.Context, id int64) {
	ids, err := r.deps.Store.NonTerminalDescendants(ctx, id)
	if err != nil {
		r.deps.Logger.Error("cancel: list descendants", "task", id, "error", err)
		return
	}
	for _, childID := range ids {
		if _, err := r.Cancel(r.persistCtx(), childID); err != nil {
			if _, invalid := AsInvalidAction(err); invalid {
				continue // it settled between the list and the write
			}
			r.deps.Logger.Error("cancel: cascade to lane", "task", id, "child", childID, "error", err)
			continue
		}
		r.deps.Logger.Info("cancelled fan-out lane", "parent", id, "child", childID)
	}
}

// refuseUnfinishedDescendants reports an error when any descendant is still
// working, which is what archive checks before it touches anything.
//
// Archive of a parent is archive of its whole subtree, and a lane still
// running would have its worktree pulled out from under it.
func (r *Runner) refuseUnfinishedDescendants(ctx context.Context, id int64) error {
	ids, err := r.deps.Store.NonTerminalDescendants(ctx, id)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		return &UnfinishedDescendantsError{TaskID: id, IDs: ids}
	}
	return nil
}

// cascadeArchive archives every settled descendant, deepest first, with the
// same `force` the parent's archive was given.
//
// Per-child dirty confirmation is not re-implemented here: each child goes
// through the ordinary Archive, so §10's refusal of a dirty worktree without
// explicit confirmation applies to each of them exactly as it does to any
// task. A conflict-blocked lane is dirty by construction, and that is the
// correct behaviour rather than a case to carve out (decision 8).
func (r *Runner) cascadeArchive(ctx context.Context, id int64, force bool) error {
	children, err := r.deps.Store.ListChildren(ctx, id)
	if err != nil {
		return err
	}
	for _, child := range children {
		// Depth first: a lane that fanned out again owns its own subtree.
		if err := r.cascadeArchive(ctx, child.ID, force); err != nil {
			return err
		}
		if child.State == store.TaskArchived {
			continue
		}
		if !taskstate.Can(child.State, taskstate.Archive) {
			// Refused earlier by refuseUnfinishedDescendants; reaching here
			// means it moved in between, and stopping is better than
			// archiving half a tree.
			return &UnfinishedDescendantsError{TaskID: id, IDs: []int64{child.ID}}
		}
		if _, _, err := r.Archive(ctx, child.ID, force); err != nil {
			return fmt.Errorf("archive lane %d: %w", child.ID, err)
		}
		r.deps.Logger.Info("archived fan-out lane", "parent", id, "child", child.ID)
	}
	return nil
}

// cascadeRetry re-admits every blocked descendant of a task, and reports how
// many it re-admitted (task 088).
//
// A parent parked in `awaiting_children` has nothing of its own to retry: the
// join is held open by lanes below it, and before this the only action §6
// offered from that state was `cancel`, which ends work instead of resuming
// it. Recovery was a manual walk of the lane tree, once per lane and once per
// level to `fan_out.max_depth`.
//
// The walk needs no new query and no recursion here. `ChildrenOf`'s recursive
// CTE already returns the whole subtree at any depth, so `rollup.Blocked` is
// exactly the set to re-admit — nested fan-outs included. The ids are sorted
// first because the CTE has no ORDER BY, and a deterministic order is
// something a gate can assert.
//
// A descendant in `awaiting_children` is deliberately absent from that set and
// is left parked. It needs no help: a parent re-admitted while its own lanes
// are unsettled parks again through parkForUnsettled under barrier mode, and
// reads an unsettled lane as "not yet" in mergeSet under eager. Whatever order
// the scheduler picks, the tree converges.
func (r *Runner) cascadeRetry(ctx context.Context, id int64) (int, error) {
	rollup, err := r.deps.Store.ChildrenOf(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("retry: list descendants of task %d: %w", id, err)
	}
	blocked := slices.Clone(rollup.Blocked)
	slices.Sort(blocked)
	count := 0
	for _, childID := range blocked {
		// persistCtx per child, as cascadeCancel does: a client that
		// disconnects mid-cascade must not leave half the tree re-admitted.
		_, n, err := r.Retry(r.persistCtx(), childID, store.Override{})
		if err != nil {
			if _, invalid := AsInvalidAction(err); invalid {
				continue // it moved between the rollup and the write
			}
			r.deps.Logger.Error("retry: cascade to lane", "task", id, "child", childID, "error", err)
			continue
		}
		// One for the lane, plus whatever its own retry reached: a blocked
		// lane that had itself fanned out and blocked again re-admits its own
		// subtree. By the time this walk reaches that grandchild it is no
		// longer blocked, and the InvalidActionError skip above keeps it from
		// being counted twice.
		count += 1 + n
		r.deps.Logger.Info("retried fan-out lane", "parent", id, "child", childID)
	}
	return count, nil
}
