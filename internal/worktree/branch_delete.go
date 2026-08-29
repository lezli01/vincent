package worktree

import (
	"context"
	"fmt"
	"os"

	"github.com/lezli01/vincent/internal/gitx"
)

// Outcomes of the archive-time branch check (§10, task 008). They are the same
// snake_case vocabulary as the Reason constants above — one word means the same
// thing wherever it surfaces — but they are *results*, not failures: three of
// the four describe a branch that is still there, which is the pre-008
// behaviour and not an error in itself.
const (
	// BranchDeleted: the branch had no commits past its base and is gone.
	BranchDeleted = "deleted"
	// BranchHasCommits: the branch carries work, so it was left alone. Nothing
	// that holds a commit object is ever deleted.
	BranchHasCommits = "has_commits"
	// BranchUnknown: git could not say whether the branch is empty — the base
	// branch was renamed or deleted, the repository is gone. Read as *cannot
	// judge*, which keeps the branch, deliberately distinct from
	// BranchHasCommits the way ReasonDirtyUnknown is from ReasonWorktreeDirty.
	BranchUnknown = "unknown"
	// BranchDeleteFailed: the branch qualified but `git branch -d` refused or
	// failed. It survives.
	BranchDeleteFailed = "error"
)

// Outcomes of the remote leg, which only runs on an attended archive with
// delete_remote_branch_on_archive on (§10, task 008).
const (
	// RemoteBranchDeleted: `git push --delete` succeeded.
	RemoteBranchDeleted = "deleted"
	// RemoteBranchNoUpstream: the local branch had no configured upstream, so
	// nothing was pushed as far as vincent knows and nothing was attempted.
	RemoteBranchNoUpstream = "no_upstream"
	// RemoteBranchFailed: the push was rejected, the host was unreachable, or
	// the call timed out. The local branch is already gone and the archive
	// still succeeded.
	RemoteBranchFailed = "error"
)

// BranchOutcome is what archive did to a task's branch. A zero value means the
// branch step never ran — the policy is off, or the task had no branch of its
// own to judge — which is what every path before task 008 did unconditionally.
type BranchOutcome struct {
	// Branch is the local branch this is about; empty when nothing was checked.
	Branch string
	// Result is one of the Branch* constants, or "" when nothing was checked.
	Result string
	// Error carries git's own message when Result is BranchUnknown or
	// BranchDeleteFailed. It is reported, never acted on: an archive is never
	// failed by a branch problem.
	Error string
	// Remote is the remote leg's outcome, non-nil only when it ran.
	Remote *RemoteBranchOutcome
}

// Checked reports whether the branch step ran at all.
func (o BranchOutcome) Checked() bool { return o.Result != "" }

// RemoteBranchOutcome is what the opt-in remote leg did.
type RemoteBranchOutcome struct {
	// Remote is the configured remote name (`branch.{name}.remote`), empty
	// when there was no upstream to resolve.
	Remote string
	// Ref is the full ref deleted on that remote (`branch.{name}.merge`).
	Ref string
	// Result is one of the RemoteBranch* constants.
	Result string
	// Error carries git's own message when Result is RemoteBranchFailed.
	Error string
}

// upstream is a branch's configured push target, read from git config rather
// than from `@{upstream}`: the remote-tracking ref can be pruned while the
// configuration that named it survives, and the question here is "did vincent
// ever push this", which the configuration answers and a pruned ref does not.
type upstream struct {
	remote string
	ref    string
}

// DeleteEmptyBranch deletes branch when it carries no commits past base, and
// reports what happened (§10, task 008). It is the *one* exception to §10's
// rule that vincent never deletes a branch: a branch whose tip is an ancestor
// of its base holds nothing a user could want back.
//
// The test is `git rev-list -n 1 base..branch` producing no output. It stays
// correct when the base moves forward after the task started — the tip is
// still an ancestor — and costs one cheap git call. Any git failure is read as
// *cannot judge* and leaves the branch alone.
//
// baseSHA is the commit the task branch was actually cut from, when the task
// recorded one (§5.3, task 056), and it wins over base. Without it a task that
// wrote nothing but started at a fetched upstream tip is *ahead* of the local
// base branch, the answer flips to BranchHasCommits, and
// delete_empty_branch_on_archive silently stops firing for every project whose
// local base is behind its remote.
//
// It never fails a caller: the error is returned for logging, and the outcome
// says what the repository now looks like. Archive has already happened by the
// time this runs, and a branch problem must not be able to reverse it.
//
// deleteRemote additionally deletes the branch's upstream counterpart, and is
// honoured only on an attended archive (§10): deleting a branch on a forge
// other people share is unrecoverable and outward-facing, so the unattended
// paths never pass true.
func (m *Manager) DeleteEmptyBranch(
	ctx context.Context, projectPath, base, baseSHA, branch string, deleteRemote bool,
) (BranchOutcome, error) {
	out := BranchOutcome{Branch: branch}
	if branch == "" {
		return BranchOutcome{}, nil
	}
	if _, err := os.Stat(projectPath); err != nil {
		e := &Error{
			Reason:  ReasonProjectPathMissing,
			Message: fmt.Sprintf("project path %s does not exist", projectPath), Err: err,
		}
		out.Result, out.Error = BranchUnknown, e.Error()
		return out, e
	}
	empty, err := m.branchIsEmpty(ctx, projectPath, baseRev(base, baseSHA), branch)
	if err != nil {
		out.Result, out.Error = BranchUnknown, err.Error()
		return out, err
	}
	if !empty {
		out.Result = BranchHasCommits
		return out, nil
	}
	// Resolved *before* the delete, though it is only used after one that
	// succeeds: `git branch -d` drops the branch's own config section, taking
	// `branch.{name}.remote` with it, so asking afterwards always answers "no
	// upstream". The ordering the outcome describes is unchanged — no push is
	// attempted unless the local delete worked.
	var up upstream
	var hasUpstream bool
	if deleteRemote {
		up, hasUpstream = m.branchUpstream(ctx, projectPath, branch)
	}
	// Forced only when baseSHA answered the emptiness question, and see
	// deleteLocalBranch for why that is not the weakening it looks like.
	if err := m.deleteLocalBranch(ctx, projectPath, branch, baseSHA != ""); err != nil {
		out.Result, out.Error = BranchDeleteFailed, err.Error()
		return out, err
	}
	out.Result = BranchDeleted
	if !deleteRemote {
		return out, nil
	}
	if !hasUpstream {
		out.Remote = &RemoteBranchOutcome{Result: RemoteBranchNoUpstream}
		return out, nil
	}
	rem := &RemoteBranchOutcome{Remote: up.remote, Ref: up.ref, Result: RemoteBranchDeleted}
	out.Remote = rem
	if err := m.pushDeleteBranch(ctx, projectPath, up); err != nil {
		rem.Result, rem.Error = RemoteBranchFailed, err.Error()
		return out, err
	}
	return out, nil
}

// branchIsEmpty reports whether branch's tip is an ancestor of baseRev. Both
// revisions are unambiguous — baseRev is either a full `refs/heads/` name or a
// resolved object name — because an ambiguous short name (a tag sharing the
// branch's name, a deleted base branch with a surviving remote-tracking ref)
// must read as *cannot judge* rather than resolve to something else and answer
// confidently.
func (m *Manager) branchIsEmpty(ctx context.Context, repo, baseRev, branch string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	out, err := m.git.Run(ctx, repo, "rev-list", "-n", "1",
		baseRev+"..refs/heads/"+branch)
	if err != nil {
		return false, &Error{
			Reason:  ReasonGitError,
			Message: fmt.Sprintf("git rev-list %s..%s failed", baseRev, branch), Err: err,
		}
	}
	return out == "", nil
}

// deleteLocalBranch runs `git branch -d`, and `-D` only when the caller
// already proved the branch empty against the commit it was actually cut from.
//
// `-d` was task 008's only form, as a second belt behind the rev-list. Its own
// check is "merged into HEAD, or into this branch's upstream" — and HEAD in the
// project repository is the human's local base branch, which is behind the
// remote in precisely the situation task 056's fetch exists for. A task branch
// cut from a fetched upstream tip that wrote *nothing* is still ahead of local
// HEAD, so `-d` refuses it and delete_empty_branch_on_archive stops firing for
// every project whose local base is stale. The rev-list against the recorded
// base SHA is the better authority there: it asks the question `-d` is
// approximating, against the right commit.
//
// What is not given up is the guard that matters. git refuses to delete a
// branch checked out in any worktree with `-D` exactly as with `-d`, so the
// working-tree corruption case is still covered — by git, not by the flag.
// Without a recorded base SHA nothing has been proved against the right
// commit, so `-d` stays, exactly as task 008 wrote it.
func (m *Manager) deleteLocalBranch(ctx context.Context, repo, branch string, force bool) error {
	ctx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	flag := "-d"
	if force {
		flag = "-D"
	}
	if _, err := m.git.Run(ctx, repo, "branch", flag, branch); err != nil {
		return &Error{
			Reason:  ReasonGitError,
			Message: fmt.Sprintf("git branch %s %s failed", flag, branch), Err: err,
		}
	}
	return nil
}

// branchUpstream reads the branch's configured remote and ref. Both halves must
// be present: a `branch.{name}.remote` with no `branch.{name}.merge` names no
// ref, and guessing the remote name from the local one is exactly the kind of
// inference that deletes the wrong ref on somebody else's forge.
//
// Two callers now: archive's remote leg, which it was written for, and the
// base-branch fetch in fetch.go, which asks the same question of the *base*
// branch and needs the same refusal to guess (task 056).
func (m *Manager) branchUpstream(ctx context.Context, repo, branch string) (upstream, bool) {
	remote, ok := m.gitConfig(ctx, repo, "branch."+branch+".remote")
	if !ok {
		return upstream{}, false
	}
	ref, ok := m.gitConfig(ctx, repo, "branch."+branch+".merge")
	if !ok {
		return upstream{}, false
	}
	return upstream{remote: remote, ref: ref}, true
}

// gitConfig reads one config key; a missing key is exit 1, not a failure.
func (m *Manager) gitConfig(ctx context.Context, repo, key string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	out, err := m.git.Run(ctx, repo, "config", "--get", key)
	if err != nil || out == "" {
		return "", false
	}
	return out, true
}

// pushDeleteBranch deletes the upstream counterpart. The full ref is pushed as
// git recorded it, so nothing here has to reconstruct the remote's naming.
func (m *Manager) pushDeleteBranch(ctx context.Context, repo string, up upstream) error {
	ctx, cancel := context.WithTimeout(ctx, gitx.RemoteTimeout)
	defer cancel()
	if _, err := m.git.Run(ctx, repo, "push", "--delete", up.remote, up.ref); err != nil {
		return &Error{
			Reason:  ReasonGitError,
			Message: fmt.Sprintf("git push --delete %s %s failed", up.remote, up.ref), Err: err,
		}
	}
	return nil
}
