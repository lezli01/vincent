package taskrun

// The merge half of a `fan_out` step (spec §7.6, task 014 decisions 3, 7, 8,
// 9, 21, 22, and task 080). The parent has woken from `awaiting_children` and
// now merges every lane's branch into the branch it already owns, so that one
// branch is still delivered at the end.
//
// Since task 080 this runs **once per round**, not once per step: a lane list
// with `needs:` spawns in waves, and each wave's merge is what makes the next
// wave's worktrees carry its dependencies' commits. A flat lane list is one
// round, so this is bit-for-bit the old join for every workflow written before
// that.
//
// Merges are sequential, `--no-ff`, in **declared** lane order, stopping at
// the first conflict. Declared rather than completion order is what makes a
// re-run conflict identically, which the idempotent recovery below depends
// on. Lanes merged in an earlier round re-merge as "Already up to date", which
// is what lets a crashed parent re-run a whole round.
//
// Nothing here persists a merge cursor. Which lanes are already merged is a
// fact git holds authoritatively — an already-merged lane re-merges as
// "Already up to date" — and a stored copy can disagree with the repository
// after a human runs `git merge --abort` themselves (decision 9).

import (
	"context"
	"fmt"
	"strings"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// ReasonLaneFailed is a fan-out whose lane did not finish its work: it was
// cancelled, or it failed and a human ended it (§18, task 014 decision 21, as
// amended by task 080 decision 2).
//
// Nothing of the **failing round** is merged: a partial round is
// indistinguishable, downstream, from a complete one, which is decision 21
// unchanged and is the whole of it for a flat lane list.
//
// What task 080 reverses is narrower and only reachable with `needs:`: rounds
// that already merged **stay merged**. They are in the branch before round 2
// is known to fail, and the two alternatives are worse — resetting the parent
// branch would make vincent destroy already-integrated commits, which §7.6's
// "the work is stopped, not destroyed" refuses everywhere else, and deferring
// every merge to the end would leave `needs:` as ordering with no code behind
// it, because a dependent lane's worktree would no longer contain its
// dependencies' commits. The task is `blocked`, not `done`, so nothing
// downstream consumes the branch, and which lanes are in it is legible from
// the child rows.
//
// In-flight lanes of other rounds are left to finish. They are real tasks, and
// killing them destroys work — the posture §7.6 already takes on cancel.
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
	tr.Note("join_started", map[string]any{
		"lanes": len(lanes), "step_id": env.step.ID, "round": env.round,
		"schedule": env.step.ScheduleMode(),
	})
	outcome := r.runJoin(ctx, env, lanes)
	tr.Note("join_finished", map[string]any{
		"state": string(outcome.state), "reason": outcome.reason,
	})
	return outcome
}

// runJoin merges every lane of one fan_out step into the parent's branch.
func (r *Runner) runJoin(ctx context.Context, env *stepEnv, lanes []store.Task) stepOutcome {
	mergeable, outcome, ok := mergeSet(env, lanes)
	if !ok {
		return outcome
	}

	// Re-entry (decision 9). A merge already in progress means one of two
	// very different things, and only one of them may abort.
	if outcome, handled := r.resumeMerge(ctx, env); handled {
		return outcome
	}

	for _, lane := range mergeable {
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
	return stepOutcome{
		state:  store.StepSucceeded,
		result: fmt.Sprintf("merged %d lanes (round %d)", len(mergeable), env.round),
	}
}

// mergeSet is the lanes this admission merges, or the outcome that stops it.
//
// Under a barrier every spawned lane must have *finished its work*, not merely
// settled: an aborted lane settled too, and merging around it would deliver a
// branch missing that lane with nothing saying so (decision 21). Checked over
// every lane spawned so far rather than this round's alone, which costs
// nothing — an earlier round's lanes are `done` or the step blocked then — and
// keeps the single-round case exactly what it was.
//
// Under `schedule: eager` that same test would read a merely *running* lane as
// a failure, which is every wake. So the three cases separate (task 081):
// `done` lanes merge, unsettled ones are "not yet", and a lane that settled
// without finishing blocks `lane_failed` **merging nothing new** in this
// admission. Merging what is mergeable first and blocking afterwards was the
// alternative, and it makes the branch content at block time depend on which
// wake happened to notice the failure — a stopwatch question about delivered
// commits, which is strictly worse than the timing dependence eager already
// asks an author to accept (decision 3). Lanes merged by earlier admissions
// stay merged either way (task 080 decision 2), and in-flight lanes are left
// to finish.
func mergeSet(env *stepEnv, lanes []store.Task) (mergeable []store.Task, outcome stepOutcome, ok bool) {
	failed := func(lane store.Task) (stepOutcome, bool) {
		env.log.Warn("lane did not finish", "lane", lane.LaneID, "child", lane.ID, "state", lane.State)
		return stepOutcome{
			state:  store.StepFailed,
			reason: ReasonLaneFailed,
			output: fmt.Sprintf("lane %q (task %d) is %s, not done", lane.LaneID, lane.ID, lane.State),
		}, false
	}
	if !env.eager {
		for _, lane := range lanes {
			if lane.State != store.TaskDone {
				outcome, ok = failed(lane)
				return nil, outcome, ok
			}
		}
		return lanes, stepOutcome{}, true
	}
	mergeable = make([]store.Task, 0, len(lanes))
	for _, lane := range lanes {
		switch {
		case lane.State == store.TaskDone:
			mergeable = append(mergeable, lane)
		case taskstate.Settled(lane.State):
			outcome, ok = failed(lane)
			return nil, outcome, ok
		}
	}
	return mergeable, stepOutcome{}, true
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

// resumedFromConflict reports whether this admission follows a human
// resolving a merge conflict by hand, as opposed to a crash mid-join.
//
// It is read from the step's own history rather than from the task's state,
// which cannot answer it: the scheduler moves a retried task out of `blocked`
// before the engine ever sees it, so by here every re-entry looks alike. The
// last attempt does distinguish them — a conflict block leaves a `failed` row
// with reason merge_conflict, and a crash leaves an `interrupted` one.
//
// The caller must ask **before** creating this attempt's row, which would
// otherwise be the latest one and hide the evidence.
//
// Getting this wrong in the safe direction costs a re-merge; getting it wrong
// in the other direction runs `git merge --abort` over an hour of somebody's
// hand resolution, which is the failure decision 9 exists to prevent.
func (r *Runner) resumedFromConflict(ctx context.Context, env *stepEnv) bool {
	last, err := r.deps.Store.LatestStepRun(ctx, env.task.ID, env.index, env.step.ID)
	if err != nil {
		env.log.Error("read the join's last attempt", "error", err)
		return false
	}
	return last != nil && last.State == store.StepFailed && last.FailureReason == ReasonMergeConflict
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
	// No paced retry for the resolver (§7.2, task 028). Its attempts are the
	// join's own: a resolver that does not resolve leaves the conflict for a
	// human, so there is no failure here for the engine to hold and re-admit.
	// Pinned rather than left alone because `defaults.retry_backoff` would
	// otherwise reach it and silently spend half its budget on a wait nothing
	// would honour.
	noBackoff := config.Duration(0)
	resolver.RetryBackoff = &noBackoff
	// The resolver is an ordinary agent step, run by the ordinary executor in
	// the parent's worktree, with the conflicted files in its context
	// (decision 24).
	resolverEnv := &stepEnv{
		task: env.task, project: env.project, wf: env.wf,
		step: resolver, index: env.index, inGroup: true,
		conflicts: paths,
		followUp:  env.followUp,
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
