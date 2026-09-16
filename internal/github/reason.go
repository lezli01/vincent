package github

import (
	"errors"
	"fmt"
)

// Unavailability reasons (task 035 decision 1). They are the *whole* client-
// facing vocabulary for "the integration did not answer": both legs map their
// own failures onto these, so a client never learns whether `gh` or an HTTP
// call produced the trouble, and no CLI stderr or HTTP body reaches it.
//
// They are snake_case for the same reason internal/worktree's block reasons
// are: one vocabulary, spelled the same wherever it surfaces.
const (
	// ReasonDisabled: `github.enabled` is false in config.yaml (§12.3).
	ReasonDisabled = "disabled"
	// ReasonNotGitHub: the project's `origin` is missing, or does not parse
	// as a github.com repository (decision 5).
	ReasonNotGitHub = "not_github"
	// ReasonNoCredential: neither `gh` nor a GITHUB_TOKEN/GH_TOKEN in the
	// daemon's environment can authenticate. This is the row `vincent doctor`
	// exists to explain.
	ReasonNoCredential = "no_credential" //nolint:gosec // G101: a block reason, not a credential
	// ReasonUnauthorized: the credential was rejected (HTTP 401, `gh` reports
	// a bad token).
	ReasonUnauthorized = "unauthorized"
	// ReasonForbidden: authenticated, but not permitted to *read* what was
	// asked for — this repository's issues, pull requests or checks (an HTTP
	// 403 on a read that is not a rate limit). The same 403 on a write is
	// ReasonNoWriteScope: one condition, one spelling (task 068).
	ReasonForbidden = "forbidden"
	// ReasonNotFound: no such repository, issue, pull request or check run
	// (HTTP 404). A private repository an unauthorized token cannot see also
	// answers 404, and so does a write the credential is not permitted to
	// make when GitHub declines to reveal the repository exists. GitHub does
	// not distinguish, and neither does this: no 404 is reinterpreted as a
	// missing write scope.
	ReasonNotFound = "not_found"
	// ReasonRateLimited: the API's rate limit is spent (HTTP 429, or a 403
	// carrying an exhausted rate-limit header).
	ReasonRateLimited = "rate_limited"
	// ReasonTimeout: the call did not finish inside RemoteTimeout.
	ReasonTimeout = "timeout"
	// ReasonUnreachable: the network call failed, or the API answered a
	// status with no more specific meaning.
	ReasonUnreachable = "unreachable"
	// ReasonBadResponse: the answer arrived and did not parse. It is its own
	// reason rather than folded into unreachable because it accuses the far
	// side of speaking a shape this package does not know, which is a bug
	// report rather than a network condition.
	ReasonBadResponse = "bad_response"
	// ReasonPullExists: a pull request already exists for this head and base
	// (task 069). It is the write path's one *expected* refusal and GitHub's
	// own backstop against a double submission, so it is named rather than
	// folded into unreachable: "there is already one" is an answer a human
	// can act on, and "GitHub could not be reached" is not.
	ReasonPullExists = "pull_exists"
	// ReasonBadRequest: GitHub refused the values as unusable (HTTP 422 that
	// is not a duplicate head), or this package refused them before sending.
	// A base branch that does not exist, an empty title, an empty comment and
	// a re-run of a run that is not a failed Actions run on the pull request's
	// head all land here.
	ReasonBadRequest = "bad_request"

	// The pull-request write reasons (task 068.4). Each is a refusal a human
	// can act on, which is why none of them folds into unreachable — and each
	// is decided from a preflight read wherever one can be made, so GitHub's
	// own refusal sentence is only ever a fallback and only ever Detail.

	// ReasonNoWriteScope: the credential may read this repository and may not
	// write to it — an HTTP 403 on any write that is not a rate limit,
	// CreatePull included.
	ReasonNoWriteScope = "no_write_scope"
	// ReasonNotMergeable: GitHub will not merge this pull request as it
	// stands — it is closed or already merged, a draft, conflicted, or
	// blocked by something other than a running check.
	ReasonNotMergeable = "not_mergeable"
	// ReasonChecksRunning: the merge is blocked and a check on the head
	// commit has not concluded yet. Waiting is the fix.
	ReasonChecksRunning = "checks_running"
	// ReasonBranchBehind: the base branch has moved and the repository
	// requires the head to be up to date before merging.
	ReasonBranchBehind = "branch_behind"
	// ReasonHeadChanged: the pull request's head is no longer the commit the
	// human confirmed. Nothing was merged — merging it would be a green build
	// for code nobody ran (task 068 decision 6).
	ReasonHeadChanged = "head_changed"
)

// reasonMessages are the one-line explanations clients render. Keeping them
// here rather than at each call site is what makes "no leg leaks its own
// error text" enforceable: the API has nothing else to print.
var reasonMessages = map[string]string{ //nolint:gosec // G101: these are the reasons' own explanations, not credentials
	ReasonDisabled:     "the GitHub integration is disabled in config.yaml",
	ReasonNotGitHub:    "this project's origin remote is not a github.com repository",
	ReasonNoCredential: "no GitHub credential: gh is not installed or not authenticated, and neither GITHUB_TOKEN nor GH_TOKEN is set",
	ReasonUnauthorized: "GitHub rejected the credential",
	// Not "this repository's issues": the same 403 now answers a pull-request
	// creation, and a message naming issues would be wrong on the write path
	// (task 069 decision 2).
	ReasonForbidden: "the credential may not do this in this repository",
	// Not "repository or issue": the same 404 answers a pull request and a
	// check run, and a write the credential may not make (task 068.4).
	ReasonNotFound:      "GitHub has no such repository, issue, pull request or check run, or the credential may not see it",
	ReasonRateLimited:   "the GitHub API rate limit is spent",
	ReasonTimeout:       "GitHub did not answer in time",
	ReasonUnreachable:   "GitHub could not be reached",
	ReasonBadResponse:   "GitHub returned a response vincent could not read",
	ReasonPullExists:    "a pull request for this branch already exists",
	ReasonBadRequest:    "GitHub refused these values for this request",
	ReasonNoWriteScope:  "the credential may read this repository but not write to it",
	ReasonNotMergeable:  "GitHub will not merge this pull request as it stands",
	ReasonChecksRunning: "the merge is blocked until the running checks finish",
	ReasonBranchBehind:  "the pull request's branch is behind its base and must be updated first",
	ReasonHeadChanged:   "the pull request's head moved since it was confirmed, so nothing was merged",
}

// Message renders a reason for a human. An unknown reason renders as itself
// rather than as an empty string: a missing map entry must not turn into a
// blank error.
func Message(reason string) string {
	if m, ok := reasonMessages[reason]; ok {
		return m
	}
	return reason
}

// Error is every failure this package reports. Reason is the client-facing
// vocabulary above; Detail is for the daemon log only and is never rendered
// into an API response (decision 1).
type Error struct {
	Reason string
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return Message(e.Reason)
	}
	return fmt.Sprintf("%s: %s", Message(e.Reason), e.Detail)
}

// ReasonOf extracts the reason from an error this package produced, falling
// back to ReasonUnreachable for anything else — an unclassified failure is
// still a failure to reach GitHub, and inventing another reason for it would
// give clients a value they cannot act on.
func ReasonOf(err error) string {
	var e *Error
	if err == nil {
		return ""
	}
	if errors.As(err, &e) {
		return e.Reason
	}
	return ReasonUnreachable
}

func newError(reason, format string, args ...any) *Error {
	return &Error{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}
