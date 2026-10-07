package worktree

// The fourth worktree-creation mode: an issue's main-role task, which shares
// one branch and one directory with every other main-role task of its issue
// (task 134 decisions 3, 7, 16, 17; 134.12).
//
// The directory normally arrives by hand-over: the engine moves the settled
// predecessor's claim to the successor in one store transaction, and this
// package's part is only to read the branch tip under the claim lock
// (HandOverUnderClaim). What lives here besides is the two cases with no
// directory to hand over — the branch is checked out in the human's main
// checkout, which blocks, and every earlier main task was archived, which
// puts the branch in a fresh worktree (CreateIssueMainAndClaim).

import (
	"context"
	"fmt"
	"strings"

	"github.com/lezli01/vincent/internal/gitx"
)

// ReasonIssueBranchCheckedOut is the block for an issue's main branch checked
// out in the project's own main checkout (decision 16). Task 125's adopt mode
// would run in that checkout instead; an issue's main task never does,
// because the directory then has two owners — the human and every main task of the
// issue in turn — and the hand-over could move it under the human.
const ReasonIssueBranchCheckedOut = "issue_branch_checked_out"

// BranchInMainCheckout reports whether branch is checked out in the
// project's own main checkout, rather than in a linked worktree or nowhere.
// It is branchCheckedOut — the probe adopt mode decides with — asked from the
// engine, which has to refuse before it touches any directory.
func (m *Manager) BranchInMainCheckout(ctx context.Context, projectPath, branch string) (bool, error) {
	if err := m.requireProjectPath(projectPath); err != nil {
		return false, err
	}
	where, err := m.branchCheckedOut(ctx, projectPath, branch)
	if err != nil {
		return false, err
	}
	return where != "" && sameDir(where, projectPath), nil
}

// LocalBranchExists reports whether branch exists in the project repository.
// A git failure reads as "no", which then reaches the ordinary cut — whose
// own checks refuse with a reason rather than guess.
func (m *Manager) LocalBranchExists(ctx context.Context, projectPath, branch string) bool {
	return m.localBranchExists(ctx, projectPath, branch)
}

// HandOverUnderClaim reads branch's tip and passes it to hand, both under the
// claim lock CreateAndClaim and RemoveAndRelease take (task 005). hand is the
// store transaction that moves a directory from one row to another; a gc scan
// landing between the read and the write would otherwise see the tip of one
// owner and the claim of the other, and a scan inside the transfer would see
// neither — which is the window the lock exists to close.
func (m *Manager) HandOverUnderClaim(
	ctx context.Context, projectPath, branch string, hand func(tip string) error,
) error {
	m.claims.RLock()
	defer m.claims.RUnlock()
	tip, err := m.branchTip(ctx, projectPath, branch)
	if err != nil {
		return err
	}
	return hand(tip)
}

// CreateIssueMainAndClaim puts an issue's existing main branch in a fresh
// worktree and hands the result to claim under the claim lock. It is the case
// where no directory survives to be handed over: every main task that held
// one has been archived, but a successor still carries the branch.
//
// It is createAdopt's mechanics without two of its halves. There is no
// main-checkout fallback — the engine has already blocked that case with
// ReasonIssueBranchCheckedOut, and it is refused here again rather than
// trusted — and there is no fetch: the branch is vincent's own, cut by the
// issue's first main task, so its upstream is wherever vincent pushed it, and
// a fast-forward from there would be a ref move nobody asked for. BaseSHA is
// the tip, so the successor's diff is its own work (§5.3).
func (m *Manager) CreateIssueMainAndClaim(
	ctx context.Context, projectPath string, owner Owner, branch string,
	claim func(c Created) error,
) (Created, error) {
	m.claims.RLock()
	defer m.claims.RUnlock()
	c, err := m.createIssueMain(ctx, projectPath, owner, branch)
	if err != nil {
		return c, err
	}
	if claim != nil {
		if err := claim(c); err != nil {
			return c, err
		}
	}
	return c, nil
}

func (m *Manager) createIssueMain(
	ctx context.Context, projectPath string, owner Owner, branch string,
) (Created, error) {
	// The repository lock, for the reason create takes it (#126).
	unlock := m.lockRepo(projectPath)
	defer unlock()

	if err := m.requireProjectPath(projectPath); err != nil {
		return Created{}, err
	}
	if strings.TrimSpace(branch) == "" {
		return Created{}, &Error{Reason: ReasonBranchNameInvalid, Message: "no issue branch to check out"}
	}
	if !m.localBranchExists(ctx, projectPath, branch) {
		return Created{}, &Error{
			Reason:  ReasonAdoptBranchMissing,
			Message: fmt.Sprintf("the issue's main branch %q does not exist in %s", branch, projectPath),
		}
	}
	if err := m.prune(ctx, projectPath); err != nil {
		return Created{}, err
	}
	where, err := m.branchCheckedOut(ctx, projectPath, branch)
	if err != nil {
		return Created{}, err
	}
	switch {
	case where != "" && sameDir(where, projectPath):
		return Created{}, issueBranchInMainCheckout(branch, projectPath)
	case where != "":
		return Created{}, &Error{
			Reason: ReasonAdoptBranchCheckedOut,
			Message: fmt.Sprintf("the issue's main branch %q is already checked out in %s; "+
				"git cannot put one branch in two worktrees", branch, where),
		}
	}
	target := m.Path(owner)
	if err := m.requireEmptyTarget(target); err != nil {
		return Created{}, err
	}
	if err := m.mkdirRoot(); err != nil {
		return Created{}, err
	}
	addCtx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	// No `-b`: the branch exists. No `--no-track` either, because nothing is
	// created that could inherit an upstream — the branch keeps what it had.
	if _, err := m.git.Run(addCtx, projectPath, "worktree", "add", target, branch); err != nil {
		return Created{}, &Error{Reason: ReasonGitError, Message: "git worktree add failed", Err: err}
	}
	sha, err := m.branchTip(ctx, projectPath, branch)
	if err != nil {
		return Created{Path: target}, err
	}
	return Created{Path: target, BaseSHA: sha}, nil
}

// IssueBranchInMainCheckout is the block the engine raises when the issue's
// main branch is in the project's main checkout (decision 16).
func IssueBranchInMainCheckout(branch, projectPath string) error {
	return issueBranchInMainCheckout(branch, projectPath)
}

func issueBranchInMainCheckout(branch, projectPath string) error {
	return &Error{
		Reason: ReasonIssueBranchCheckedOut,
		Message: fmt.Sprintf("the issue's main branch %q is checked out in the project's main checkout %s; "+
			"switch that checkout to another branch, then retry", branch, projectPath),
	}
}

// BranchTip resolves a local branch to the commit it points at. Archive
// reads it to stamp a main-role task's end_sha (134.12) before the worktree
// that has the branch checked out is removed.
func (m *Manager) BranchTip(ctx context.Context, projectPath, branch string) (string, error) {
	return m.branchTip(ctx, projectPath, branch)
}
