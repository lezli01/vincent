package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ghFields is the `--json` field set both `gh issue list` and `gh issue view`
// are asked for. One list, so the two calls cannot drift into producing
// different Issues for the same issue.
const ghFields = "number,title,body,url,state,labels,author,assignees,milestone,createdAt,updatedAt"

// ghIssue is `gh --json`'s shape. It is deliberately its own type rather than
// json tags on Issue: `gh` and the REST API disagree about almost every name
// (`url` vs `html_url`, `author` vs `user`, `createdAt` vs `created_at`), and
// a single struct carrying both spellings would silently accept a half-parsed
// answer from either.
type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	Milestone *struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"milestone"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (g ghIssue) normalize(repo Repo, now time.Time) Issue {
	issue := Issue{
		Repo:      repo.String(),
		Number:    g.Number,
		Title:     g.Title,
		Body:      g.Body,
		URL:       g.URL,
		State:     normalizeState(g.State),
		Author:    g.Author.Login,
		CreatedAt: g.CreatedAt,
		UpdatedAt: g.UpdatedAt,
		FetchedAt: now,
	}
	for _, l := range g.Labels {
		if l.Name != "" {
			issue.Labels = append(issue.Labels, l.Name)
		}
	}
	// The first assignee only. §8.1.2 field values are single strings, and a
	// joined list under a field named `assignee` would read as one login.
	if len(g.Assignees) > 0 {
		issue.Assignee = g.Assignees[0].Login
	}
	if g.Milestone != nil {
		issue.Milestone, issue.MilestoneNumber = g.Milestone.Title, g.Milestone.Number
	}
	return issue
}

// ghAuthenticated reports whether `gh` can act for the user. `gh auth status`
// exits non-zero when no host is logged in, which is the whole question —
// its output is not parsed, because that text is not a contract.
func (c *Client) ghAuthenticated(ctx context.Context, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	_, err := c.runGH(ctx, path, "auth", "status")
	return err == nil
}

func (c *Client) ghList(ctx context.Context, cred credential, repo Repo, opts ListOptions) ([]Issue, error) {
	out, err := c.runGH(ctx, cred.ghPath,
		"issue", "list",
		"--repo", repo.String(),
		"--state", opts.state(),
		"--limit", strconv.Itoa(opts.limit()),
		"--json", ghFields)
	if err != nil {
		return nil, err
	}
	return parseGHList(out, repo, c.now())
}

// parseGHList is the `gh issue list --json` half of the leg, split from the
// exec so the table tests can drive it straight from captured output.
func parseGHList(out []byte, repo Repo, now time.Time) ([]Issue, error) {
	var raw []ghIssue
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, newError(ReasonBadResponse, "decode gh issue list: %v", err)
	}
	issues := make([]Issue, 0, len(raw))
	for _, g := range raw {
		issues = append(issues, g.normalize(repo, now))
	}
	return issues, nil
}

func (c *Client) ghGet(ctx context.Context, cred credential, repo Repo, number int) (Issue, error) {
	out, err := c.runGH(ctx, cred.ghPath,
		"issue", "view", strconv.Itoa(number),
		"--repo", repo.String(),
		"--json", ghFields)
	if err != nil {
		return Issue{}, err
	}
	return parseGHIssue(out, repo, c.now())
}

func parseGHIssue(out []byte, repo Repo, now time.Time) (Issue, error) {
	var raw ghIssue
	if err := json.Unmarshal(out, &raw); err != nil {
		return Issue{}, newError(ReasonBadResponse, "decode gh issue view: %v", err)
	}
	if raw.Number == 0 {
		return Issue{}, newError(ReasonBadResponse, "gh issue view returned no issue number")
	}
	return raw.normalize(repo, now), nil
}

// runGH executes gh and returns stdout. Every failure becomes an *Error
// carrying a named reason plus the stderr as *detail* — which reaches the
// daemon log and nothing else (decision 1).
func (c *Client) runGH(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	hideConsole(cmd)
	// GH_PAGER and NO_COLOR are not set here: `--json` output is neither
	// paged nor colored by gh, and inheriting the user's environment is the
	// point (§2).
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, ghError(err, stderr.String(), ctx.Err())
	}
	return stdout.Bytes(), nil
}

// ghError maps a failed `gh` invocation onto the reason vocabulary. The
// mapping reads stderr because that is the only channel gh reports a cause
// on — its exit code is 1 for everything — but no part of that text escapes
// this package's Detail field.
func ghError(runErr error, stderr string, ctxErr error) *Error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = runErr.Error()
	}
	if ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return &Error{Reason: ReasonTimeout, Detail: detail}
		}
		return &Error{Reason: ReasonUnreachable, Detail: detail}
	}
	var exit *exec.ExitError
	if !errors.As(runErr, &exit) {
		// gh could not be executed at all — deleted between the probe and
		// the call, or not executable.
		return &Error{Reason: ReasonNoCredential, Detail: detail}
	}
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "rate limit"):
		return &Error{Reason: ReasonRateLimited, Detail: detail}
	case strings.Contains(lower, "bad credentials"),
		strings.Contains(lower, "http 401"),
		strings.Contains(lower, "authentication token"):
		return &Error{Reason: ReasonUnauthorized, Detail: detail}
	case strings.Contains(lower, "not logged"), strings.Contains(lower, "gh auth login"):
		return &Error{Reason: ReasonNoCredential, Detail: detail}
	case strings.Contains(lower, "http 403"), strings.Contains(lower, "must have admin"):
		return &Error{Reason: ReasonForbidden, Detail: detail}
	case strings.Contains(lower, "could not resolve to"),
		strings.Contains(lower, "not found"),
		strings.Contains(lower, "http 404"):
		return &Error{Reason: ReasonNotFound, Detail: detail}
	default:
		return &Error{Reason: ReasonUnreachable, Detail: fmt.Sprintf("exit %d: %s", exit.ExitCode(), detail)}
	}
}

// ghVersion returns `gh --version`'s first line, or "" when it cannot be
// asked. It is reported verbatim for the same reason gitx reports git's:
// nothing in vincent branches on a `gh` version, and a parsed one would be a
// number somebody later gates on.
func (c *Client) ghVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	out, err := c.runGH(ctx, path, "--version")
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}
