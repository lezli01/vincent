package worktree

import (
	"context"
	"fmt"
	"strings"

	"github.com/lezli01/vincent/internal/gitx"
)

// The second worktree-creation mode: a task whose branch **is** a pull
// request's head branch (§10, task 064 decision 2).
//
// Everything §10 says about the first mode is inverted here, and only here: a
// pre-existing branch of that name is the normal case rather than a refusal,
// the branch is not cut with `-b`, and it carries an upstream on purpose. That
// upstream is the whole point — it is what makes the agent's commits reach the
// pull request when a workflow pushes — and §10's "`--no-track` is not
// optional" is narrowed rather than reversed: its stated hazard is archive's
// remote leg pushing `--delete` against an inherited upstream, and decision 3
// closes that by never letting archive touch a branch vincent did not cut.

// Reason values this mode can block a task with (spec §18). Same snake_case
// vocabulary as every other reason: a `block_reason` means the same thing
// wherever it originated.
const (
	// ReasonPullFetchFailed: the pull request's head could not be fetched.
	// Unlike task 056's base fetch this is fatal rather than silent — there
	// is nothing to fall back to, because the head *is* the task's branch.
	ReasonPullFetchFailed = "pull_fetch_failed"
	// ReasonPullBranchDiverged: a local branch of the head's name exists and
	// carries commits the fetched head does not. It is never `reset --hard`:
	// those commits may be unpushed, and discarding them silently is the same
	// dishonesty §10 refuses for branch names.
	ReasonPullBranchDiverged = "pull_branch_diverged"
	// ReasonPullBranchCheckedOut: the head branch is already checked out in
	// another worktree — vincent's, or the user's own main checkout. git
	// cannot put one branch in two worktrees, and this is the honest way to
	// say so rather than letting git's message surface. It is also the
	// outside-vincent half of "one active task per pull request"; the inside
	// half is task 001's in-transaction claim check.
	ReasonPullBranchCheckedOut = "pull_branch_checked_out"
)

// PullSpec is what a pull-request task's worktree creation needs to know. It
// is assembled by the caller from the stored link and the live pull request,
// so this package still runs no GitHub call and stays a git-only leaf.
type PullSpec struct {
	// Number is the pull request number, used only to build a fork's
	// `refs/pull/{n}/head`.
	Number int
	// Branch is the head branch name, which is also the task's branch.
	Branch string
	// Ref is the full ref to fetch: `refs/heads/{head}` for a pull request
	// from this repository, `refs/pull/{n}/head` for one from a fork.
	Ref string
	// Fork says the head lives in another repository. A fork's branch is
	// created with **no upstream**: nothing can push back, and the daemon
	// does not `git remote add` the fork — §10 (task 056) states nothing
	// local is mutated, and a remote left behind after archive is exactly the
	// residue that rule exists to prevent (decision 5).
	Fork bool
}

// pullRemote resolves the remote a pull request's head is fetched from.
//
// The base branch's own upstream wins, for the reason fetchBase reads it
// rather than assuming a name: a local `master` tracking a remote called
// something other than `origin` must fetch from that one. `origin` is the
// fallback, and it is the honest one — the project's GitHub identity is
// derived from `origin` in the first place (task 035 decision 5), so a
// project whose pull requests vincent can see is a project with an `origin`.
func (m *Manager) pullRemote(ctx context.Context, repo, base string) string {
	if up, ok := m.branchUpstream(ctx, repo, base); ok && up.remote != "" {
		return up.remote
	}
	return "origin"
}

// CreatePullAndClaim is CreateAndClaim's pull-request counterpart: it fetches
// the head, puts the head branch in a worktree, and hands the result to claim
// under the same claim lock (task 005).
//
// It is a separate entry point rather than a flag on Create because the two
// modes share almost nothing but their locking: one refuses a pre-existing
// branch and the other expects it, one cuts with `-b --no-track` and the other
// checks out and tracks. A boolean parameter would have made every caller of
// the ordinary path read as though it were choosing.
func (m *Manager) CreatePullAndClaim(
	ctx context.Context, projectPath string, owner Owner, base string, spec PullSpec,
	claim func(c Created) error,
) (Created, error) {
	m.claims.RLock()
	defer m.claims.RUnlock()
	c, err := m.createPull(ctx, projectPath, owner, base, spec)
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

func (m *Manager) createPull(
	ctx context.Context, projectPath string, owner Owner, base string, spec PullSpec,
) (Created, error) {
	// Same repository lock as create, and for the same reason (#126): the
	// fetch, the FETCH_HEAD read and the add must not have a peer admission
	// between them.
	unlock := m.lockRepo(projectPath)
	defer unlock()

	if err := m.requireProjectPath(projectPath); err != nil {
		return Created{}, err
	}
	if strings.TrimSpace(spec.Branch) == "" {
		return Created{}, &Error{
			Reason:  ReasonBranchNameInvalid,
			Message: "pull request has no head branch to check out",
		}
	}
	// No branch_exists refusal here. The head branch existing locally is the
	// normal case for anyone who has already looked at the pull request, and
	// refusing it would make the feature unusable for exactly the people who
	// asked for it.
	if err := m.prune(ctx, projectPath); err != nil {
		return Created{}, err
	}
	target := m.Path(owner)
	if err := m.requireEmptyTarget(target); err != nil {
		return Created{}, err
	}

	remote := m.pullRemote(ctx, projectPath, base)
	sha, fo := m.fetchPullHead(ctx, projectPath, remote, spec.Ref)
	out := Created{Path: target, Fetch: fo}
	if sha == "" {
		return out, &Error{
			Reason: ReasonPullFetchFailed,
			Message: fmt.Sprintf("could not fetch %s from %s for pull request #%d: %s",
				spec.Ref, remote, spec.Number, fo.Error),
		}
	}
	out.BaseSHA = sha

	// A branch already in a worktree — vincent's own, or the human's main
	// checkout — is checked *before* the ref is moved: a fast-forward of a
	// checked-out branch would leave that working tree describing a commit it
	// does not contain.
	if where, err := m.branchCheckedOut(ctx, projectPath, spec.Branch); err != nil {
		return out, err
	} else if where != "" {
		return out, &Error{
			Reason: ReasonPullBranchCheckedOut,
			Message: fmt.Sprintf("branch %q is already checked out in %s; git cannot put one branch in two worktrees",
				spec.Branch, where),
		}
	}
	if m.localBranchExists(ctx, projectPath, spec.Branch) {
		if err := m.fastForwardBranch(ctx, projectPath, spec.Branch, sha); err != nil {
			return out, err
		}
	} else if err := m.createBranchAt(ctx, projectPath, spec.Branch, sha); err != nil {
		return out, err
	}

	if err := m.mkdirRoot(); err != nil {
		return out, err
	}
	addCtx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	// No `-b`: the branch already exists by now, either because the human had
	// it or because the step above created it at the fetched head.
	if _, err := m.git.Run(addCtx, projectPath, "worktree", "add", target, spec.Branch); err != nil {
		return out, &Error{Reason: ReasonGitError, Message: "git worktree add failed", Err: err}
	}
	// The upstream *is* the deliverable on a same-repository pull request:
	// without it a workflow's `git push` has nothing to push to. A fork gets
	// none, so a delivery step fails loudly instead of pushing somewhere
	// nobody is watching.
	if !spec.Fork {
		if err := m.setUpstream(ctx, projectPath, spec.Branch, remote); err != nil {
			return out, err
		}
	}
	return out, nil
}

// fastForwardBranch moves branch to sha when sha is a descendant of it, and
// refuses otherwise.
//
// `--no-deref`-free `update-ref` with the old value stated is what makes this
// safe against a concurrent writer: git refuses the update if the ref moved
// since the read. It is deliberately not `reset --hard` and not `branch -f`
// without the ancestry test — the local copy may hold commits nobody has
// pushed, and there is no way to get them back afterwards (decision 4).
func (m *Manager) fastForwardBranch(ctx context.Context, repo, branch, sha string) error {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	ref := "refs/heads/" + branch
	old, err := m.git.Run(ctx, repo, "rev-parse", "--verify", ref)
	if err != nil {
		return &Error{Reason: ReasonGitError, Message: "read local head branch", Err: err}
	}
	old = strings.TrimSpace(old)
	if old == sha {
		return nil
	}
	// Already ahead of the fetched head: the local branch contains it. Not a
	// divergence, and nothing to do — the extra commits are the human's, and
	// a push is what will reconcile them with the pull request.
	if m.isAncestor(ctx, repo, sha, old) {
		return nil
	}
	if !m.isAncestor(ctx, repo, old, sha) {
		return &Error{
			Reason: ReasonPullBranchDiverged,
			Message: fmt.Sprintf("local branch %q has diverged from the pull request's head (%s); "+
				"vincent will not discard commits it cannot get back", branch, shortSHA(sha)),
		}
	}
	if _, err := m.git.Run(ctx, repo, "update-ref", ref, sha, old); err != nil {
		return &Error{Reason: ReasonGitError, Message: "fast-forward local head branch", Err: err}
	}
	return nil
}

// createBranchAt creates branch at sha with no upstream. The upstream is set
// afterwards and only for a same-repository pull request, so a fork's branch
// never acquires one by inheritance.
func (m *Manager) createBranchAt(ctx context.Context, repo, branch, sha string) error {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	if _, err := m.git.Run(ctx, repo, "branch", "--no-track", branch, sha); err != nil {
		return &Error{Reason: ReasonGitError, Message: "create head branch", Err: err}
	}
	return nil
}

// setUpstream configures `branch.{branch}.remote` + `.merge` directly rather
// than through `git branch --set-upstream-to`, which needs a remote-tracking
// ref to exist — and the head was fetched by ref, so there may be none.
func (m *Manager) setUpstream(ctx context.Context, repo, branch, remote string) error {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	for key, value := range map[string]string{
		"branch." + branch + ".remote": remote,
		"branch." + branch + ".merge":  "refs/heads/" + branch,
	} {
		if _, err := m.git.Run(ctx, repo, "config", "--local", key, value); err != nil {
			return &Error{Reason: ReasonGitError, Message: "configure branch upstream", Err: err}
		}
	}
	return nil
}

// branchCheckedOut names the worktree holding branch, or "" when none does.
// `git worktree list --porcelain` reports the main checkout too, which is the
// case that matters most: a human with the pull request's branch open is the
// likeliest way this blocks.
func (m *Manager) branchCheckedOut(ctx context.Context, repo, branch string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	out, err := m.git.Run(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return "", &Error{Reason: ReasonGitError, Message: "git worktree list failed", Err: err}
	}
	want := "refs/heads/" + branch
	path := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == "branch "+want:
			return path, nil
		}
	}
	return "", nil
}

func (m *Manager) isAncestor(ctx context.Context, repo, ancestor, descendant string) bool {
	_, err := m.git.Run(ctx, repo, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
