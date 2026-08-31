package apiclient

import (
	"context"
	"net/http"
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

// GitHubPullRequest is one row of GET /v1/projects/{id}/github/pulls, in the
// daemon's normalized shape.
//
// Nothing here is stored on the task. What a task keeps is a
// GitHubPullLink — repo and number — and everything below is re-read every
// time, because draft, state and merged status are live by nature (task 052).
type GitHubPullRequest struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	// State is open or closed. A merged pull request is closed and carries
	// Merged: `gh` spells that third state MERGED and the REST API does not
	// spell it at all, so the daemon folds it onto a bool.
	State      string `json:"state"`
	Draft      bool   `json:"draft"`
	Merged     bool   `json:"merged"`
	HeadBranch string `json:"head_branch,omitempty"`
	// HeadRepo is `owner/name` of the repository the head lives in. A value
	// different from Repo is a fork: its branch can be fetched and run, and
	// nothing can push back to it (task 064 decision 5).
	HeadRepo string `json:"head_repo,omitempty"`
	// HeadSHA is the commit the head branch points at — the commit the check
	// rollup is about (task 068).
	HeadSHA    string    `json:"head_sha,omitempty"`
	BaseBranch string    `json:"base_branch,omitempty"`
	Author     string    `json:"author,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitzero"`
	UpdatedAt  time.Time `json:"updated_at,omitzero"`
	FetchedAt  time.Time `json:"fetched_at,omitzero"`
	// TaskID is the task this pull request is linked to, nil when none is;
	// LinkSource is `auto` or `human`, so a client can say which kind of
	// claim it is showing.
	TaskID     *int64 `json:"task_id,omitempty"`
	LinkSource string `json:"link_source,omitempty"`
	// Prefill is what creating a task from this pull request would fill in,
	// computed by the daemon (task 064). Present only when the listing named
	// a workflow; the form previews it in editable rows and makes no GitHub
	// call of its own.
	Prefill *GitHubPrefill `json:"prefill,omitempty"`
}

// Fork reports that the head branch lives in another repository. An empty
// HeadRepo reads as the same repository, which is what every leg reported
// before the field existed.
func (p GitHubPullRequest) Fork() bool {
	return p.HeadRepo != "" && p.Repo != "" && !strings.EqualFold(p.HeadRepo, p.Repo)
}

// Status is the one word a row renders: merged beats closed, draft beats
// open, which is the order a human reads them in.
func (p GitHubPullRequest) Status() string {
	switch {
	case p.Merged:
		return "merged"
	case p.State == "closed":
		return "closed"
	case p.Draft:
		return "draft"
	default:
		return "open"
	}
}

// GitHubPullLink is a task's stored pull-request link — a **pointer**, never
// a snapshot. Suppressed is a human's sticky unlink, which the daemon's
// reconciler reads so the next tick does not re-apply what a person removed.
type GitHubPullLink struct {
	Repo       string    `json:"repo"`
	Number     int       `json:"number"`
	Source     string    `json:"source"`
	Suppressed bool      `json:"suppressed,omitempty"`
	LinkedAt   time.Time `json:"linked_at,omitzero"`
	// Branch records that the task's branch **is** this pull request's head
	// branch, because the task was created from it (task 064). It is why
	// archive leaves that branch alone and why a retry refuses
	// `branch_override`. Fork additionally says the head lives in another
	// repository, so nothing can be pushed back.
	Branch bool `json:"branch,omitempty"`
	Fork   bool `json:"fork,omitempty"`
}

// GitHubTaskPull is GET /v1/tasks/{id}/github/pull: what this task knows
// about its pull request right now.
//
// Pull is fetched live on every call, which is what lets a task still name a
// pull request that has since merged and dropped off the open listing. Reason
// carries the daemon's named vocabulary when the fetch could not be made, so
// a workspace still renders when GitHub is unreachable.
type GitHubTaskPull struct {
	Linked     bool               `json:"linked"`
	Repo       string             `json:"repo,omitempty"`
	Number     int                `json:"number,omitempty"`
	Source     string             `json:"source,omitempty"`
	Suppressed bool               `json:"suppressed,omitempty"`
	Pull       *GitHubPullRequest `json:"pull,omitempty"`
	Reason     string             `json:"reason,omitempty"`
	// CompareURL is GitHub's own "open a pull request" page for this task's
	// branch, prefilled from the task. The daemon *builds* it — no request is
	// made to GitHub to produce it, and vincent still writes nothing there.
	// Present only when nothing is linked.
	CompareURL string `json:"compare_url,omitempty"`
}

// GitHubPullsOptions narrow a pull-request listing. The zero value asks for
// the most recent **open** pull requests with no prefill — 052's default,
// unchanged: closed and merged are now selectable, not listed by default
// (task 064 decision 9).
type GitHubPullsOptions struct {
	// State is open (default), closed or all.
	State string
	// Limit caps the rows; <= 0 lets the daemon choose.
	Limit int
	// Workflow opts into the per-row prefill.
	Workflow string
}

// ListGitHubPulls lists a project's pull requests, newest first.
func (c *Client) ListGitHubPulls(
	ctx context.Context, projectID int64, opts GitHubPullsOptions,
) ([]GitHubPullRequest, error) {
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
	path := "/v1/projects/" + strconv.FormatInt(projectID, 10) + "/github/pulls"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out []GitHubPullRequest
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TaskGitHubPull fetches a task's linked pull request, live.
func (c *Client) TaskGitHubPull(ctx context.Context, taskID int64) (GitHubTaskPull, error) {
	var out GitHubTaskPull
	if err := c.get(ctx, taskPullPath(taskID), &out); err != nil {
		return GitHubTaskPull{}, err
	}
	return out, nil
}

// LinkGitHubPull names a task's pull request by hand, for the cases the
// head-branch rule misses or gets wrong. It clears any earlier unlink.
func (c *Client) LinkGitHubPull(ctx context.Context, taskID int64, number int) (Task, error) {
	var out Task
	body := map[string]int{"number": number}
	if err := c.send(ctx, http.MethodPost, taskPullPath(taskID), body, &out); err != nil {
		return Task{}, err
	}
	return out, nil
}

// UnlinkGitHubPull removes a task's link and records the refusal, so the
// daemon's reconciler does not re-apply it on the next tick.
func (c *Client) UnlinkGitHubPull(ctx context.Context, taskID int64) (Task, error) {
	var out Task
	if err := c.send(ctx, http.MethodDelete, taskPullPath(taskID), nil, &out); err != nil {
		return Task{}, err
	}
	return out, nil
}

func taskPullPath(taskID int64) string {
	return "/v1/tasks/" + strconv.FormatInt(taskID, 10) + "/github/pull"
}

// GitHubCheckRun is one row of GET /v1/tasks/{id}/github/pull/checks, in the
// daemon's normalized shape (task 068). GitHub's check runs and its older
// commit statuses arrive here as the same thing, because a human reading "did
// the build pass" is not asking which API answered.
type GitHubCheckRun struct {
	Name string `json:"name"`
	// State is one word: queued, in_progress, success, failure, cancelled,
	// skipped, neutral, timed_out, action_required or stale.
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
	// RunID is the GitHub Actions workflow run behind this check, and 0 when
	// the row is not Actions-backed — a third-party check run or a legacy
	// commit status. Re-run is offered only where it is set (task 068
	// decision 3): a key that is offered and then refuses is the thing the
	// reason vocabulary exists to avoid.
	RunID       int64     `json:"run_id,omitempty"`
	StartedAt   time.Time `json:"started_at,omitzero"`
	CompletedAt time.Time `json:"completed_at,omitzero"`
}

// Actions reports that re-run has an honest meaning for this row.
func (c GitHubCheckRun) Actions() bool { return c.RunID > 0 }

// Running reports that the check has not concluded yet.
func (c GitHubCheckRun) Running() bool { return c.State == "queued" || c.State == "in_progress" }

// Failed reports a conclusion a human would call a failure.
func (c GitHubCheckRun) Failed() bool {
	switch c.State {
	case "failure", "timed_out", "action_required", "cancelled":
		return true
	default:
		return false
	}
}

// GitHubTaskChecks is GET /v1/tasks/{id}/github/pull/checks: the live check
// rollup for the linked pull request's head commit.
//
// Like the pull-request row it answers 200 even when it has nothing to show,
// carrying the daemon's named Reason instead: a tab that refuses to render
// because GitHub is unreachable is worse than one that says so.
type GitHubTaskChecks struct {
	Linked bool   `json:"linked"`
	Repo   string `json:"repo,omitempty"`
	Number int    `json:"number,omitempty"`
	// Ref is the head commit the rows belong to. It is reported because the
	// rollup is only meaningful against it.
	Ref  string           `json:"ref,omitempty"`
	Runs []GitHubCheckRun `json:"runs,omitempty"`
	// State is the one word for the whole commit: failure if anything failed,
	// in_progress while anything is still running, success when everything
	// that concluded passed, empty when there are no checks at all.
	State     string    `json:"state,omitempty"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	// Reason is the daemon's named reason when nothing could be fetched.
	Reason string `json:"reason,omitempty"`
}

// TaskGitHubChecks fetches the live check rollup for a task's pull request.
func (c *Client) TaskGitHubChecks(ctx context.Context, taskID int64) (GitHubTaskChecks, error) {
	var out GitHubTaskChecks
	path := "/v1/tasks/" + strconv.FormatInt(taskID, 10) + "/github/pull/checks"
	if err := c.get(ctx, path, &out); err != nil {
		return GitHubTaskChecks{}, err
	}
	return out, nil
}
