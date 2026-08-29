package worktree

import (
	"context"
	"regexp"

	"github.com/lezli01/vincent/internal/gitx"
)

// Outcomes of the base-branch fetch that precedes a worktree creation (§10,
// task 056). Same snake_case vocabulary as the Reason constants, and results
// rather than failures for the same reason task 008's Branch* constants are:
// three of the four describe a task that was created exactly as asked. A
// fetch never blocks a task and never becomes a block_reason — a repository
// with no remote is a supported local-first configuration, and a fan_out
// child's base is its parent's branch, which has no remote counterpart and
// never will (§7.6).
const (
	// FetchDisabled: `fetch_base_branch` is off, so nothing was attempted and
	// the task branched from the local base — the pre-056 behaviour exactly.
	FetchDisabled = "disabled"
	// FetchNoUpstream: the base branch has no `branch.{base}.remote` +
	// `branch.{base}.merge` pair, so there is no ref to fetch. Normal, not a
	// degradation: a repository with no remote, a branch that only ever
	// existed locally, and every fan_out lane land here.
	FetchNoUpstream = "no_upstream"
	// FetchDone: the upstream ref was fetched and resolved, and the task
	// branch starts at that commit.
	FetchDone = "fetched"
	// FetchFailed: the remote was unreachable, refused the connection, or did
	// not answer inside gitx.RemoteTimeout. The task branched from the local
	// base and was created normally.
	FetchFailed = "error"
)

// FetchOutcome is what the base-branch fetch did. The zero value means create
// was never asked to fetch. It is returned rather than logged: this package
// holds no logger, and taskrun is where a worktree result becomes a log line
// (the shape task 008 set with BranchOutcome).
type FetchOutcome struct {
	// Remote is the resolved remote name (`branch.{base}.remote`), empty when
	// there was no upstream to resolve.
	Remote string
	// Ref is the full ref that was fetched (`branch.{base}.merge`).
	Ref string
	// Result is one of the Fetch* constants.
	Result string
	// Error carries git's own message when Result is FetchFailed.
	Error string
}

// Degraded reports whether the fetch was asked for and did not happen. Only
// FetchFailed is: no upstream is the answer for a whole class of repositories
// vincent supports, and logging it as a problem would cry wolf on every
// fan_out lane.
func (o FetchOutcome) Degraded() bool { return o.Result == FetchFailed }

// fullHex matches a resolved object name. `git rev-parse FETCH_HEAD` prints
// one line, but it is checked rather than trusted: the value becomes the start
// point of `git worktree add` and is persisted as the task's base, so anything
// that is not an object name must fall back to the local branch instead of
// being handed to git.
var fullHex = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// fetchBase refreshes base from its own configured upstream and returns the
// commit the task branch should start at, or "" to use the local base.
//
// The remote is read from `branch.{base}.remote` + `branch.{base}.merge`
// through branchUpstream — task 008's resolver, whose comment already refuses
// to guess a remote name from a local one — rather than assuming `origin`.
// That is not caution for its own sake: it is what makes a local `master`
// tracking `refs/heads/main` fetch the right ref, and what turns "no remote",
// "local-only branch" and "fan_out lane" into one answer instead of three
// special cases.
//
// The start point is the SHA `FETCH_HEAD` resolved to, not `{remote}/{base}`.
// A raw commit does not depend on the remote's refspec configuration, does not
// require a remote-tracking ref to exist, cannot be re-pointed by a later
// fetch, and is the base SHA §5.3 records without a second call. The one
// exposure is a user's own concurrent `git fetch` in the same repository
// rewriting FETCH_HEAD between these two calls; the window is microseconds and
// the outcome is a task based on a different upstream commit, not a corrupt
// repository.
//
// Nothing here mutates the user's local base branch. It is frequently checked
// out — and often dirty — in the human's own working copy, so a fast-forward
// would need its own refusal path; branching from the fetched commit touches
// no shared repository state and cannot fail for that reason.
func (m *Manager) fetchBase(ctx context.Context, repo, base string) (string, FetchOutcome) {
	up, ok := m.branchUpstream(ctx, repo, base)
	if !ok {
		return "", FetchOutcome{Result: FetchNoUpstream}
	}
	out := FetchOutcome{Remote: up.remote, Ref: up.ref, Result: FetchDone}
	fetchCtx, cancel := context.WithTimeout(ctx, gitx.RemoteTimeout)
	defer cancel()
	if _, err := m.git.Run(fetchCtx, repo, "fetch", up.remote, up.ref); err != nil {
		out.Result, out.Error = FetchFailed, err.Error()
		return "", out
	}
	sha, err := m.git.Run(fetchCtx, repo, "rev-parse", "FETCH_HEAD")
	if err != nil {
		out.Result, out.Error = FetchFailed, err.Error()
		return "", out
	}
	if !fullHex.MatchString(sha) {
		out.Result, out.Error = FetchFailed, "FETCH_HEAD did not resolve to a commit"
		return "", out
	}
	return sha, out
}

// baseRev names the revision a task's base is compared against. The recorded
// base SHA wins when the task has one: once a task branch starts at a fetched
// commit, `base_branch` names a moving ref that is no longer where the task
// began, and reading it as the fork point measures the task against whatever
// the local branch happens to be now (§5.3, task 056).
//
// The fallback is the fully-qualified branch, for the same reason it always
// was: an ambiguous short name — a tag sharing the branch's name, a deleted
// base with a surviving remote-tracking ref — must read as *cannot judge*
// rather than resolve to something else and answer confidently. A 40-hex
// object name needs no such qualification and cannot be ambiguous.
func baseRev(base, baseSHA string) string {
	if baseSHA != "" {
		return baseSHA
	}
	return "refs/heads/" + base
}
