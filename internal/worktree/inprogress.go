package worktree

// Detecting a repository operation a human left half-finished (task 074,
// spec §10 amended 2026-09-01).
//
// This lives beside InMerge, which is its one-operation ancestor: handing a
// chat's worktree to a task inherits whatever the directory is in the middle
// of, and gating on a merge alone would let a half-finished rebase through to
// surface later as an unexplained git_error inside a step.

import (
	"context"
	"os"
	"path/filepath"
)

// ReasonRepoOperationInProgress is a worktree whose repository is partway
// through an operation — a conflicted merge, a stopped rebase, a cherry-pick
// or revert awaiting a commit, a running bisect. It belongs to the shared
// snake_case vocabulary internal/taskrun draws block reasons from, so the same
// string means the same thing wherever it originated.
//
// Ordinary dirty state is deliberately not one of these: uncommitted work is
// what a handoff preserves, not what it refuses.
const ReasonRepoOperationInProgress = "repo_operation_in_progress"

// inProgressMarkers maps the name a human would recognise the operation by to
// the path, relative to the worktree's own git dir, git records it at. Order
// is the order they are probed in, so a worktree in two of them reports the
// one a reader is most likely to be looking for.
var inProgressMarkers = []struct {
	op   string
	path string
}{
	{"merge", "MERGE_HEAD"},
	// `rebase-merge` covers interactive and merge-backend rebases;
	// `rebase-apply` is the am/patch backend and also `git am`. Both are
	// directories rather than files, which os.Stat reports just the same.
	{"rebase", "rebase-merge"},
	{"rebase", "rebase-apply"},
	{"cherry-pick", "CHERRY_PICK_HEAD"},
	{"revert", "REVERT_HEAD"},
	{"bisect", "BISECT_LOG"},
}

// InProgressOp names the repository operation the worktree is partway
// through, or "" when it is not in one.
//
// It reads git's own markers rather than a persisted cursor, for the reason
// InMerge does: git holds the fact authoritatively, and a stored copy
// disagrees the moment a human runs `git rebase --abort` themselves. The
// markers live in the worktree's *own* git dir — `.git/worktrees/{name}` for a
// linked worktree, which is every worktree vincent makes — so the resolution
// gitDir already does is reused rather than restated.
func (m *Manager) InProgressOp(ctx context.Context, worktreePath string) (string, error) {
	dir, err := m.gitDir(ctx, worktreePath)
	if err != nil {
		return "", err
	}
	for _, marker := range inProgressMarkers {
		switch _, statErr := os.Stat(filepath.Join(dir, marker.path)); {
		case statErr == nil:
			return marker.op, nil
		case os.IsNotExist(statErr):
			continue
		default:
			return "", &Error{Reason: ReasonGitError, Err: statErr}
		}
	}
	return "", nil
}
