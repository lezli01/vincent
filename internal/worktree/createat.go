package worktree

import (
	"context"
	"fmt"

	"github.com/lezli01/vincent/internal/gitx"
)

// CreateAtAndClaim is CreateAndClaim for a branch cut from a commit rather
// than from a base branch's tip: a chat forked from a turn's checkpoint
// starts where that turn started (task 167 decision 11), which is wherever
// the source chat's branch then stood. There is no fetch and no fast-forward
// — the start point is a commit already in the repository, and refreshing a
// base it was never cut from would mean nothing — so the Created it returns
// records neither, and BaseSHA is left "": the caller records the base it
// inherits (decision C).
//
// It is its own entry point for the reason CreatePullAndClaim is: the
// ordinary cut's base checks and refresh do not apply, and a flag on Create
// would make every ordinary caller read as though it were choosing.
func (m *Manager) CreateAtAndClaim(
	ctx context.Context, projectPath string, owner Owner, branch, startSHA string,
	claim func(c Created) error,
) (Created, error) {
	m.claims.RLock()
	defer m.claims.RUnlock()
	c, err := m.createAt(ctx, projectPath, owner, branch, startSHA)
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

func (m *Manager) createAt(ctx context.Context, projectPath string, owner Owner, branch, startSHA string) (Created, error) {
	// The same repository lock and pre-flight checks as create (#126).
	unlock := m.lockRepo(projectPath)
	defer unlock()
	if err := m.requireProjectPath(projectPath); err != nil {
		return Created{}, err
	}
	qctx, qcancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	_, err := m.git.Run(qctx, projectPath, "rev-parse", "--verify", "--quiet", startSHA+"^{commit}")
	qcancel()
	if err != nil {
		return Created{}, &Error{
			Reason:  ReasonGitError,
			Message: fmt.Sprintf("start point %s is not a commit in %s", startSHA, projectPath), Err: err,
		}
	}
	if m.localBranchExists(ctx, projectPath, branch) {
		return Created{}, &Error{
			Reason:  ReasonBranchExists,
			Message: fmt.Sprintf("branch %q already exists in %s (never reused)", branch, projectPath),
		}
	}
	if err := m.prune(ctx, projectPath); err != nil {
		return Created{}, err
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
	// --no-track for create's reason; a SHA start point carries no upstream
	// anyway, so this is only the belt.
	if _, err := m.git.Run(addCtx, projectPath,
		"worktree", "add", target, "-b", branch, "--no-track", startSHA); err != nil {
		return Created{}, &Error{Reason: ReasonGitError, Message: "git worktree add failed", Err: err}
	}
	return Created{
		Path:        target,
		Fetch:       FetchOutcome{Result: FetchDisabled},
		FastForward: FastForwardOutcome{Result: FastForwardNotAttempted},
	}, nil
}
