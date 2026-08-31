package worktree

import (
	"context"
	"strings"

	"github.com/lezli01/vincent/internal/gitx"
)

// The branch push (§10, task 069 decision 5).
//
// This is the second thing vincent has ever pushed, and the first it pushes
// on a human's say-so rather than on archive's. It exists because a compare
// URL for a branch that was never pushed leads to a dead page, and the fix
// was a manual `git push` in the worktree — the one step vincent should own.
//
// **It never forces.** `git push -u origin <branch>` and nothing else: no
// `--force`, no `--force-with-lease`, no `+refs/...` refspec. A diverged,
// protected or rejected push changes nothing on the remote and answers with
// a named reason, for the reason task 064 decision 4 gives for
// pull_branch_diverged — the local branch may hold commits nobody pushed, and
// discarding them silently is dishonest. The absence of `--force` in the argv
// is asserted by a test rather than left to review.

// Reason values the push can fail with. Same snake_case vocabulary as every
// other Reason in this package (§18), because a `block_reason` means the same
// thing wherever it originated.
const (
	// ReasonPushRejected: the remote refused the update. A non-fast-forward,
	// a protected branch, a pre-receive hook — git reports all three by
	// rejecting the ref, and vincent does not force past any of them.
	ReasonPushRejected = "push_rejected"
	// ReasonPushNoCredential: git could not authenticate. GIT_TERMINAL_PROMPT
	// is 0 on this call, so a credential helper that wants a terminal fails
	// here rather than parking a request handler on a prompt nobody can
	// answer.
	ReasonPushNoCredential = "push_no_credential" //nolint:gosec // G101: a block reason, not a credential
	// ReasonPushFailed: the remote was unreachable, refused the connection,
	// or did not answer inside gitx.RemoteTimeout. Everything the two above
	// do not name lands here.
	ReasonPushFailed = "push_failed"
)

// DefaultPushRemote is the remote a human-initiated push targets. It is
// `origin` and only `origin`, because that is the remote §13.2's GitHub
// identity is derived from (task 035 decision 5): pushing to one remote and
// opening a pull request against another repository's identity would be two
// answers to one question.
const DefaultPushRemote = "origin"

// PushOutcome is what the push did. Remote and Branch are what was pushed;
// SetUpstream records that the branch had no `branch.{name}.remote` before
// this call and now has one.
type PushOutcome struct {
	Remote      string
	Branch      string
	SetUpstream bool
}

// PushBranch pushes branch to `origin`, setting its upstream, and never
// forces (task 069 decision 5).
//
// `-u` is passed unconditionally rather than only when there is no upstream:
// setting a branch's upstream to the ref it was just pushed to is idempotent,
// and the alternative — read the config, branch on it, push twice as two
// different commands — is two code paths for one push and a race between the
// read and the write.
//
// Nothing here inspects the worktree. Uncommitted work is not in the push and
// therefore not in the pull request; that is stated to the human in the popup
// before they confirm rather than guessed at here (decision 4).
func (m *Manager) PushBranch(ctx context.Context, repo, branch string) (PushOutcome, error) {
	out := PushOutcome{Remote: DefaultPushRemote, Branch: branch}
	if strings.TrimSpace(branch) == "" {
		return out, &Error{Reason: ReasonBranchNameInvalid, Message: "no branch to push"}
	}
	_, out.SetUpstream = m.branchUpstream(ctx, repo, branch)
	out.SetUpstream = !out.SetUpstream

	pushCtx, cancel := context.WithTimeout(ctx, gitx.RemoteTimeout)
	defer cancel()
	// pushArgs is the whole argv, built in one place so the "no --force"
	// assertion has one thing to read.
	if _, err := m.git.RunEnv(pushCtx, repo, pushEnv(), pushArgs(DefaultPushRemote, branch)...); err != nil {
		return out, &Error{Reason: pushReason(err), Message: "git push failed", Err: err}
	}
	return out, nil
}

// pushArgs is the argv of a human-initiated branch push. It is a function
// rather than three literals at the call site so a test can assert on the
// whole of it — specifically that no element of it is a force flag.
func pushArgs(remote, branch string) []string {
	return []string{"push", "--set-upstream", remote, branch}
}

// pushEnv is the environment layered over the daemon's for a push.
//
// GIT_TERMINAL_PROMPT=0 turns "git wants a username" from a hung request
// handler into an exit code this package can name. The daemon has no
// terminal to prompt on and the caller is an HTTP request, so a prompt is
// never answerable — failing is the only honest outcome.
func pushEnv() []string { return []string{"GIT_TERMINAL_PROMPT=0"} }

// pushReason maps git's own rejection onto the vocabulary above. It reads
// stderr because a push reports its cause nowhere else — git's exit code is 1
// for a rejected ref, an unreachable host and a bad credential alike — and no
// part of that text reaches a client: it rides in the *Error's Err, which the
// daemon logs.
func pushReason(err error) string {
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "rejected"),
		strings.Contains(lower, "non-fast-forward"),
		strings.Contains(lower, "fetch first"),
		strings.Contains(lower, "protected branch"),
		strings.Contains(lower, "pre-receive hook declined"):
		return ReasonPushRejected
	case strings.Contains(lower, "authentication failed"),
		strings.Contains(lower, "could not read username"),
		strings.Contains(lower, "could not read password"),
		strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "terminal prompts disabled"),
		strings.Contains(lower, "invalid username or token"):
		return ReasonPushNoCredential
	default:
		return ReasonPushFailed
	}
}
