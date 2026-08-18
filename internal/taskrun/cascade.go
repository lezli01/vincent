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
