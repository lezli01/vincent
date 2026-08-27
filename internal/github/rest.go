package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public REST API root. It is a field on Options rather
// than a constant used directly so tests can point the leg at an httptest
// server — no test in this repository may reach the real network.
const DefaultBaseURL = "https://api.github.com"

// apiVersion is the REST API version header GitHub asks callers to pin, so a
// future default cannot change this leg's parsing underneath it.
const apiVersion = "2022-11-28"

// maxResponseBytes bounds a response body. A repository with enormous issue
// bodies is a disk-and-memory question the daemon answers here rather than in
// the caller.
const maxResponseBytes = 8 << 20

// restIssue is the REST API's shape (see ghIssue for why the two legs do not
// share a struct).
type restIssue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	Milestone *struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"milestone"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// PullRequest is present exactly when this row is a pull request. The
	// REST `/issues` collection includes PRs and `gh issue list` does not, so
	// filtering on it is what makes the two legs return the same list
	// (decision 1).
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

func (r restIssue) normalize(repo Repo, now time.Time) Issue {
	issue := Issue{
		Repo:      repo.String(),
		Number:    r.Number,
		Title:     r.Title,
		Body:      r.Body,
		URL:       r.HTMLURL,
		State:     normalizeState(r.State),
		Author:    r.User.Login,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
		FetchedAt: now,
	}
	for _, l := range r.Labels {
		if l.Name != "" {
			issue.Labels = append(issue.Labels, l.Name)
		}
	}
	if len(r.Assignees) > 0 {
		issue.Assignee = r.Assignees[0].Login
	}
	if r.Milestone != nil {
		issue.Milestone, issue.MilestoneNumber = r.Milestone.Title, r.Milestone.Number
	}
	return issue
}

func (c *Client) restList(ctx context.Context, cred credential, repo Repo, opts ListOptions) ([]Issue, error) {
	q := url.Values{}
	q.Set("state", opts.state())
	q.Set("per_page", strconv.Itoa(opts.limit()))
	q.Set("sort", "created")
	q.Set("direction", "desc")
	body, err := c.restGET(ctx, cred, fmt.Sprintf("/repos/%s/%s/issues?%s",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), q.Encode()))
	if err != nil {
		return nil, err
	}
	return parseRESTList(body, repo, c.now())
}

// parseRESTList is the decoding half of the leg, split from the HTTP call so
// the table tests can drive it straight from captured API output.
func parseRESTList(body []byte, repo Repo, now time.Time) ([]Issue, error) {
	var raw []restIssue
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, newError(ReasonBadResponse, "decode issue list: %v", err)
	}
	issues := make([]Issue, 0, len(raw))
	for _, r := range raw {
		if r.PullRequest != nil {
			continue
		}
		issues = append(issues, r.normalize(repo, now))
	}
	return issues, nil
}

func (c *Client) restGet(ctx context.Context, cred credential, repo Repo, number int) (Issue, error) {
	body, err := c.restGET(ctx, cred, fmt.Sprintf("/repos/%s/%s/issues/%d",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number))
	if err != nil {
		return Issue{}, err
	}
	return parseRESTIssue(body, repo, number, c.now())
}

func parseRESTIssue(body []byte, repo Repo, number int, now time.Time) (Issue, error) {
	var raw restIssue
	if err := json.Unmarshal(body, &raw); err != nil {
		return Issue{}, newError(ReasonBadResponse, "decode issue: %v", err)
	}
	if raw.PullRequest != nil {
		// A pull request is not an issue here, and saying "not found" is what
		// `gh issue view` says about the same number (decision 10).
		return Issue{}, newError(ReasonNotFound, "#%d is a pull request", number)
	}
	if raw.Number == 0 {
		return Issue{}, newError(ReasonBadResponse, "issue response carried no number")
	}
	return raw.normalize(repo, now), nil
}

func (c *Client) restGET(ctx context.Context, cred credential, path string) ([]byte, error) {
	base := c.opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return nil, newError(ReasonUnreachable, "build request: %v", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "vincent")
	req.Header.Set("Authorization", "Bearer "+cred.token)

	client := c.opts.HTTP
	if client == nil {
		client = &http.Client{Timeout: RemoteTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, newError(ReasonTimeout, "%v", err)
		}
		return nil, newError(ReasonUnreachable, "%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, restError(resp, body)
	}
	if readErr != nil {
		return nil, newError(ReasonUnreachable, "read response: %v", readErr)
	}
	return body, nil
}

// restError maps an HTTP status onto the reason vocabulary. The body is kept
// as detail for the daemon log and never rendered to a client (decision 1).
func restError(resp *http.Response, body []byte) *Error {
	detail := strings.TrimSpace(string(body))
	if len(detail) > 512 {
		detail = detail[:512]
	}
	detail = fmt.Sprintf("http %d: %s", resp.StatusCode, detail)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return &Error{Reason: ReasonUnauthorized, Detail: detail}
	case http.StatusForbidden:
		// GitHub answers 403 both for "you may not read this" and for a spent
		// rate limit; the remaining-quota header is what separates them.
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return &Error{Reason: ReasonRateLimited, Detail: detail}
		}
		return &Error{Reason: ReasonForbidden, Detail: detail}
	case http.StatusTooManyRequests:
		return &Error{Reason: ReasonRateLimited, Detail: detail}
	case http.StatusNotFound:
		return &Error{Reason: ReasonNotFound, Detail: detail}
	default:
		return &Error{Reason: ReasonUnreachable, Detail: detail}
	}
}
