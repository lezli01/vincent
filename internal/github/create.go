package github

import (
	"context"
	"strings"
)

// The one write path (task 069, decision record row 27 as amended).
//
// Everything else in this package reads. This creates a pull request, and it
// is the whole of what "internal/github is no longer read-only" means: there
// is no second write method, no update, no comment, no merge, and row 11's
// prohibition on hardcoded merge behaviour is untouched.
//
// Both legs answer into the same normalized PullRequest the read side
// produces, so a client cannot tell which one ran — and neither leg's own
// error text escapes, exactly as decision 1 requires of the read side.

// CreateOptions is what a human filled into the popup, plus the two refs the
// daemon derived. Every field is required except Body: GitHub's own form is
// unusable without a title, and finding that out in an error envelope is a
// round trip wasted.
type CreateOptions struct {
	// Base is the branch the pull request merges into; Head is the branch it
	// merges from. Both are plain branch names — a cross-repository head
	// (`owner:branch`) is not built here, because vincent only ever pushes to
	// the repository its own `origin` names.
	Base string
	Head string

	Title string
	Body  string
	Draft bool
}

// CreatePull opens a pull request and returns it, normalized.
//
// It is a human's act. Nothing on the step path reaches it: an agent step
// already has a full-auto shell in its own worktree and can run `gh pr
// create` there, which is row 11's original path and stays open — and the
// route in front of this is excluded from the MCP tool surface for exactly
// that reason (decision 3).
func (c *Client) CreatePull(ctx context.Context, repo Repo, opts CreateOptions) (PullRequest, error) {
	if strings.TrimSpace(opts.Title) == "" {
		return PullRequest{}, newError(ReasonBadRequest, "a pull request needs a title")
	}
	if strings.TrimSpace(opts.Head) == "" || strings.TrimSpace(opts.Base) == "" {
		return PullRequest{}, newError(ReasonBadRequest, "a pull request needs a head and a base branch")
	}
	cred, err := c.credential(ctx)
	if err != nil {
		return PullRequest{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	var pull PullRequest
	if cred.via == ViaGH {
		pull, err = c.ghCreatePull(ctx, cred, repo, opts)
	} else {
		pull, err = c.restCreatePull(ctx, cred, repo, opts)
	}
	if err != nil {
		c.logf("github pull request create failed", "repo", repo.String(), "head", opts.Head,
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return PullRequest{}, err
	}
	return pull, nil
}
