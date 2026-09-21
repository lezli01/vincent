package worktree

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lezli01/vincent/internal/gitx"
)

// The third worktree-creation mode: a task or chat that runs on an **existing**
// branch the user named, rather than on one vincent cut (§10, task 125).
//
// It is modelled on the pull-request mode rather than on the ordinary one,
// because it inverts the same two halves of "cut a new branch, refuse a
// pre-existing one": no `-b`, no `branch_exists` refusal, and an upstream that
// is left alone rather than suppressed with `--no-track`. It differs from the
// pull-request mode in the two places where that mode has a remote authority
// this one does not:
//
//   - There is no ref that *must* be reached. A pull request's head is the
//     task's branch, so a failed fetch is fatal; an adopted branch is already
//     on the machine, so a failed fetch leaves the local copy exactly where it
//     is and the task runs on it (fetchBase's policy, reused verbatim).
//   - A branch checked out in the project's own main checkout is not a
//     refusal. The pull-request mode blocks with ReasonPullBranchCheckedOut
//     because it *fast-forwards* the head onto whatever holds it, which would
//     move the human's working tree under them; this mode leaves a branch that
//     is ahead exactly where it is, so it has nothing to push into their tree
//     and can simply run in that checkout (decision 3). A branch held by one
//     of vincent's *own* worktrees is still a refusal — that directory belongs
//     to another owner.
//
// Adoption is never inferred from a branch that happens to exist (decision 1).
// It is selected by the create request, so every existing behaviour — task
// 001's `checkBranchCollision` 400 and the `branch_exists` block at admission —
// is untouched for every task that does not ask for it.

// Reason values this mode can block a task with (spec §18). Same snake_case
// vocabulary as every other reason: a `block_reason` means the same thing
// wherever it originated.
const (
	// ReasonAdoptBranchMissing: the branch the task was created to adopt does
	// not exist locally any more. Creation checks it, but creation is a
	// courtesy and admission is the authority — a branch can be deleted in
	// between, and the ordinary mode's base_branch_missing has exactly this
	// shape.
	ReasonAdoptBranchMissing = "adopt_branch_missing"
	// ReasonAdoptBranchDiverged: the adopted branch and its own upstream have
	// each moved. It is never `reset --hard` and never `branch -f`: the local
	// commits may be unpushed, which is the argument ReasonPullBranchDiverged
	// already makes, and here the local copy is the one the user pointed at.
	ReasonAdoptBranchDiverged = "adopt_branch_diverged"
	// ReasonAdoptBranchCheckedOut: the branch is checked out in one of
	// vincent's own worktrees, so another task or chat is working on it. git
	// cannot put one branch in two worktrees, and the *main* checkout is
	// deliberately not this case — that one runs in the checkout instead
	// (decision 3).
	ReasonAdoptBranchCheckedOut = "adopt_branch_checked_out"
)

// CreateAdoptAndClaim is CreateAndClaim's counterpart for a branch that
// already exists: it refreshes the branch from its own upstream where it has
// one, puts it in a worktree — or runs in the project's main checkout when
// that is where the branch already is — and hands the result to claim under
// the same claim lock (task 005).
//
// It is a third entry point rather than a flag on Create for the reason
// CreatePullAndClaim's own comment gives: a boolean would make every caller of
// the ordinary path read as though it were choosing.
func (m *Manager) CreateAdoptAndClaim(
	ctx context.Context, projectPath string, owner Owner, branch string,
	claim func(c Created) error,
) (Created, error) {
	m.claims.RLock()
	defer m.claims.RUnlock()
	c, err := m.createAdopt(ctx, projectPath, owner, branch)
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

func (m *Manager) createAdopt(
	ctx context.Context, projectPath string, owner Owner, branch string,
) (Created, error) {
	// Same repository lock as create and createPull, and for the same reason
	// (#126): the fetch, the ref update and the add must not have a peer
	// admission between them.
	unlock := m.lockRepo(projectPath)
	defer unlock()

	if err := m.requireProjectPath(projectPath); err != nil {
		return Created{}, err
	}
	if strings.TrimSpace(branch) == "" {
		return Created{}, &Error{
			Reason:  ReasonBranchNameInvalid,
			Message: "no branch to adopt",
		}
	}
	// The authority, not the courtesy: POST /v1/tasks checks the branch is
	// there, but a task can sit queued for as long as the caps say and the
	// branch can be deleted in the meantime.
	if !m.localBranchExists(ctx, projectPath, branch) {
		return Created{}, &Error{
			Reason:  ReasonAdoptBranchMissing,
			Message: fmt.Sprintf("branch %q does not exist in %s", branch, projectPath),
		}
	}
	if err := m.prune(ctx, projectPath); err != nil {
		return Created{}, err
	}

	// Asked *before* anything moves, exactly as createPull asks it: where the
	// branch is decides both whether a ref may be touched and which directory
	// the owner works in.
	where, err := m.branchCheckedOut(ctx, projectPath, branch)
	if err != nil {
		return Created{}, err
	}
	if where != "" && sameDir(where, projectPath) {
		return m.adoptMainCheckout(ctx, projectPath, branch)
	}
	if where != "" {
		return Created{}, &Error{
			Reason: ReasonAdoptBranchCheckedOut,
			Message: fmt.Sprintf("branch %q is already checked out in %s; git cannot put one branch in two worktrees",
				branch, where),
		}
	}

	target := m.Path(owner)
	if err := m.requireEmptyTarget(target); err != nil {
		return Created{}, err
	}
	out := Created{Path: target, Fetch: FetchOutcome{Result: FetchNoUpstream}}
	// The branch's *own* upstream, never `origin` — decision 5. A branch the
	// user deliberately kept local gets no `branch.{name}.remote` written for
	// it: that is repository configuration nobody asked for, and a later push
	// would create a remote branch nobody asked for either. fetchBase is
	// reused whole, including its policy that a failed fetch is silent and
	// the local ref is what the owner works on.
	up, hasUpstream := m.branchUpstream(ctx, projectPath, branch)
	if hasUpstream {
		sha, fo := m.fetchBase(ctx, projectPath, branch)
		out.Fetch = fo
		if sha != "" {
			// Not checked out anywhere — proved above — so the ref may move.
			// fastForwardBranch is the pull-request mode's: ahead is left
			// alone, behind moves, diverged refuses with no ref moved. Only
			// the reason differs, because a block_reason has to say which
			// mode produced it.
			if err := m.fastForwardBranch(ctx, projectPath, branch, sha); err != nil {
				return out, adoptDivergence(err, branch, sha)
			}
		}
	}
	if err := m.mkdirRoot(); err != nil {
		return out, err
	}
	addCtx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	// No `-b` and no `--no-track`: the branch exists, and whatever upstream it
	// carries is the user's, not something `git worktree add` invented.
	if _, err := m.git.Run(addCtx, projectPath, "worktree", "add", target, branch); err != nil {
		return out, &Error{Reason: ReasonGitError, Message: "git worktree add failed", Err: err}
	}
	if hasUpstream {
		// A no-op in the ordinary case — the configuration is already there,
		// which is how branchUpstream found it — and the one line that makes
		// "this branch keeps the upstream it had" explicit rather than
		// incidental. A branch with none stays with none (decision 5).
		if err := m.setUpstream(ctx, projectPath, branch, up.remote); err != nil {
			return out, err
		}
	}
	sha, err := m.branchTip(ctx, projectPath, branch)
	if err != nil {
		return out, err
	}
	// The tip at admission, not the fork point (§5.3, decision 7): the diff
	// then answers "what did this task change" rather than re-rendering the
	// branch's whole history.
	out.BaseSHA = sha
	return out, nil
}

// adoptMainCheckout is part 3 of the mode: the branch is checked out in the
// project's own main checkout, so the owner works *there* and no worktree is
// created (decision 3).
//
// Nothing is fetched and no ref is moved. The human's working tree is on this
// branch — possibly dirty, possibly mid-rebase — and moving the ref under them
// is the one thing §10 will not do to a checkout it does not own (task 099's
// fast-forward has the same refusal). The branch is used exactly as they left
// it, which is also what makes archive's job nothing: there is no directory to
// remove.
func (m *Manager) adoptMainCheckout(ctx context.Context, projectPath, branch string) (Created, error) {
	sha, err := m.branchTip(ctx, projectPath, branch)
	if err != nil {
		return Created{}, err
	}
	return Created{
		Path:    projectPath,
		BaseSHA: sha,
		// Never attempted rather than "no upstream": the branch may well have
		// one, and saying it did not would be a claim about the repository
		// instead of about what this admission did.
		Fetch: FetchOutcome{},
	}, nil
}

// adoptDivergence re-labels fastForwardBranch's divergence with this mode's
// own reason. Everything else it can return — a git failure reading or moving
// the ref — already means the same thing in both modes and passes through.
func adoptDivergence(err error, branch, sha string) error {
	if ReasonOf(err) != ReasonPullBranchDiverged {
		return err
	}
	return &Error{
		Reason: ReasonAdoptBranchDiverged,
		Message: fmt.Sprintf("branch %q has diverged from its upstream (%s); "+
			"vincent will not discard commits it cannot get back", branch, shortSHA(sha)),
	}
}

// branchTip resolves a local branch to the commit it points at.
func (m *Manager) branchTip(ctx context.Context, repo, branch string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	sha, err := m.git.Run(ctx, repo, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return "", &Error{Reason: ReasonGitError, Message: "read branch tip", Err: err}
	}
	sha = strings.TrimSpace(sha)
	if !fullHex.MatchString(sha) {
		return "", &Error{
			Reason:  ReasonGitError,
			Message: fmt.Sprintf("branch %q did not resolve to a commit", branch),
		}
	}
	return sha, nil
}

// Branch is one local branch of a project, as the branch listing reports it
// (§13.2, task 125). It is what the new-task and new-chat forms offer.
type Branch struct {
	// Name is the short branch name.
	Name string
	// CheckedOutIn is the working tree holding it, or "" when none does. The
	// project's own path here means the adopt mode will run in the main
	// checkout rather than in a worktree, which is the one thing a user
	// choosing a branch most needs to be told.
	CheckedOutIn string
	// Current marks the branch the project's main checkout has at HEAD.
	Current bool
}

// ListBranches returns the project's local branches in git's own ordering,
// each with the working tree holding it.
//
// Local only. A remote-tracking ref is not something `git worktree add` can
// take, and offering one would produce a name that then fails at admission
// with adopt_branch_missing — a picker that lists what cannot be used is
// worse than one that lists less.
func (m *Manager) ListBranches(ctx context.Context, repo string) ([]Branch, error) {
	if err := m.requireProjectPath(repo); err != nil {
		return nil, err
	}
	listCtx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	out, err := m.git.Run(listCtx, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, &Error{Reason: ReasonGitError, Message: "git for-each-ref failed", Err: err}
	}
	held, err := m.checkedOutBranches(ctx, repo)
	if err != nil {
		return nil, err
	}
	current, _ := m.git.Run(listCtx, repo, "rev-parse", "--abbrev-ref", "HEAD")
	current = strings.TrimSpace(current)
	var branches []Branch
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		branches = append(branches, Branch{
			Name:         name,
			CheckedOutIn: held[name],
			Current:      name == current,
		})
	}
	return branches, nil
}

// checkedOutBranches maps every branch git reports as checked out to the
// working tree holding it. It is branchCheckedOut's loop asked once for the
// whole list rather than once per branch: a project with two hundred branches
// would otherwise cost two hundred `git worktree list` calls to render one
// picker.
func (m *Manager) checkedOutBranches(ctx context.Context, repo string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	out, err := m.git.Run(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, &Error{Reason: ReasonGitError, Message: "git worktree list failed", Err: err}
	}
	held := map[string]string{}
	path := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			held[strings.TrimPrefix(line, "branch refs/heads/")] = path
		}
	}
	return held, nil
}

// SameDir reports whether two paths name the same directory. It is sameDir
// exported for the API's branch listing, which has to tell a client that a
// branch is held by the project's *own* checkout rather than by a worktree —
// the same comparison, asked from outside the package.
func SameDir(a, b string) bool { return sameDir(a, b) }

// sameDir compares two directory paths the way the rest of this package does:
// lexically, after Clean, with no symlink resolution.
//
// git prints worktree paths in its own form — `/private/var/...` on macOS,
// forward slashes on Windows — so both sides are run through filepath.Clean
// and, where the platform's paths are case-insensitive, folded. It is
// deliberately not os.SameFile: the comparison has to work for a path that
// does not exist, which is exactly the case Remove's early return asks about.
func sameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	if caseInsensitivePaths && strings.EqualFold(a, b) {
		return true
	}
	// git reports the resolved path; the project path may be the symlink the
	// user configured. Comparing the resolved forms catches macOS's
	// /var → /private/var without making the lexical case above depend on the
	// filesystem.
	ra, erra := filepath.EvalSymlinks(a)
	rb, errb := filepath.EvalSymlinks(b)
	if erra != nil || errb != nil {
		return false
	}
	if caseInsensitivePaths {
		return strings.EqualFold(ra, rb)
	}
	return ra == rb
}
