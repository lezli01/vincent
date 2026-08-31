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
	// ReasonForbidden: authenticated, but not permitted to read this
	// repository's issues (HTTP 403 that is not a rate limit).
	ReasonForbidden = "forbidden"
	// ReasonNotFound: no such repository or issue (HTTP 404). A private
	// repository an unauthorized token cannot see also answers 404; GitHub
	// does not distinguish, and neither does this.
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
	// A base branch that does not exist and an empty title both land here.
	ReasonBadRequest = "bad_request"
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
	ReasonForbidden:   "the credential may not do this in this repository",
	ReasonNotFound:    "GitHub has no such repository or issue",
	ReasonRateLimited: "the GitHub API rate limit is spent",
	ReasonTimeout:     "GitHub did not answer in time",
	ReasonUnreachable: "GitHub could not be reached",
	ReasonBadResponse: "GitHub returned a response vincent could not read",
	ReasonPullExists:  "a pull request for this branch already exists",
	ReasonBadRequest:  "GitHub refused these values for a pull request",
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
// still a failure to reach GitHub, and inventing a tenth reason for it would
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
