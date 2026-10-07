package worktree

// Merging a fan-out lane's branch into its parent's (spec §7.6, task 014
// decisions 7, 9).
//
// The git primitives live here rather than in the engine for the reason every
// other git call does: this package owns the repository vocabulary and the
// `Reason*` taxonomy a block_reason is drawn from.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lezli01/vincent/internal/gitx"
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
// The message is a machine-read contract, not prose: `diff?by=lane` parses
// it to attribute each merge to its lane (§7.6), so it is spelled here and
// nowhere else.
func (m *Manager) MergeLane(
	ctx context.Context, worktreePath, branch, laneID string, childID int64,
) (MergeResult, error) {
	msg := fmt.Sprintf("Merge lane '%s' of task %d", laneID, childID)
	return m.merge(ctx, worktreePath, branch, msg, fmt.Sprintf("lane %q", laneID))
}

// MergeBranch merges branch into the branch checked out in worktreePath with
// `--no-ff` and the caller's message, telling a conflict from a failure the
// way MergeLane does.
//
// It exists for merges that are not a fan-out lane's — the task-to-task
// merge-back (#761) — which must not reuse MergeLane's message: `diff?by=lane`
// parses that message, and a merge-back spelled like a lane would be
// attributed to a lane that never existed.
func (m *Manager) MergeBranch(
	ctx context.Context, worktreePath, branch, msg string,
) (MergeResult, error) {
	return m.merge(ctx, worktreePath, branch, msg, branch)
}

// merge is the one `git merge --no-ff`; label names what was merged in a git
// failure's text.
//
// --no-ff keeps each merge visible in history and matches the repo's own
// no-squash convention. No author or committer is set: vincent runs as the
// invoking user (§16) and has no business inventing an identity.
func (m *Manager) merge(
	ctx context.Context, worktreePath, branch, msg, label string,
) (MergeResult, error) {
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
		Err:    fmt.Errorf("merge %s: %w: %s", label, err, strings.TrimSpace(out)),
	}
}

// Merged reports whether branch is already an ancestor of the commit checked
// out in worktreePath — "has this lane's work landed on the parent's branch?".
//
// git is asked rather than a stored cursor, for the reason join.go gives about
// the merge cursor: a human who ran `git reset --hard` themselves is telling
// the truth and a persisted copy is not (task 014 decision 9). The round
// scheduler asks it once per spawned lane per round (§7.6, task 080).
func (m *Manager) Merged(ctx context.Context, worktreePath, branch string) (bool, error) {
	if branch == "" {
		return false, nil
	}
	if _, err := m.git.Run(ctx, worktreePath, "merge-base", "--is-ancestor", branch, "HEAD"); err != nil {
		// `--is-ancestor` reports "no" as exit 1, which is not an error to
		// report upward; anything else (an unknown ref, a broken repo) is.
		var ge *gitx.Error
		if errors.As(err, &ge) && ge.ExitCode == 1 {
			return false, nil
		}
		return false, fmt.Errorf("check whether %s is merged: %w", branch, err)
	}
	return true, nil
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
// message, for an `on_conflict: agent` resolver's prompt, and for
// ConflictMarkers, which opens each one.
//
// `-z` through RunRaw, for the reason ListFiles gives: without it git
// C-quotes a non-ASCII, quote, backslash or control-character path, and Run
// trims the output, so the names would not be the files' own. ConflictMarkers
// would then fail to open them, take them for deleted, and let their markers
// be committed (review F1 of #767).
func (m *Manager) ConflictedPaths(ctx context.Context, worktreePath string) ([]string, error) {
	out, err := m.git.RunRaw(ctx, worktreePath, "diff", "--name-only", "-z", "--diff-filter=U")
	if err != nil {
		return nil, &Error{Reason: ReasonGitError, Err: fmt.Errorf("list unmerged paths: %w", err)}
	}
	var paths []string
	for _, row := range bytes.Split(out, []byte{0}) {
		if len(row) > 0 {
			paths = append(paths, string(row))
		}
	}
	return paths, nil
}

// ConflictMarkers returns those of paths (worktree-relative, as
// ConflictedPaths lists them) whose content still holds a conflict marker: a
// line starting `<<<<<<< ` or `>>>>>>> `. A line that is exactly `=======` is
// not enough on its own: it is a legitimate setext heading underline under a
// seven-character title, and git never writes the separator without the two
// lines around it.
//
// It exists because the index cannot answer the question once the files are
// staged: `git add` clears a path's unmerged entry whatever the file holds, so
// an `on_conflict: agent` resolver that exits 0 without resolving would get
// its markers committed into the parent's branch (#756). The engine therefore
// reads the files the merge left conflicted before it stages anything.
//
// Plain file reads rather than `git diff --check`, which also reports
// whitespace errors in localizable text: no subprocess, no output to parse,
// and the same answer on every platform. A CRLF line ending is tolerated. A
// path the resolver deleted holds no markers — deleting is a resolution — and
// is skipped. Only git's default marker size (7) is recognised: a repository
// that sets `conflict-marker-size` is a documented limitation.
func (m *Manager) ConflictMarkers(_ context.Context, worktreePath string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	// Read through an os.Root so a path git listed can never reach outside
	// the worktree, symlinks included.
	root, err := os.OpenRoot(worktreePath)
	if err != nil {
		return nil, &Error{Reason: ReasonGitError, Err: fmt.Errorf("open worktree for conflict markers: %w", err)}
	}
	defer func() { _ = root.Close() }()
	var marked []string
	for _, p := range paths {
		data, err := root.ReadFile(filepath.FromSlash(p))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, &Error{Reason: ReasonGitError, Err: fmt.Errorf("read %s for conflict markers: %w", p, err)}
		}
		if hasConflictMarker(data) {
			marked = append(marked, p)
		}
	}
	return marked, nil
}

// hasConflictMarker reports whether any line of data is a default-size
// conflict marker.
func hasConflictMarker(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "<<<<<<< ") || strings.HasPrefix(line, ">>>>>>> ") {
			return true
		}
	}
	return false
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
