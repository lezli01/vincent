package taskrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/worktree"
)

// ensureIssueMainWorktree is ensureWorktree's fourth creation mode: an issue's
// main-role task, which works in the one directory its issue's main tasks
// share (task 134 decisions 3, 7, 16; 134.12). In order:
//
//  1. The issue's main branch in the project's own main checkout blocks
//     `issue_branch_checked_out` (decision 16). The task never runs in the
//     project path, as task 125's adopt mode would.
//  2. A settled predecessor still naming the directory hands it on, in one
//     store transaction (Store.TransferIssueWorktree). Uncommitted work goes
//     with it — that is the point of sharing (decision 7's option C, beaten
//     because a fresh worktree loses it) — but a half-finished git
//     operation does not: it blocks `repo_operation_in_progress` and the
//     predecessor keeps the directory, as a chat handoff refuses one.
//  3. No holder but the branch exists — every main task that held it was
//     archived: the branch goes into a fresh worktree, as vincent's own.
//  4. No branch yet — this is the issue's first main task: the ordinary cut.
//
// Crash safety is the transfer's single transaction. A crash before it
// commits leaves the predecessor naming the directory and the successor
// naming nothing, so re-admission runs this selection again from the top; a
// crash after it leaves the successor naming the directory, so re-admission
// returns early from ensureWorktree, and recovery (§12.4,
// internal/taskrun/recover.go) resumes it like any task with a worktree. No
// moment exists at which both rows, or neither, name it.
func (r *Runner) ensureIssueMainWorktree(
	ctx context.Context, task *store.Task, project *store.Project, fetch bool, log *slog.Logger,
) error {
	wt := r.deps.Worktrees
	inMain, err := wt.BranchInMainCheckout(ctx, project.Path, task.BranchName)
	if err != nil {
		return r.worktreeFailed(ctx, task, err, log)
	}
	if inMain {
		return r.worktreeFailed(ctx, task, worktree.IssueBranchInMainCheckout(task.BranchName, project.Path), log)
	}
	// Twice at most. Losing the transfer means the holder changed between
	// the read and the transaction — a human archived it, or another
	// admission took it — and a fresh read decides correctly. The scheduler
	// admits one main task of an issue at a time (IssueOccupied), so losing
	// twice is not a race this code can win by looping: it fails closed and
	// a retry, with whatever moved settled, clears it.
	for attempt := 0; ; attempt++ {
		holder, err := r.deps.Store.IssueMainWorktreeHolder(ctx, *task.IssueID, task.ID)
		if err != nil {
			r.fail(task, ReasonInternalError, withLocalError("reading the issue's main worktree failed", err),
				log, "read issue main worktree holder", err)
			return err
		}
		if holder == nil {
			return r.createIssueMainWorktree(ctx, task, project, fetch, log)
		}
		err = r.takeIssueMainWorktree(ctx, task, project, holder, log)
		if !errors.Is(err, store.ErrIssueWorktreeNotHeld) {
			return err
		}
		if attempt > 0 {
			// Fail closed rather than guess: writing a claim the store
			// refused twice would put two rows on one directory, the
			// ambiguity the §10 reclaimer must never see.
			detail := "the issue's main worktree moved to another task during admission; retry the task"
			r.fail(task, worktree.ReasonGitError, detail, log, "hand over issue main worktree", err)
			return err
		}
		log.Info("the issue's main worktree moved during admission; reading its holder again",
			"from_task", holder.ID, "error", err)
	}
}

// takeIssueMainWorktree is case 2: the predecessor holder hands its
// directory to task. It returns an ErrIssueWorktreeNotHeld error untouched,
// and blocks the task on anything else.
func (r *Runner) takeIssueMainWorktree(
	ctx context.Context, task *store.Task, project *store.Project, holder *store.Task, log *slog.Logger,
) error {
	if !taskstate.Settled(holder.State) {
		// The scheduler's occupancy predicate keeps a successor queued while
		// any main task of its issue is unsettled, so this is a holder that
		// should not exist. Taking its directory would put two live tasks in
		// it; leaving the successor queued would spin it. Blocking says so.
		err := fmt.Errorf("issue main worktree held by task %d in %s", holder.ID, holder.State)
		detail := fmt.Sprintf("the issue's main worktree is still held by task %d, which is %s; "+
			"retry once it has finished", holder.ID, holder.State)
		r.fail(task, worktree.ReasonGitError, detail, log, "hand over issue main worktree", err)
		return err
	}
	wt := r.deps.Worktrees
	op, err := wt.InProgressOp(ctx, holder.WorktreePath)
	if err != nil {
		return r.worktreeFailed(ctx, task, err, log)
	}
	if op != "" {
		// The holder keeps the directory and nothing is written: a human
		// finishes or aborts the operation there, then retries this task.
		err := fmt.Errorf("issue main worktree %s is partway through a %s", holder.WorktreePath, op)
		detail := fmt.Sprintf("the issue's main worktree %s, handed on from task %d, is partway through a %s; "+
			"finish or abort it there, then retry", holder.WorktreePath, holder.ID, op)
		r.fail(task, worktree.ReasonRepoOperationInProgress, detail, log, "hand over issue main worktree", err)
		return err
	}
	var tip string
	err = wt.HandOverUnderClaim(ctx, project.Path, task.BranchName, func(sha string) error {
		if err := r.deps.Store.TransferIssueWorktree(ctx, holder.ID, task.ID, holder.WorktreePath, sha); err != nil {
			return err
		}
		tip = sha
		return nil
	})
	switch {
	case errors.Is(err, store.ErrIssueWorktreeNotHeld):
		return err
	case err != nil:
		return r.worktreeFailed(ctx, task, err, log)
	}
	// The predecessor's container goes now, not at its archive: it still
	// bind-mounts the directory this task is about to work in, and a live
	// mount is a reason the last main task's worktree removal fails (§16,
	// task 061). The predecessor has settled, so nothing runs in it, and
	// should it be followed up later it gets a container of its own, mounting
	// whatever directory it receives then (review F2 of #770).
	r.removeTaskContainer(ctx, holder, log)
	task.WorktreePath = holder.WorktreePath
	task.BaseSHA = tip
	// NULL, as the store wrote it: the directory was received, not cut, so
	// no base refresh happened (task 125 decision 7).
	task.BaseRefresh = nil
	log.Info("received the issue's main worktree from its predecessor",
		"from_task", holder.ID, "path", holder.WorktreePath, "base_sha", tip)
	return nil
}

// createIssueMainWorktree is cases 3 and 4: no directory to hand over.
func (r *Runner) createIssueMainWorktree(
	ctx context.Context, task *store.Task, project *store.Project, fetch bool, log *slog.Logger,
) error {
	wt := r.deps.Worktrees
	owner := worktree.TaskOwner(task.ID)
	var refresh *store.BaseRefresh
	persist := func(c worktree.Created) {
		// Logged and carried on, as ensureWorktree's own claim does: an
		// unclaimed directory is the crash case gc reclaims.
		if err := r.deps.Store.ClaimTaskWorktree(ctx, task.ID, c.Path, c.BaseSHA, refresh); err != nil {
			log.Error("persist worktree path", "error", err)
		}
	}
	var created worktree.Created
	var err error
	existing := wt.LocalBranchExists(ctx, project.Path, task.BranchName)
	if existing {
		// Case 3. base_sha is the tip and base_refresh stays NULL: nothing
		// refreshed a base, and the successor's diff starts where the
		// archived predecessors left the branch.
		created, err = wt.CreateIssueMainAndClaim(ctx, project.Path, owner, task.BranchName,
			func(c worktree.Created) error { persist(c); return nil })
	} else {
		// Case 4, the issue's first main task: an ordinary cut, recorded
		// the way ensureWorktree records one.
		created, err = wt.CreateAndClaim(ctx, project.Path, owner, task.BranchName, task.BaseBranch, fetch,
			func(c worktree.Created) error {
				refresh = &store.BaseRefresh{
					Fetch: store.BaseFetch(c.Fetch), FastForward: store.BaseFastForward(c.FastForward),
				}
				persist(c)
				return nil
			})
		logBaseRefresh(log, task.BaseBranch, created.Fetch, created.FastForward)
	}
	if err != nil {
		return r.worktreeFailed(ctx, task, err, log)
	}
	if existing {
		log.Info("put the issue's main branch in a fresh worktree; no earlier main task holds one",
			"branch", task.BranchName, "base_sha", created.BaseSHA)
	}
	task.WorktreePath = created.Path
	task.BaseSHA = created.BaseSHA
	task.BaseRefresh = refresh
	return nil
}
