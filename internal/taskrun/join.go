package taskrun

// The join half of a `fan_out` step (spec §7.6, task 014 decisions 3, 7, 8,
// 9, 21, 22). The parent has woken from `awaiting_children` and now merges
// every lane's branch into the branch it already owns, so that one branch is
// still delivered at the end.
//
// Merges are sequential, `--no-ff`, in **declared** lane order, stopping at
// the first conflict. Declared rather than completion order is what makes a
// re-run conflict identically, which the idempotent recovery below depends
// on.
//
// Nothing here persists a merge cursor. Which lanes are already merged is a
// fact git holds authoritatively — an already-merged lane re-merges as
// "Already up to date" — and a stored copy can disagree with the repository
// after a human runs `git merge --abort` themselves (decision 9).

import (
	"context"
	"fmt"
	"strings"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// ReasonLaneFailed is a fan-out whose lane did not finish its work: it was
// cancelled, or it failed and a human ended it (§18, task 014 decision 21).
// Nothing is merged — a partial merge is indistinguishable, downstream, from
// a complete one.
const ReasonLaneFailed = "lane_failed"

// ReasonMergeConflict re-exports the worktree taxonomy's name so the engine's
// vocabulary stays one list (T1.5/T1.6 decision).
const ReasonMergeConflict = worktree.ReasonMergeConflict

// runJoinStep is the fan_out step's executor on a re-admission: it loads the
// lanes this step spawned and merges them.
func (r *Runner) runJoinStep(ctx context.Context, env *stepEnv, tr *transcript) stepOutcome {
	children, err := r.deps.Store.ListChildren(ctx, env.task.ID)
	if err != nil {
		env.log.Error("list lanes", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
	}
	lanes := make([]store.Task, 0, len(children))
	for _, c := range children {
		if c.ParentStepIndex != nil && *c.ParentStepIndex == env.index {
			lanes = append(lanes, c)
		}
	}
	tr.Note("join_started", map[string]any{"lanes": len(lanes), "step_id": env.step.ID})
	outcome := r.runJoin(ctx, env, lanes)
	tr.Note("join_finished", map[string]any{
		"state": string(outcome.state), "reason": outcome.reason,
	})
	return outcome
}

// runJoin merges every lane of one fan_out step into the parent's branch.
func (r *Runner) runJoin(ctx context.Context, env *stepEnv, lanes []store.Task) stepOutcome {
	// Every lane must have *finished its work*, not merely settled: an
	// aborted lane settled too, and merging around it would deliver a branch
	// missing that lane with nothing saying so (decision 21).
	for _, lane := range lanes {
		if lane.State != store.TaskDone {
			env.log.Warn("lane did not finish", "lane", lane.LaneID, "child", lane.ID, "state", lane.State)
			return stepOutcome{
				state:  store.StepFailed,
				reason: ReasonLaneFailed,
				output: fmt.Sprintf("lane %q (task %d) is %s, not done", lane.LaneID, lane.ID, lane.State),
			}
		}
	}

	// Re-entry (decision 9). A merge already in progress means one of two
	// very different things, and only one of them may abort.
	if outcome, handled := r.resumeMerge(ctx, env); handled {
		return outcome
	}

	for _, lane := range lanes {
		result, err := r.deps.Worktrees.MergeLane(ctx, env.task.WorktreePath,
			lane.BranchName, lane.LaneID, lane.ID)
		if err != nil {
			reason := worktree.ReasonOf(err)
			if reason == "" {
				reason = worktree.ReasonGitError
			}
			env.log.Error("merge lane", "lane", lane.LaneID, "error", err)
			return stepOutcome{state: store.StepFailed, reason: reason, output: err.Error()}
		}
		if result == worktree.MergeConflicted {
			resolved, outcome := r.handleConflict(ctx, env, lane)
			if !resolved {
				return outcome
			}
			// The resolver fixed it and the merge is committed, so the join
			// carries on with the remaining lanes in the same admission.
		}
		env.log.Info("lane merged", "lane", lane.LaneID, "child", lane.ID, "branch", lane.BranchName)
	}
	return stepOutcome{state: store.StepSucceeded, result: fmt.Sprintf("merged %d lanes", len(lanes))}
}

// resumeMerge handles a join re-entered with a merge already in progress. It
// reports whether it took over — when it did, the caller must not start
// merging from the top.
//
// The task's own state is what disambiguates, which is why no cursor is
// stored:
//
//   - `blocked` with merge_conflict is a human who has just resolved by hand
//     and hit retry. A clean index means commit and carry on with the
//     remaining lanes; a still-conflicted one means block again.
//   - anything else is a crash between two lanes. Recovery aborts and
//     re-merges every lane from the top, which is a no-op for the ones
//     already in.
//
// The failure this distinction exists to prevent is specific and expensive:
// recovery running `--abort` over a conflict a human spent an hour resolving.
func (r *Runner) resumeMerge(ctx context.Context, env *stepEnv) (stepOutcome, bool) {
	inMerge, err := r.deps.Worktrees.InMerge(ctx, env.task.WorktreePath)
	if err != nil {
		env.log.Error("check merge state", "error", err)
		return stepOutcome{state: store.StepFailed, reason: worktree.ReasonGitError}, true
	}
	if !inMerge {
		return stepOutcome{}, false
	}

	if env.resumedFromConflict {
		conflicted, err := r.deps.Worktrees.IndexConflicted(ctx, env.task.WorktreePath)
		if err != nil {
			env.log.Error("check index", "error", err)
			return stepOutcome{state: store.StepFailed, reason: worktree.ReasonGitError}, true
		}
		if conflicted {
			env.log.Warn("retry with the conflict still unresolved")
			return stepOutcome{
				state:  store.StepFailed,
				reason: ReasonMergeConflict,
				output: "the merge is still conflicted; resolve and stage the files, then retry",
			}, true
		}
		if err := r.deps.Worktrees.CommitMerge(ctx, env.task.WorktreePath); err != nil {
			env.log.Error("commit resolved merge", "error", err)
			return stepOutcome{state: store.StepFailed, reason: worktree.ReasonGitError}, true
		}
		env.log.Info("resolved merge committed; continuing with the remaining lanes")
		// Fall through to the ordinary loop: the merged lanes are now
		// "Already up to date" no-ops and the rest merge normally.
		return stepOutcome{}, false
	}

	// A crash mid-merge. Abort, then re-merge from the top.
	if err := r.deps.Worktrees.AbortMerge(ctx, env.task.WorktreePath); err != nil {
		env.log.Error("abort interrupted merge", "error", err)
		return stepOutcome{state: store.StepFailed, reason: worktree.ReasonGitError}, true
	}
	env.log.Info("aborted a merge interrupted mid-join; re-merging every lane")
	return stepOutcome{}, false
}

// handleConflict applies the step's `on_conflict:` policy to a conflicted
// merge. The default blocks; `agent` tries a resolver first and falls back to
// the block when it or its check fails.
//
// Blocking by default is §7.2's posture applied unchanged: retries are for
// failures a retry can fix, and a human decides what a machine could not. The
// alternative default — an agent silently resolving a semantic conflict and
// the merge commit landing unread — is the one outcome that turns a
// time-saving feature into a correctness liability.
// It reports whether the conflict was resolved, so the caller can continue
// with the remaining lanes rather than restarting the join.
func (r *Runner) handleConflict(
	ctx context.Context, env *stepEnv, lane store.Task,
) (resolved bool, outcome stepOutcome) {
	paths, err := r.deps.Worktrees.ConflictedPaths(ctx, env.task.WorktreePath)
	if err != nil {
		env.log.Error("list conflicted paths", "error", err)
	}
	blocked := stepOutcome{
		state:  store.StepFailed,
		reason: ReasonMergeConflict,
		output: fmt.Sprintf("lane %q (task %d) conflicts in:\n%s",
			lane.LaneID, lane.ID, strings.Join(paths, "\n")),
	}
	if env.step.ConflictPolicy() != workflow.ConflictAgent || env.step.Merge.Agent == nil {
		env.log.Warn("merge conflict; blocking for a human",
			"lane", lane.LaneID, "files", len(paths))
		return false, blocked
	}

	env.log.Info("merge conflict; trying the agent resolver", "lane", lane.LaneID, "files", len(paths))
	resolver := *env.step.Merge.Agent
	resolver.Type = workflow.StepAgent
	// The resolver is an ordinary agent step, run by the ordinary executor in
	// the parent's worktree, with the conflicted files in its context
	// (decision 24).
	resolverEnv := &stepEnv{
		task: env.task, project: env.project, wf: env.wf,
		step: resolver, index: env.index, inGroup: true,
		conflicts: paths,
		log:       env.log.With("merge_resolver", resolver.ID),
	}
	attempt := r.runStepWithRetries(ctx, resolverEnv)
	if attempt.state != store.StepSucceeded {
		env.log.Warn("agent resolver did not resolve the conflict; blocking",
			"reason", attempt.reason)
		return false, blocked
	}
	// The agent may have edited without staging. Staging here rather than
	// requiring it of the prompt keeps the contract "leave the files right".
	if err := r.deps.Worktrees.StageAll(ctx, env.task.WorktreePath); err != nil {
		env.log.Error("stage resolver output", "error", err)
		return false, blocked
	}
	if conflicted, cErr := r.deps.Worktrees.IndexConflicted(ctx, env.task.WorktreePath); cErr != nil || conflicted {
		env.log.Warn("conflict markers survived the resolver; blocking")
		return false, blocked
	}
	if err := r.deps.Worktrees.CommitMerge(ctx, env.task.WorktreePath); err != nil {
		env.log.Error("commit resolver merge", "error", err)
		return false, blocked
	}
	env.log.Info("agent resolved the merge conflict", "lane", lane.LaneID)
	return true, stepOutcome{}
}
