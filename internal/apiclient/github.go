package apiclient

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The GitHub issue endpoints (§13.2, task 035). Clients never talk to GitHub:
// these are two ordinary calls to the daemon, which owns every GitHub call it
// makes on their behalf.

// GitHubStatus is GET /v1/projects/{id}/github — the capability probe.
//
// Three answers, and they are different: the integration is switched off, the
// project is not a GitHub repository, or it is and cannot be reached right
// now. A client that collapses them tells a user to check their token when
// the truth is that their remote is a GitLab URL.
type GitHubStatus struct {
	Enabled   bool   `json:"enabled"`
	Repo      string `json:"repo,omitempty"`
	Available bool   `json:"available"`
	// Reason is the daemon's named reason, stable enough to branch on;
	// Message is the sentence to show. Neither carries `gh` or HTTP error
	// text.
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// Via is `gh` or `token` when available.
	Via string `json:"via,omitempty"`
}

// Unavailable renders why the picker is not on offer, and "" when it is.
func (g GitHubStatus) Unavailable() string {
	if g.Available {
		return ""
	}
	if g.Message != "" {
		return g.Message
	}
	return "GitHub is not available for this project"
}

// GitHubIssue is one row of GET /v1/projects/{id}/github/issues, in the
// daemon's normalized shape — the same shape it snapshots onto a task, so a
// picker row and a created task's `github_issue` read identically.
type GitHubIssue struct {
	Repo            string    `json:"repo"`
	Number          int       `json:"number"`
	Title           string    `json:"title"`
	Body            string    `json:"body,omitempty"`
	URL             string    `json:"url"`
	State           string    `json:"state"`
	Labels          []string  `json:"labels,omitempty"`
	Author          string    `json:"author,omitempty"`
	Assignee        string    `json:"assignee,omitempty"`
	Milestone       string    `json:"milestone,omitempty"`
	MilestoneNumber int       `json:"milestone_number,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitzero"`
	UpdatedAt       time.Time `json:"updated_at,omitzero"`
	// FetchedAt is when the daemon captured this. On a task's stored issue it
	// is the honest answer to "how old is this?" — the snapshot is never
	// refreshed.
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	// Prefill is what creating a task from this issue would fill in. It is
	// present only when the listing named a workflow, because the
	// declared-field half of a prefill is a fact about a workflow.
	Prefill *GitHubPrefill `json:"prefill,omitempty"`
}

// LabelList is the comma-joined spelling, for a one-line summary.
func (i GitHubIssue) LabelList() string { return strings.Join(i.Labels, ", ") }

// GitHubPrefill is the daemon's computed prefill for one issue. The form
// drops it into editable rows: every guess is visible before creation, and
// none of it is locked.
type GitHubPrefill struct {
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Fields      map[string]string `json:"fields,omitempty"`
}

// ProjectGitHub fetches the capability probe for one project.
func (c *Client) ProjectGitHub(ctx context.Context, projectID int64) (GitHubStatus, error) {
	var out GitHubStatus
	path := "/v1/projects/" + strconv.FormatInt(projectID, 10) + "/github"
	if err := c.get(ctx, path, &out); err != nil {
		return GitHubStatus{}, err
	}
	return out, nil
}

// GitHubIssuesOptions narrow a listing. The zero value asks for the most
// recent open issues with no prefill.
type GitHubIssuesOptions struct {
	// State is open (default), closed or all.
	State string
	// Limit caps the rows; <= 0 lets the daemon choose.
	Limit int
	// Workflow opts into the per-row prefill.
	Workflow string
}

// ListGitHubIssues lists a project's issues, newest first. An unavailable
// integration comes back as *Error with the daemon's reason in Details.
func (c *Client) ListGitHubIssues(
	ctx context.Context, projectID int64, opts GitHubIssuesOptions,
) ([]GitHubIssue, error) {
	q := url.Values{}
	if opts.State != "" {
		q.Set("state", opts.State)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Workflow != "" {
		q.Set("workflow", opts.Workflow)
	}
	path := "/v1/projects/" + strconv.FormatInt(projectID, 10) + "/github/issues"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out []GitHubIssue
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}
