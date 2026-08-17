package worktree

// Merging a fan-out lane's branch into its parent's (spec §7.6, task 014
// decisions 7, 9).
//
// The git primitives live here rather than in the engine for the reason every
// other git call does: this package owns the repository vocabulary and the
// `Reason*` taxonomy a block_reason is drawn from.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReasonMergeConflict is a lane whose merge into the parent's branch
// conflicted (§18, task 014 decision 8). The worktree is left conflicted so a
// human can resolve in place — that is the point of the block, not a
// side effect of it.
const ReasonMergeConflict = "merge_conflict"

// MergeResult is the outcome of merging one lane.
type MergeResult int

const (
	// MergeOK is a completed merge, including an "Already up to date" no-op —
	// which is what re-merging an already-merged lane produces, and is what
	// makes the whole join idempotent (decision 9).
	MergeOK MergeResult = iota
	// MergeConflicted is a merge stopped by a conflict, with the worktree and
	// index left in the conflicted state.
	MergeConflicted
)

// MergeLane merges branch into the branch checked out in worktreePath, with
// `--no-ff` and a message naming the lane and the task it came from.
//
// --no-ff keeps each lane visible in history and matches the repo's own
// no-squash convention. No author or committer is set: vincent runs as the
// invoking user (§16) and has no business inventing an identity.
func (m *Manager) MergeLane(
	ctx context.Context, worktreePath, branch, laneID string, childID int64,
) (MergeResult, error) {
	msg := fmt.Sprintf("Merge lane '%s' of task %d", laneID, childID)
	out, err := m.git.Run(ctx, worktreePath, "merge", "--no-ff", "-m", msg, branch)
	if err == nil {
		return MergeOK, nil
	}
	// A conflict is an ordinary outcome here, not a git failure: git exits
	// non-zero either way, and MERGE_HEAD is what tells them apart.
	if inMerge, mErr := m.InMerge(ctx, worktreePath); mErr == nil && inMerge {
		return MergeConflicted, nil
	}
	return MergeOK, &Error{
		Reason: ReasonGitError,
		Err:    fmt.Errorf("merge lane %q: %w: %s", laneID, err, strings.TrimSpace(out)),
	}
}

// InMerge reports whether a merge is in progress in the worktree — MERGE_HEAD
// exists. It is the fact both re-entry paths turn on (decision 9), read from
// git rather than from a persisted cursor, because git holds it
// authoritatively and a stored copy can disagree after a human runs
// `git merge --abort` themselves.
func (m *Manager) InMerge(ctx context.Context, worktreePath string) (bool, error) {
	dir, err := m.gitDir(ctx, worktreePath)
	if err != nil {
		return false, err
	}
	_, statErr := os.Stat(filepath.Join(dir, "MERGE_HEAD"))
	if statErr == nil {
		return true, nil
	}
	if os.IsNotExist(statErr) {
		return false, nil
	}
	return false, &Error{Reason: ReasonGitError, Err: fmt.Errorf("stat MERGE_HEAD: %w", statErr)}
}

// IndexConflicted reports whether the worktree's index still holds unmerged
// paths. A human who has resolved by hand and staged the result leaves
// MERGE_HEAD in place with a clean index, which is the case the retry path
// completes rather than restarts (decision 9).
func (m *Manager) IndexConflicted(ctx context.Context, worktreePath string) (bool, error) {
	out, err := m.git.Run(ctx, worktreePath, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return false, &Error{Reason: ReasonGitError, Err: fmt.Errorf("list unmerged paths: %w", err)}
	}
	return strings.TrimSpace(out) != "", nil
}

// ConflictedPaths lists the files with unresolved conflicts, for the block's
// message and for an `on_conflict: agent` resolver's prompt.
func (m *Manager) ConflictedPaths(ctx context.Context, worktreePath string) ([]string, error) {
	out, err := m.git.Run(ctx, worktreePath, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, &Error{Reason: ReasonGitError, Err: fmt.Errorf("list unmerged paths: %w", err)}
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

// CommitMerge completes a merge whose conflicts have been resolved and
// staged, keeping the message git already recorded for it.
func (m *Manager) CommitMerge(ctx context.Context, worktreePath string) error {
	if _, err := m.git.Run(ctx, worktreePath, "commit", "--no-edit"); err != nil {
		return &Error{Reason: ReasonGitError, Err: fmt.Errorf("commit resolved merge: %w", err)}
	}
	return nil
}

// AbortMerge undoes an in-progress merge.
//
// Recovery calls this and a human retry must not: aborting over a conflict
// somebody spent an hour resolving is the specific, expensive failure
// decision 9 exists to prevent.
func (m *Manager) AbortMerge(ctx context.Context, worktreePath string) error {
	if _, err := m.git.Run(ctx, worktreePath, "merge", "--abort"); err != nil {
		return &Error{Reason: ReasonGitError, Err: fmt.Errorf("abort merge: %w", err)}
	}
	return nil
}

// StageAll stages everything in the worktree, for an agent resolver that
// edited files but did not stage them.
func (m *Manager) StageAll(ctx context.Context, worktreePath string) error {
	if _, err := m.git.Run(ctx, worktreePath, "add", "-A"); err != nil {
		return &Error{Reason: ReasonGitError, Err: fmt.Errorf("stage resolved files: %w", err)}
	}
	return nil
}

// gitDir resolves the worktree's own git directory — for a linked worktree
// that is `.git/worktrees/{name}`, not the repository's `.git`, and MERGE_HEAD
// lives in the former.
func (m *Manager) gitDir(ctx context.Context, worktreePath string) (string, error) {
	out, err := m.git.Run(ctx, worktreePath, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", &Error{Reason: ReasonGitError, Err: fmt.Errorf("resolve git dir: %w", err)}
	}
	return strings.TrimSpace(out), nil
}
