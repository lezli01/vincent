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
	// labels, which mirror the remote, and of a comment on an issue with a
	// live remote, whose thread does (task 130 decision 24, 130.16).
	IssueReasonMirrored = "issue_mirrored"
	// IssueReasonForgeWrite is an agent's close or reopen of an issue whose
	// state writes back to GitHub (task 130.10): only a human's act writes
	// to a forge. A client whose environment marks it as a step or a chat
	// agent gets it too.
	IssueReasonForgeWrite = "forge_write_needs_human"
)

// IssueSync is an imported issue's state write-back (task 130.10). State
// is synced, pending, failed or conflict; Reason says why it is not synced
// (disabled, rate_limited, no_write_scope, moved, gone, …).
type IssueSync struct {
	State        string     `json:"state"`
	Reason       string     `json:"reason,omitempty"`
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
}

// IssueSource is where an imported issue came from.
type IssueSource struct {
	Provider    string `json:"provider"`
	Repo        string `json:"repo"`
	Number      int    `json:"number,omitempty"`
	URL         string `json:"url,omitempty"`
	RemoteState string `json:"remote_state,omitempty"`
	// LastSyncedAt is when the importer last wrote the issue from the
	// remote (task 130.8); nil when it never has.
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	// Status is "moved" or "missing" when the last sweep could no longer
	// find the remote where it was; empty while it is live.
	Status string `json:"status,omitempty"`
}

// IssueTasks is the root tasks an issue started: how many, and which are
// still unsettled.
type IssueTasks struct {
	Count     int     `json:"count"`
	ActiveIDs []int64 `json:"active_ids"`
}

// IssueMainWorktree is an issue's main worktree: its branch, and the
// admitted, unsettled main task holding it — nil when it is free.
type IssueMainWorktree struct {
	Branch         string `json:"branch"`
	OccupantTaskID *int64 `json:"occupant_task_id"`
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
	Sync            *IssueSync   `json:"sync,omitempty"`
	Active          bool         `json:"active"`
	TaskCount       int          `json:"task_count"`
	// Lane is the board lane — open, in_progress, hand_off or done (task
	// 134 decision 2); Attention is whether a root task is waiting on a
	// person (decision 3).
	Lane      string `json:"lane"`
	Attention bool   `json:"attention"`
	// MainWorktree is the issue's main branch and the main task occupying
	// it (task 134 decisions 2, 8); nil while the issue has no main branch.
	MainWorktree *IssueMainWorktree `json:"main_worktree,omitempty"`
	Version      int64              `json:"version"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	ClosedAt     *time.Time         `json:"closed_at,omitempty"`

	Body             string     `json:"body,omitempty"`
	AvailableActions []string   `json:"available_actions,omitempty"`
	Tasks            IssueTasks `json:"tasks"`
	Editable         []string   `json:"editable,omitempty"`
	// Commentable is whether AddIssueComment is taken: false while the
	// issue's GitHub remote is live, true for a local issue and one whose
	// remote moved or went missing (task 130 decision 24.3). A list row
	// leaves it false.
	Commentable bool `json:"commentable,omitempty"`
	// Prefill is what CreateTaskRequest.IssueID would fill in under the
	// workflow GetIssue named (task 130.7); nil when it named none. The
	// daemon computes it with the function the create runs.
	Prefill *GitHubPrefill `json:"prefill,omitempty"`
}

// IssueListOptions filters ListIssues. Zero values mean "no filter".
type IssueListOptions struct {
	ProjectID int64
	States    []string
	Lanes     []string // open, in_progress, hand_off, done; any of them
	Labels    []string // every one must match
	Kind      string
	Query     string // substring of title or body
	Source    string // "local" or "github"
	Sort      string // "updated" (default) or "created"
	Limit     int
	Offset    int
	// RemoteNumber narrows to the issue imported from that GitHub issue
	// number (task 130.11, decision 22.4); the daemon requires ProjectID
	// beside it. It is how a script holding a GitHub number finds the issue
	// id CreateTaskRequest.IssueID takes, now `github_issue` is gone.
	RemoteNumber int
}

func (o IssueListOptions) query() string {
	v := url.Values{}
	if o.ProjectID != 0 {
		v.Set("project_id", strconv.FormatInt(o.ProjectID, 10))
	}
	for _, s := range o.States {
		v.Add("state", s)
	}
	for _, l := range o.Lanes {
		v.Add("lane", l)
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
	if o.RemoteNumber > 0 {
		v.Set("remote_number", strconv.Itoa(o.RemoteNumber))
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

// IssueComment is one comment of an issue's thread (task 130.16). Remote
// marks one mirrored from GitHub, read-only; RemoteKey is its GitHub id.
type IssueComment struct {
	ID        int64     `json:"id"`
	IssueID   int64     `json:"issue_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	Remote    bool      `json:"remote"`
	RemoteKey string    `json:"remote_key,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// The thread's events on §13.3's stream. Each payload is IssueCommentEvent:
// ids and the actor, never the text.
const (
	EventIssueCommentAdded   = "issue.comment_added"
	EventIssueCommentUpdated = "issue.comment_updated"
)

// IssueCommentEvent is the payload of EventIssueCommentAdded and
// EventIssueCommentUpdated: the issue, the comment, and who wrote it (human,
// agent or sync — an update is always sync's, a mirrored edit).
type IssueCommentEvent struct {
	ID        int64  `json:"id"`
	CommentID int64  `json:"comment_id"`
	By        string `json:"by"`
}

// ListIssueComments reads an issue's thread, oldest first.
func (c *Client) ListIssueComments(ctx context.Context, issueID int64) ([]IssueComment, error) {
	var out struct {
		Comments []IssueComment `json:"comments"`
	}
	if err := c.get(ctx, fmt.Sprintf("/v1/issues/%d/comments", issueID), &out); err != nil {
		return nil, err
	}
	return out.Comments, nil
}

// AddIssueComment adds a local comment; the daemon derives its author. One
// on an issue with a live remote is a 409 whose reason is
// IssueReasonMirrored: nothing is ever posted to GitHub.
func (c *Client) AddIssueComment(ctx context.Context, issueID int64, body string) (IssueComment, error) {
	var out IssueComment
	req := struct {
		Body string `json:"body"`
	}{body}
	if err := c.post(ctx, fmt.Sprintf("/v1/issues/%d/comments", issueID), req, &out); err != nil {
		return IssueComment{}, err
	}
	return out, nil
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

// IssueSyncStatus is a project's issue import status (task 130.8): GET and
// POST /v1/projects/{id}/issues/sync.
type IssueSyncStatus struct {
	// Enabled is github.enabled with a non-zero github.poll_interval: whether
	// the reconciler tick imports at all.
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	Repo     string `json:"repo"`
	// LastSyncedAt is when an import last succeeded; nil when none has.
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	OK           bool       `json:"ok"`
	// Reason names why OK is false: github_disabled, poll_disabled,
	// pending, rate_limited, not_github, no_client, origin_changed, or one
	// of internal/github's unavailability reasons.
	Reason           string     `json:"reason,omitempty"`
	ImportComplete   bool       `json:"import_complete"`
	RateLimitedUntil *time.Time `json:"rate_limited_until,omitempty"`
	// WritesPending, WritesFailed and WritesConflict count imported issues
	// whose newest state write-back is waiting, gave up, or found GitHub
	// changed first (task 130.10).
	WritesPending  int `json:"writes_pending"`
	WritesFailed   int `json:"writes_failed"`
	WritesConflict int `json:"writes_conflict"`
}

// IssueSyncStatus reads a project's issue import status. It never starts a
// sync.
func (c *Client) IssueSyncStatus(ctx context.Context, projectID int64) (IssueSyncStatus, error) {
	var out IssueSyncStatus
	if err := c.get(ctx, fmt.Sprintf("/v1/projects/%d/issues/sync", projectID), &out); err != nil {
		return IssueSyncStatus{}, err
	}
	return out, nil
}

// SyncIssues asks the daemon to sync a project's issues now. The daemon
// answers 202 at once with the status as it stands; the sync itself runs on
// the importer's own goroutine.
func (c *Client) SyncIssues(ctx context.Context, projectID int64) (IssueSyncStatus, error) {
	var out IssueSyncStatus
	if err := c.post(ctx, fmt.Sprintf("/v1/projects/%d/issues/sync", projectID), struct{}{}, &out); err != nil {
		return IssueSyncStatus{}, err
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
