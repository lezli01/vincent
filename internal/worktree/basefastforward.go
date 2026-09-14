package worktree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lezli01/vincent/internal/gitx"
)

// Outcomes of the fast-forward of a project's local base branch that follows
// a successful base fetch (§10, issue #430). Results rather than failures, on
// the Fetch* constants' convention: every one of them describes a task that was
// created exactly as asked, from the fetched commit, and none ever becomes a
// block_reason. What is recorded is what happened to the *human's* branch, which
// the task does not depend on.
const (
	// FastForwardAdvanced: refs/heads/{base} moved to the fetched commit, and
	// so did the working tree of the checkout holding it, if any.
	FastForwardAdvanced = "advanced"
	// FastForwardUpToDate: the local base already was the fetched commit.
	FastForwardUpToDate = "up_to_date"
	// FastForwardSkipped: moving the branch was not safe or did not work;
	// Reason says which. Nothing is left half-moved.
	FastForwardSkipped = "skipped"
	// FastForwardNotAttempted: there was no fetched commit to move to — the
	// fetch was off, found no upstream, or failed.
	FastForwardNotAttempted = "not_attempted"
)

// Why a fast-forward was skipped (FastForwardOutcome.Reason, set only when
// Result is FastForwardSkipped).
const (
	// SkipDiverged: the local base carries commits the upstream does not, and
	// the upstream carries commits it does not. Only a merge or a rebase can
	// reconcile them, and that is the human's call.
	SkipDiverged = "diverged"
	// SkipLocalAhead: the local base already contains the fetched commit, plus
	// commits of its own that nobody has pushed yet.
	SkipLocalAhead = "local_ahead"
	// SkipCheckoutDirty: the base is checked out with local changes, untracked
	// files included (the T1.5/T1.6 rule IsDirty states).
	SkipCheckoutDirty = "checkout_dirty"
	// SkipCheckoutBusy: the checkout holding the base is partway through a
	// merge, rebase, cherry-pick, revert or bisect (InProgressOp).
	SkipCheckoutBusy = "checkout_busy"
	// SkipError: a git command failed, or a concurrent writer moved the branch
	// between the read and the update. Error carries git's message.
	SkipError = "error"
)

// FastForwardOutcome is what fastForwardBase did to the local base branch. The
// zero value means creation never considered it, which is the pull-request mode
// (createPull): that mode fetches a head, not the base, and must not claim a base
// fast-forward happened. It is returned rather than logged, for the reason
// FetchOutcome is.
type FastForwardOutcome struct {
	// Result is one of the FastForward* constants.
	Result string
	// Reason is one of the Skip* constants when Result is FastForwardSkipped.
	Reason string
	// Worktree is the checkout whose working tree moved with the branch, set
	// only when Result is FastForwardAdvanced and the base was checked out.
	Worktree string
	// Error carries git's own message when Reason is SkipError.
	Error string
}

// Skipped reports whether a fast-forward was considered and did not happen.
// Up to date and not attempted are not skips: neither leaves the human with a
// stale branch that this call could have refreshed.
func (o FastForwardOutcome) Skipped() bool { return o.Result == FastForwardSkipped }

// fastForwardBase moves the project's local base branch to sha, the commit the
// base fetch just resolved, when that loses nothing and disturbs nobody. It runs
// inside create's repository lock, after fetchBase and before the task's
// `worktree add`, and whatever it answers the task branch still starts at sha:
// this refreshes the human's branch, it is not what the task is built from.
//
// Only a strict fast-forward moves anything. A local base that is ahead of the
// upstream or has diverged from it holds commits that may exist nowhere else, so
// it is left exactly as it is and the outcome says why.
//
// A base that is checked out — usually in the human's own main checkout, but
// `worktree list` names linked worktrees too — moves with its working tree or
// not at all. Moving only the ref would leave that checkout's HEAD naming a
// commit its index and files do not contain, which reads as every upstream
// change reverted and staged. So a checkout that is dirty (untracked included)
// or partway through an operation is skipped, and a clean one is switched with
// `read-tree -m -u` before the ref is written. read-tree is itself the last
// line of defence: it refuses to overwrite a file that changed after the check,
// or an ignored file the new tree tracks, and the refusal is recorded as an
// error with nothing else run.
//
// Plumbing only — never `merge --ff-only`, `pull` or `checkout`. Those run
// post-merge and post-checkout hooks, and a hook of the human's firing in their
// own checkout, under a lock that is holding a task admission, is not something
// this step may cause. (`update-ref` still fires reference-transaction, as the
// `worktree add` that follows already does.)
//
// The ref write is a compare-and-swap, `update-ref {ref} {new} {old}`, so a
// human committing or pulling concurrently is never overwritten: git refuses,
// the working tree is switched back, and the outcome is an error. Nothing here
// is returned as a failure — a base the daemon could not refresh is a line in
// the log, never a blocked task.
func (m *Manager) fastForwardBase(ctx context.Context, repo, base, sha string) FastForwardOutcome {
	ref := "refs/heads/" + base
	old, err := m.readRef(ctx, repo, ref)
	if err != nil {
		return skippedWithError(err)
	}
	if old == sha {
		return FastForwardOutcome{Result: FastForwardUpToDate}
	}
	// Local ahead is tested before divergence because it is the narrower
	// answer: an ahead branch also fails "old is an ancestor of new".
	if ahead, err := m.ancestorOf(ctx, repo, sha, old); err != nil {
		return skippedWithError(err)
	} else if ahead {
		return skipped(SkipLocalAhead)
	}
	if behind, err := m.ancestorOf(ctx, repo, old, sha); err != nil {
		return skippedWithError(err)
	} else if !behind {
		return skipped(SkipDiverged)
	}

	where, err := m.branchCheckedOut(ctx, repo, base)
	if err != nil {
		return skippedWithError(err)
	}
	if where == "" {
		if err := m.casRef(ctx, repo, ref, sha, old); err != nil {
			return skippedWithError(err)
		}
		return FastForwardOutcome{Result: FastForwardAdvanced}
	}
	if skip, err := m.checkoutRefusal(ctx, where); err != nil {
		return skippedWithError(err)
	} else if skip != "" {
		return skipped(skip)
	}

	if err := m.readTree(ctx, where, old, sha); err != nil {
		return skippedWithError(err)
	}
	if err := m.casRef(ctx, repo, ref, sha, old); err != nil {
		// The branch moved under us, so the tree just switched to sha
		// describes nothing HEAD names. Switch it back to old — the state the
		// checkout was verified clean in — rather than leave the two
		// disagreeing.
		if rbErr := m.readTree(ctx, where, sha, old); rbErr != nil {
			return skippedWithError(fmt.Errorf("%w; switching %s back also failed: %w", err, where, rbErr))
		}
		return skippedWithError(err)
	}
	return FastForwardOutcome{Result: FastForwardAdvanced, Worktree: filepath.Clean(where)}
}

func skipped(reason string) FastForwardOutcome {
	return FastForwardOutcome{Result: FastForwardSkipped, Reason: reason}
}

func skippedWithError(err error) FastForwardOutcome {
	return FastForwardOutcome{Result: FastForwardSkipped, Reason: SkipError, Error: err.Error()}
}

// checkoutRefusal names the Skip* reason a checkout holding the base cannot be
// switched for, or "" when it is clean and idle. Busy is asked first: a stopped
// merge or rebase is almost always dirty as well, and "an operation is in
// progress" is the more useful thing to tell a human who has forgotten one.
func (m *Manager) checkoutRefusal(ctx context.Context, where string) (string, error) {
	opCtx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	if op, err := m.InProgressOp(opCtx, where); err != nil {
		return "", err
	} else if op != "" {
		return SkipCheckoutBusy, nil
	}
	// `status` also refreshes the index's stat data, which read-tree's
	// up-to-date check depends on.
	if dirty, err := m.IsDirty(ctx, where); err != nil {
		return "", err
	} else if dirty {
		return SkipCheckoutDirty, nil
	}
	return "", nil
}

func (m *Manager) readRef(ctx context.Context, repo, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	return m.git.Run(ctx, repo, "rev-parse", "--verify", ref)
}

// ancestorOf is isAncestor with git's failures kept apart from its answer.
// `merge-base --is-ancestor` exits 1 for "no" and something else for "could
// not tell"; isAncestor folds both into false, which here would turn an
// unreadable object into a confident "diverged".
func (m *Manager) ancestorOf(ctx context.Context, repo, ancestor, descendant string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	_, err := m.git.Run(ctx, repo, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var gerr *gitx.Error
	if errors.As(err, &gerr) && gerr.ExitCode == 1 && gerr.Err == nil {
		return false, nil
	}
	return false, err
}

// readTree switches the checkout at dir from tree from to tree to, index and
// files, the way a checkout would but without running its hooks. It takes the
// worktree timeout because it writes files, and a large change is a large
// checkout.
func (m *Manager) readTree(ctx context.Context, dir, from, to string) error {
	ctx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	_, err := m.git.Run(ctx, dir, "read-tree", "-m", "-u", from, to)
	return err
}

// casRef writes ref to sha only if it still holds old. The test seam runs first
// so an in-package test can play the concurrent writer the old value guards
// against.
func (m *Manager) casRef(ctx context.Context, repo, ref, sha, old string) error {
	if m.beforeBaseRefUpdate != nil {
		m.beforeBaseRefUpdate()
	}
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	_, err := m.git.Run(ctx, repo, "update-ref",
		"-m", "vincent: fast-forward to "+strings.TrimPrefix(ref, "refs/heads/")+"'s fetched upstream",
		ref, sha, old)
	return err
}
