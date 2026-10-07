package taskrun

// The merge-back task (spec §5.6, task 134 decisions 11, 12, 15, and
// 134.14). When a side task of an issue reaches `done`, the daemon inserts,
// in the same transaction, a queued main-role task whose one synthesized
// step merges the side branch into the issue's main worktree. It waits for
// that worktree through the same admission predicate as any main task, and
// receives it through 134.12's transfer or revival, so everything here runs
// in a directory this task owns.
//
// The merge itself is fan_out's join machinery reused (join.go): the same
// crash-safe re-entry (resumedFromConflict, resumeMerge) and the same
// conflict policy (handleConflict). Only the message differs, so
// `diff?by=lane` never reads a merge-back as a lane.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// ReasonMergeSourceMissing is a merge-back whose side task was deleted, or
// whose side branch no longer exists in git (§18, task 134.14). There is
// nothing to merge; a human skips or cancels it.
const ReasonMergeSourceMissing = "merge_source_missing"

// ReasonMergeTargetMissing is a merge-back that found the issue's main
// branch gone at admission (§18, task 134.14-a): every main task of the
// issue was archived and the branch the side task forked from was deleted
// as well, so there is nothing to revive and merge into.
const ReasonMergeTargetMissing = "merge_target_missing"

// isMergeBack reports whether task is a merge-back. The workflow name is
// what says so, not merge_source_task_id: deleting the source nulls that
// column, and the task is still a merge-back — one that blocks
// merge_source_missing.
func isMergeBack(task *store.Task) bool {
	return task.WorkflowName == workflow.MergeBackName && task.IssueWorktree == store.IssueWorktreeMain
}

// mergeBackFor builds the merge-back task a side task's `→ done` inserts, or
// nil when there is none to insert (task 134.14). It runs before the
// transition, because asking git whether the side branch has commits past
// its base is a subprocess, and SQLite's write lock is never held across
// one.
//
// An empty side branch merges nothing, so it creates nothing (134.14-b), as
// fan_out merges nothing for an empty lane; nor does one already merged. A branch git cannot resolve
// still creates one: the merge-back is what says so, by blocking
// merge_source_missing where a human sees it.
func (r *Runner) mergeBackFor(ctx context.Context, task *store.Task, log *slog.Logger) *store.Task {
	if task.IssueWorktree != store.IssueWorktreeSide || task.IssueID == nil {
		return nil
	}
	project, err := r.deps.Store.GetProject(ctx, task.ProjectID)
	if err != nil {
		log.Error("merge-back: load project", "error", err)
		return nil
	}
	tip, err := r.deps.Worktrees.BranchTip(ctx, project.Path, task.BranchName)
	if err == nil && task.BaseSHA != "" && tip == task.BaseSHA {
		log.Info("side task has no commits past its base; no merge-back", "branch", task.BranchName)
		return nil
	}
	if err != nil {
		log.Warn("merge-back: side branch tip unreadable; the merge-back will say so",
			"branch", task.BranchName, "error", err)
	}
	// Nor does a side branch the issue's main branch already contains — a
	// follow-up that committed nothing, or one abandoned with `skip`, after
	// an earlier merge-back landed it (134.14-b, review F5 of #771). The
	// side task's base is that main branch (134.13). An unreadable answer
	// still creates one: the merge is then a harmless "Already up to date".
	if err == nil {
		merged, mErr := r.deps.Worktrees.BranchMergedInto(ctx, project.Path, task.BranchName, task.BaseBranch)
		if mErr != nil {
			log.Warn("merge-back: could not tell whether the side branch is merged", "error", mErr)
		}
		if merged {
			log.Info("side branch is already on the issue's main branch; no merge-back",
				"branch", task.BranchName, "main", task.BaseBranch)
			return nil
		}
	}
	issueID := *task.IssueID
	title := workflow.MergeBackTitle(task.ID, issueID)
	return &store.Task{
		Title: title,
		Description: fmt.Sprintf("Merges task %d's branch %s into issue #%d's main branch.",
			task.ID, task.BranchName, issueID),
		WorkflowName:     workflow.MergeBackName,
		WorkflowSnapshot: workflow.MergeBackSource(task.ID, issueID, task.BranchName),
		// The resolver's agent, model and effort are the side task's
		// overrides, falling back to the ordinary defaults (134.14).
		AgentOverride:  task.AgentOverride,
		ModelOverride:  task.ModelOverride,
		EffortOverride: task.EffortOverride,
		Restricted:     task.Restricted,
		MaxTaskCostUSD: task.MaxTaskCostUSD,
		Issue:          task.Issue,
	}
}

// mergeBackBranch is the side branch a merge-back's snapshot records.
func mergeBackBranch(step workflow.Step) string {
	return strings.ReplaceAll(step.Env[workflow.MergeBackBranchEnv], `{{"{{"}}`, "{{")
}

// runMergeBackStep is the `__merge_back` step's executor, reached through
// runAttempt so the merge has a step_runs row, a transcript and events like
// any step. env.resumedFromConflict was asked before the row existed.
func (r *Runner) runMergeBackStep(ctx context.Context, env *stepEnv, tr *transcript) stepOutcome {
	branch := mergeBackBranch(env.step)
	tr.Note("merge_back_started", map[string]any{"branch": branch, "source_task_id": env.task.MergeSourceTaskID})
	outcome := r.runMergeBack(ctx, env, branch)
	tr.Note("merge_back_finished", map[string]any{"state": string(outcome.state), "reason": outcome.reason})
	return outcome
}

func (r *Runner) runMergeBack(ctx context.Context, env *stepEnv, branch string) stepOutcome {
	task := env.task
	// A hand resolution being retried is committed, or a crash mid-merge
	// aborted, before anything else: the source's absence must not leave a
	// half-done merge in the issue's main worktree.
	if outcome, handled := r.resumeMerge(ctx, env); handled {
		return outcome
	}
	var source *store.Task
	if task.MergeSourceTaskID != nil {
		got, err := r.deps.Store.GetTask(ctx, *task.MergeSourceTaskID)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			env.log.Error("merge-back: load source task", "error", err)
			return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
		default:
			source = got
		}
	}
	if source == nil {
		return stepOutcome{
			state: store.StepFailed, reason: ReasonMergeSourceMissing,
			output: fmt.Sprintf("the side task whose branch %s this merges was deleted; skip or cancel this task", branch),
		}
	}
	if branch == "" || !r.deps.Worktrees.LocalBranchExists(ctx, env.project.Path, branch) {
		return stepOutcome{
			state: store.StepFailed, reason: ReasonMergeSourceMissing,
			output: fmt.Sprintf("task %d's branch %s no longer exists; skip or cancel this task", source.ID, branch),
		}
	}
	result, err := r.deps.Worktrees.MergeBranch(ctx, task.WorktreePath, "refs/heads/"+branch, task.Title)
	if err != nil {
		reason := worktree.ReasonOf(err)
		if reason == "" {
			reason = worktree.ReasonGitError
		}
		env.log.Error("merge back", "branch", branch, "error", err)
		return stepOutcome{state: store.StepFailed, reason: reason, output: err.Error()}
	}
	if result == worktree.MergeConflicted {
		subject := fmt.Sprintf("task %d's branch %s", source.ID, branch)
		if resolved, outcome := r.handleConflict(ctx, env, mergeBackPolicy(task, source), subject); !resolved {
			return outcome
		}
	}
	env.log.Info("side task merged back", "source", source.ID, "branch", branch)
	return stepOutcome{state: store.StepSucceeded, result: fmt.Sprintf("merged task %d's branch %s", source.ID, branch)}
}

// mergeBackPolicy is the `merge:` block a merge-back's conflict runs under:
// the side task's `merge_back.on_conflict`, copied onto this row at
// creation, and for `agent` the built-in resolver (task 134 decision 12).
// The resolver carries no check — handleConflict's "no conflict markers in
// the files that conflicted" is the floor — and leaves agent, model and
// effort to the task's overrides, which are the side task's.
func mergeBackPolicy(task, source *store.Task) *workflow.Merge {
	if task.MergeOnConflict != store.MergeOnConflictAgent {
		return &workflow.Merge{OnConflict: workflow.ConflictBlock}
	}
	return &workflow.Merge{
		OnConflict: workflow.ConflictAgent,
		Agent: &workflow.Step{
			ID:     "resolve",
			Name:   "resolve merge-back conflicts",
			Type:   workflow.StepAgent,
			Prompt: workflow.MergeBackResolverPrompt(source.ID, source.Title, source.Description),
		},
	}
}

// mergeBackEnded reports whether task is a merge-back with nothing left to
// run: a human skipped its one step from a block (§6), so the step cursor is
// past it and no repair or follow-up is pending. execute finishes such a task
// before ensureWorktree, the way an abandoned follow-up is finished: a
// merge-back skipped from merge_target_missing has no worktree and never
// could have one, and creating it again would only block it again.
func mergeBackEnded(task *store.Task, wf *workflow.Workflow) bool {
	return isMergeBack(task) && task.CurrentStep >= len(wf.Steps) &&
		(task.PendingRepair == nil || task.PendingRepair.Empty()) &&
		(task.PendingFollowUp == nil || task.PendingFollowUp.Empty())
}

// finishSkippedMergeBack ends a skipped merge-back without merging (§6's
// ordinary skip): a conflicted merge it left in the issue's main worktree is
// aborted first, so the next main task receives that directory clean rather
// than blocking repo_operation_in_progress on it (task 134 decision 15).
func (r *Runner) finishSkippedMergeBack(ctx context.Context, task *store.Task, log *slog.Logger) {
	r.abortMergeBack(ctx, task, log)
	r.complete(task, log)
}

// abortMergeBack runs `git merge --abort` in a merge-back that is ending
// without its merge — cancelled (task 134 decision 15) or skipped — so the
// issue's main worktree goes to the next main task clean. It asks git rather
// than the task's block reason: a merge is just as much in progress under a
// running `agent` resolver, or in a merge-back re-queued after a crash, as in
// one blocked merge_conflict. This is the one path besides recovery that
// aborts a merge (§12.4 as amended). A failure is logged and the task ends
// anyway: the successor's admission then blocks repo_operation_in_progress,
// which names the directory.
//
// Callers run it only once the ending is certain — after the cancel has
// committed and the actor has exited, or from the skipped task's own actor —
// so a cancel that loses a race to a retry never throws away a hand
// resolution.
func (r *Runner) abortMergeBack(ctx context.Context, task *store.Task, log *slog.Logger) {
	if !isMergeBack(task) || task.WorktreePath == "" {
		return
	}
	inMerge, err := r.deps.Worktrees.InMerge(ctx, task.WorktreePath)
	if err != nil {
		log.Error("merge-back: check for a merge to abort", "error", err)
		return
	}
	if !inMerge {
		return
	}
	if err := r.deps.Worktrees.AbortMerge(ctx, task.WorktreePath); err != nil {
		log.Error("merge-back: abort the merge it is ending without", "error", err)
		return
	}
	log.Info("merge-back: aborted the merge it is ending without")
}
