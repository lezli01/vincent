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

// restPull is the REST API's pull-request shape (see ghPull for why the two
// legs do not share a struct).
type restPull struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
		// Repo is null when the head's fork has been deleted, which is why
		// this is a pointer: an absent repository must normalize to an empty
		// HeadRepo rather than to `"/"`.
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// MergedAt is the only merged signal both the collection and the single
	// resource carry; the single resource's `merged` bool is deliberately not
	// read, so one field decides it on both routes.
	MergedAt *time.Time `json:"merged_at"`
}

func (r restPull) normalize(repo Repo, now time.Time) PullRequest {
	pull := PullRequest{
		Repo:       repo.String(),
		Number:     r.Number,
		Title:      r.Title,
		Body:       r.Body,
		URL:        r.HTMLURL,
		State:      normalizeState(r.State),
		Draft:      r.Draft,
		HeadBranch: r.Head.Ref,
		HeadSHA:    r.Head.SHA,
		BaseBranch: r.Base.Ref,
		Author:     r.User.Login,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
		FetchedAt:  now,
	}
	if r.Head.Repo != nil {
		pull.HeadRepo = r.Head.Repo.FullName
	}
	if r.MergedAt != nil && !r.MergedAt.IsZero() {
		pull.Merged, pull.State = true, StateClosed
	}
	return pull
}

func (c *Client) restListPulls(ctx context.Context, cred credential, repo Repo, opts ListOptions) ([]PullRequest, error) {
	q := url.Values{}
	q.Set("state", opts.state())
	q.Set("per_page", strconv.Itoa(opts.limit()))
	q.Set("sort", "created")
	q.Set("direction", "desc")
	body, err := c.restGET(ctx, cred, fmt.Sprintf("/repos/%s/%s/pulls?%s",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), q.Encode()))
	if err != nil {
		return nil, err
	}
	return parseRESTPullList(body, repo, c.now())
}

func parseRESTPullList(body []byte, repo Repo, now time.Time) ([]PullRequest, error) {
	var raw []restPull
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, newError(ReasonBadResponse, "decode pull request list: %v", err)
	}
	pulls := make([]PullRequest, 0, len(raw))
	for _, r := range raw {
		pulls = append(pulls, r.normalize(repo, now))
	}
	return pulls, nil
}

func (c *Client) restGetPull(ctx context.Context, cred credential, repo Repo, number int) (PullRequest, error) {
	body, err := c.restGET(ctx, cred, fmt.Sprintf("/repos/%s/%s/pulls/%d",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number))
	if err != nil {
		return PullRequest{}, err
	}
	return parseRESTPull(body, repo, c.now())
}

func parseRESTPull(body []byte, repo Repo, now time.Time) (PullRequest, error) {
	var raw restPull
	if err := json.Unmarshal(body, &raw); err != nil {
		return PullRequest{}, newError(ReasonBadResponse, "decode pull request: %v", err)
	}
	if raw.Number == 0 {
		return PullRequest{}, newError(ReasonBadResponse, "pull request response carried no number")
	}
	return raw.normalize(repo, now), nil
}

// restCheckRuns is GET /repos/{o}/{r}/commits/{sha}/check-runs. The REST leg
// needs two calls where `gh` needs one, because check runs and legacy commit
// statuses are separate APIs there and `statusCheckRollup` is a GraphQL
// convenience with no REST equivalent (task 068).
type restCheckRuns struct {
	CheckRuns []struct {
		Name        string     `json:"name"`
		Status      string     `json:"status"`
		Conclusion  string     `json:"conclusion"`
		HTMLURL     string     `json:"html_url"`
		DetailsURL  string     `json:"details_url"`
		StartedAt   *time.Time `json:"started_at"`
		CompletedAt *time.Time `json:"completed_at"`
	} `json:"check_runs"`
}

// restCommitStatus is GET /repos/{o}/{r}/commits/{sha}/status — the legacy
// half, which `gh` folds into the same array and this leg has to fetch
// separately.
type restCommitStatus struct {
	Statuses []struct {
		Context   string    `json:"context"`
		State     string    `json:"state"`
		TargetURL string    `json:"target_url"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"statuses"`
}

func (c *Client) restChecks(ctx context.Context, cred credential, repo Repo, ref string) (CheckRollup, error) {
	// per_page is pinned rather than left to the default 30: a repository
	// with more matrix legs than that would report a partial rollup, and a
	// partial rollup reads exactly like a complete one.
	runsBody, err := c.restGET(ctx, cred, fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(ref)))
	if err != nil {
		return CheckRollup{}, err
	}
	statusBody, err := c.restGET(ctx, cred, fmt.Sprintf("/repos/%s/%s/commits/%s/status?per_page=100",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(ref)))
	if err != nil {
		return CheckRollup{}, err
	}
	return parseRESTChecks(runsBody, statusBody, ref, c.now())
}

// parseRESTChecks is the decoding half of the leg, split from the HTTP calls
// so the table tests can drive it straight from captured API output.
func parseRESTChecks(runsBody, statusBody []byte, ref string, now time.Time) (CheckRollup, error) {
	var rawRuns restCheckRuns
	if err := json.Unmarshal(runsBody, &rawRuns); err != nil {
		return CheckRollup{}, newError(ReasonBadResponse, "decode check runs: %v", err)
	}
	var rawStatus restCommitStatus
	if err := json.Unmarshal(statusBody, &rawStatus); err != nil {
		return CheckRollup{}, newError(ReasonBadResponse, "decode commit status: %v", err)
	}
	runs := make([]CheckRun, 0, len(rawRuns.CheckRuns)+len(rawStatus.Statuses))
	for _, r := range rawRuns.CheckRuns {
		if r.Name == "" {
			continue
		}
		// html_url is the page a human opens; details_url is what the app
		// pointed at. They are the same page for Actions, and only the app's
		// own for a third-party check — so html_url wins for display, and
		// details_url still decides provenance.
		link := r.HTMLURL
		if link == "" {
			link = r.DetailsURL
		}
		run := CheckRun{
			Name:  r.Name,
			State: normalizeCheckState(r.Status, r.Conclusion),
			URL:   link,
			RunID: actionsRunID(link),
		}
		if run.RunID == 0 {
			run.RunID = actionsRunID(r.DetailsURL)
		}
		if r.StartedAt != nil {
			run.StartedAt = *r.StartedAt
		}
		if r.CompletedAt != nil {
			run.CompletedAt = *r.CompletedAt
		}
		runs = append(runs, run)
	}
	for _, st := range rawStatus.Statuses {
		if st.Context == "" {
			continue
		}
		runs = append(runs, CheckRun{
			Name:      st.Context,
			State:     normalizeStatusState(st.State),
			URL:       st.TargetURL,
			StartedAt: st.CreatedAt,
		})
	}
	return newRollup(ref, runs, now), nil
}
