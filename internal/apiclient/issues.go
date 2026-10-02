package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The issue routes (§5.6, §13.2, task 130.3). The wire types are this
// package's own; the server's DTOs stay unexported in internal/api, and the
// live tests keep the two from drifting.

// Issue 409 reasons, as they arrive in Error.Details["reason"].
const (
	// IssueReasonChanged is a PATCH whose version is stale; IssueConflict
	// returns the current issue it carries.
	IssueReasonChanged = "issue_changed"
	// IssueReasonMirrored is a PATCH of an imported issue's title, body or
	// labels, which mirror the remote.
	IssueReasonMirrored = "issue_mirrored"
)

// IssueSource is where an imported issue came from.
type IssueSource struct {
	Provider    string `json:"provider"`
	Repo        string `json:"repo"`
	Number      int    `json:"number,omitempty"`
	URL         string `json:"url,omitempty"`
	RemoteState string `json:"remote_state,omitempty"`
}

// IssueTasks is the root tasks an issue started: how many, and which are
// still unsettled.
type IssueTasks struct {
	Count     int     `json:"count"`
	ActiveIDs []int64 `json:"active_ids"`
}

// Issue is one issue. A row from ListIssues leaves Body, AvailableActions,
// Tasks and Editable zero — a list never carries them; GetIssue and every
// write fill them.
type Issue struct {
	ID              int64        `json:"id"`
	ProjectID       int64        `json:"project_id"`
	Title           string       `json:"title"`
	State           string       `json:"state"`
	CloseReason     string       `json:"close_reason,omitempty"`
	DuplicateOf     *int64       `json:"duplicate_of,omitempty"`
	Kind            string       `json:"kind"`
	Priority        int          `json:"priority"`
	Author          string       `json:"author"`
	CreatedByTaskID *int64       `json:"created_by_task_id,omitempty"`
	Labels          []string     `json:"labels"`
	Source          *IssueSource `json:"source"`
	Active          bool         `json:"active"`
	TaskCount       int          `json:"task_count"`
	Version         int64        `json:"version"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	ClosedAt        *time.Time   `json:"closed_at,omitempty"`

	Body             string     `json:"body,omitempty"`
	AvailableActions []string   `json:"available_actions,omitempty"`
	Tasks            IssueTasks `json:"tasks"`
	Editable         []string   `json:"editable,omitempty"`
	// Prefill is what CreateTaskRequest.IssueID would fill in under the
	// workflow GetIssue named (task 130.7); nil when it named none. The
	// daemon computes it with the function the create runs.
	Prefill *GitHubPrefill `json:"prefill,omitempty"`
}

// IssueListOptions filters ListIssues. Zero values mean "no filter".
type IssueListOptions struct {
	ProjectID int64
	States    []string
	Labels    []string // every one must match
	Kind      string
	Query     string // substring of title or body
	Source    string // "local" or "github"
	Sort      string // "updated" (default) or "created"
	Limit     int
	Offset    int
}

func (o IssueListOptions) query() string {
	v := url.Values{}
	if o.ProjectID != 0 {
		v.Set("project_id", strconv.FormatInt(o.ProjectID, 10))
	}
	for _, s := range o.States {
		v.Add("state", s)
	}
	for _, l := range o.Labels {
		v.Add("label", l)
	}
	for k, s := range map[string]string{"kind": o.Kind, "q": o.Query, "source": o.Source, "sort": o.Sort} {
		if s != "" {
			v.Set(k, s)
		}
	}
	if o.Limit > 0 {
		v.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		v.Set("offset", strconv.Itoa(o.Offset))
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// CreateIssueRequest is POST /v1/issues' body. The author is not a field:
// the daemon derives it.
type CreateIssueRequest struct {
	ProjectID int64    `json:"project_id"`
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Priority  int      `json:"priority,omitempty"`
}

// IssuePatch is PATCH /v1/issues/{id}'s body. Version is the one the caller
// read; nil fields are unchanged. Labels replaces the set and excludes
// AddLabels/RemoveLabels.
type IssuePatch struct {
	Version      int64     `json:"version"`
	Title        *string   `json:"title,omitempty"`
	Body         *string   `json:"body,omitempty"`
	Labels       *[]string `json:"labels,omitempty"`
	AddLabels    []string  `json:"add_labels,omitempty"`
	RemoveLabels []string  `json:"remove_labels,omitempty"`
	Kind         *string   `json:"kind,omitempty"`
	Priority     *int      `json:"priority,omitempty"`
}

// CloseIssueRequest is POST /v1/issues/{id}/close's body. An empty Reason
// is completed; DuplicateOf needs Reason "duplicate".
type CloseIssueRequest struct {
	Reason      string `json:"reason,omitempty"`
	DuplicateOf *int64 `json:"duplicate_of,omitempty"`
}

// IssueLabel is one entry of a project's label catalogue.
type IssueLabel struct {
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	IssueCount  int    `json:"issue_count"`
}

// ListIssues lists issues, most recently updated first unless Sort says
// otherwise. Rows carry no body.
func (c *Client) ListIssues(ctx context.Context, opts IssueListOptions) ([]Issue, error) {
	var out []Issue
	if err := c.get(ctx, "/v1/issues"+opts.query(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateIssue files an issue. A non-empty idempotencyKey makes the create
// replayable: re-sending the same request with the same key returns the
// issue the first send created (§13.1).
func (c *Client) CreateIssue(ctx context.Context, req CreateIssueRequest, idempotencyKey string) (Issue, error) {
	var hdr http.Header
	if idempotencyKey != "" {
		hdr = http.Header{"Idempotency-Key": {idempotencyKey}}
	}
	var out Issue
	if err := c.sendWith(ctx, http.MethodPost, "/v1/issues", hdr, req, &out); err != nil {
		return Issue{}, err
	}
	return out, nil
}

// GetIssue fetches one issue in full.
//
// A non-empty workflow asks for Issue.Prefill as well: what a task created
// from this issue with that workflow would be prefilled with.
func (c *Client) GetIssue(ctx context.Context, id int64, workflow string) (Issue, error) {
	var out Issue
	path := fmt.Sprintf("/v1/issues/%d", id)
	if workflow != "" {
		path += "?" + url.Values{"workflow": {workflow}}.Encode()
	}
	if err := c.get(ctx, path, &out); err != nil {
		return Issue{}, err
	}
	return out, nil
}

// PatchIssue edits an issue at the version the caller read. A stale version
// is a 409 IssueConflict can read the current issue out of.
func (c *Client) PatchIssue(ctx context.Context, id int64, p IssuePatch) (Issue, error) {
	var out Issue
	if err := c.send(ctx, http.MethodPatch, fmt.Sprintf("/v1/issues/%d", id), p, &out); err != nil {
		return Issue{}, err
	}
	return out, nil
}

// CloseIssue closes an open issue.
func (c *Client) CloseIssue(ctx context.Context, id int64, req CloseIssueRequest) (Issue, error) {
	var out Issue
	if err := c.post(ctx, fmt.Sprintf("/v1/issues/%d/close", id), req, &out); err != nil {
		return Issue{}, err
	}
	return out, nil
}

// ReopenIssue reopens a closed issue.
func (c *Client) ReopenIssue(ctx context.Context, id int64) (Issue, error) {
	var out Issue
	if err := c.post(ctx, fmt.Sprintf("/v1/issues/%d/reopen", id), struct{}{}, &out); err != nil {
		return Issue{}, err
	}
	return out, nil
}

// DeleteIssue permanently deletes an issue, in any state. An imported one
// leaves a tombstone so it is never imported again; upstream is untouched.
func (c *Client) DeleteIssue(ctx context.Context, id int64) error {
	return c.send(ctx, http.MethodDelete, fmt.Sprintf("/v1/issues/%d", id), nil, nil)
}

// ListIssueLabels fetches a project's label catalogue.
func (c *Client) ListIssueLabels(ctx context.Context, projectID int64) ([]IssueLabel, error) {
	var out []IssueLabel
	if err := c.get(ctx, fmt.Sprintf("/v1/projects/%d/issue-labels", projectID), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// IssueConflict reports whether err is an issue 409 and, if so, its reason
// and — for IssueReasonChanged — the issue as it is now.
func IssueConflict(err error) (reason string, current *Issue, ok bool) {
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusConflict {
		return "", nil, false
	}
	reason = e.Details["reason"]
	if raw, has := e.RawDetails["issue"]; has {
		var iss Issue
		if json.Unmarshal(raw, &iss) == nil {
			current = &iss
		}
	}
	return reason, current, true
}
