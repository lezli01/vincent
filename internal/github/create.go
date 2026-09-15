package github

import (
	"context"
	"strings"
)

// Pull-request creation (task 069, decision record row 27 as amended).
//
// It was the first write in this package and, until task 068.4, the only one.
// It is no longer alone — write.go holds merge, close, reopen, comment and
// re-run — and it follows the same rules they do: a human's act, reached from
// no MCP tool, and a 403 is `no_write_scope`.
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
