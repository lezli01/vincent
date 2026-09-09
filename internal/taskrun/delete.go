package taskrun

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/lezli01/vincent/internal/worktree"
)

// Delete permanently removes an archived task: the row and its step_runs, then
// the transcript directory, and optionally the branch (§13.2, task 092).
//
// It is *not* a §6 action. taskstate has no opinion on it, it never appears in
// `available_actions`, and the "archived only" rule is checked by the store
// inside the delete transaction rather than by the FSM — the precedent is
// `DELETE /v1/projects/{id}`, which is likewise no action. It lives on the
// runner all the same, because the runner is what holds the worktree manager,
// the data dir and the logger the three steps need.
//
// deleteBranch applies §10's standing rule unchanged (task 008, widened by
// task 092 from "at archive time" to "at archive time and at permanent
// delete"): a branch carrying any commit past its base is reported
// `has_commits` and kept, whatever the human answered. The remote leg is not
// offered — `delete_remote_branch_on_archive` stays honoured only by archive,
// because deleting a branch on a forge other people share is unrecoverable and
// a delete has no second chance to reconsider.
//
// The order is deliberate. The row goes first: a failed unlink must not
// resurrect a row that has already been reported gone, and `vincent gc`
// already treats a transcript directory with no row as its own to reclaim.
// Neither the branch step nor the transcript step can fail the call.
func (r *Runner) Delete(
	ctx context.Context, id int64, deleteBranch bool,
) (worktree.BranchOutcome, error) {
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return worktree.BranchOutcome{}, err
	}
	// Read before the delete: the row carries the base, the recorded base SHA
	// and the branch name the §10 judgement needs, and it is about to stop
	// existing.
	var projectPath string
	if deleteBranch && task.BranchName != "" {
		ran, err := r.ranAStep(ctx, id)
		if err != nil {
			return worktree.BranchOutcome{}, err
		}
		if ran {
			project, err := r.deps.Store.GetProject(ctx, task.ProjectID)
			if err != nil {
				return worktree.BranchOutcome{}, err
			}
			projectPath = project.Path
		}
	}
	if err := r.deps.Store.DeleteTaskCascade(ctx, id); err != nil {
		return worktree.BranchOutcome{}, err
	}
	RemoveTranscriptDir(r.deps.DataDir, strconv.FormatInt(id, 10), r.deps.Logger)
	if projectPath == "" {
		return worktree.BranchOutcome{}, nil
	}
	log := r.deps.Logger.With("task", id, "branch", task.BranchName)
	// Whether the branch is ours to delete at all (task 064 decision 3): a
	// task created from a pull request runs on the contributor's head branch,
	// and this must not touch it. Same answer archive gives.
	ours := !task.GitHubPull.FromPull()
	out, err := r.deps.Worktrees.DeleteEmptyBranch(ctx, projectPath,
		task.BaseBranch, task.BaseSHA, task.BranchName, false, &ours)
	if err != nil {
		log.Warn("delete: branch kept", "result", out.Result, "error", err)
		return out, nil
	}
	switch out.Result {
	case worktree.BranchDeleted:
		log.Info("delete: deleted the branch, which had no commits past its base", "base", task.BaseBranch)
	case worktree.BranchHasCommits:
		log.Info("delete: kept the branch, which has commits past its base", "base", task.BaseBranch)
	case worktree.BranchNotOurs:
		log.Info("delete: kept the branch, which came from a pull request and was not cut by vincent")
	}
	return out, nil
}

// ranAStep reports whether the task ever executed a step, which is this
// path's answer to "is this branch one vincent cut for this task".
//
// Archive asks it as `worktree_path != ""` and cannot be asked here: archive
// *clears* that column on the way into `archived`, so by the time a delete
// runs it is empty on every task. The question still has to be answered,
// because a task that blocked with `branch_exists` (§10, task 001) carries a
// branch_name naming **somebody else's** branch, and nothing may delete that.
//
// A step run is the surviving evidence. `ensureWorktree` runs before the first
// step executor, so a task blocked at worktree creation has no step_run row at
// all, while a task that ran anything necessarily had a worktree — and
// `git worktree add -b` makes a worktree and a branch or neither. It fails
// conservatively in the one case it is imprecise: a task whose worktree was
// made and which was cancelled before its first step keeps its branch, which
// is the safe direction. The rows are read before the delete, which is the
// only time they exist.
func (r *Runner) ranAStep(ctx context.Context, id int64) (bool, error) {
	runs, err := r.deps.Store.ListStepRuns(ctx, id)
	if err != nil {
		return false, err
	}
	return len(runs) > 0, nil
}

// RemoveTranscriptDir removes one directory under {data_dir}/transcripts,
// best-effort and logged, reusing the pruner's containment shape: the name is
// joined onto the root rather than taken from a caller, so nothing outside
// that root is reachable. Already gone is the common case — a task pruned by
// retention, or one that never produced a transcript — and is not a log line.
//
// It is exported and takes the directory name rather than an id because the
// API's chat delete calls it too: chats keep their transcripts under the same
// root as `chat-{id}` (worktree.ChatOwner(id).Dir()), and duplicating the
// containment would be duplicating the part that matters.
func RemoveTranscriptDir(dataDir, name string, log *slog.Logger) {
	if dataDir == "" || name == "" {
		// No data dir means no transcript root to be inside, and a relative
		// join would reach whatever the process's working directory is.
		return
	}
	dir := filepath.Join(dataDir, "transcripts", name)
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("delete: transcript directory kept", "dir", name, "error", err)
	}
}
