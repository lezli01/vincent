package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Human actions as the daemon names them in available_actions (§6). A client
// renders a bar from the strings the daemon sends, so these exist to key the
// keymap onto, never to decide what is valid.
const (
	ActionCancel  = "cancel"
	ActionPause   = "pause"
	ActionResume  = "resume"
	ActionRetry   = "retry"
	ActionRepair  = "repair"
	ActionSkip    = "skip"
	ActionAnswer  = "answer"
	ActionApprove = "approve"
	ActionReject  = "reject"
	ActionArchive = "archive"
)

// RepairStepID is the reserved step id the daemon records an ad-hoc repair
// run under (§5.4, task 025). A repair's row sits at the *blocked step's*
// index, so this is what tells the two apart — a client that rendered it as
// an attempt of that step would say the opposite of what happened.
//
// It is declared here for the reason the action names above are: the client
// owns its wire types, and a string the daemon sends is keyed onto rather
// than imported from the engine. A test pins the two together.
const RepairStepID = "__repair"

// Override is the body of POST /v1/tasks/{id}/retry (§6). Prompt and Run are
// edit+retry: the text replaces the failing step's prompt or command in this
// task's snapshot only. All empty is a plain retry.
type Override struct {
	Prompt string `json:"prompt_override,omitempty"`
	Run    string `json:"run_override,omitempty"`
	// Branch renames the task's branch before the retry re-admits it, which is
	// how a `branch_exists` block is recovered without losing the task and its
	// transcripts (task 001). Unlike the other two it does not touch the
	// snapshot.
	Branch string `json:"branch_override,omitempty"`
}

// Cancel aborts the task, killing any live process (§6).
func (c *Client) Cancel(ctx context.Context, id int64) (Task, error) {
	return c.action(ctx, id, ActionCancel, nil)
}

// Pause holds the task at the next step boundary (§6).
func (c *Client) Pause(ctx context.Context, id int64) (Task, error) {
	return c.action(ctx, id, ActionPause, nil)
}

// Resume re-queues a paused task (§6).
func (c *Client) Resume(ctx context.Context, id int64) (Task, error) {
	return c.action(ctx, id, ActionResume, nil)
}

// Retry re-runs the failed step. A zero Override is a plain retry; either
// field set is edit+retry, which rewrites this task's snapshot (§6).
func (c *Client) Retry(ctx context.Context, id int64, ov Override) (Task, error) {
	if ov == (Override{}) {
		return c.action(ctx, id, ActionRetry, nil)
	}
	return c.action(ctx, id, ActionRetry, ov)
}

// RepairInput is the body of POST /v1/tasks/{id}/repair (§6, task 025).
// Prompt is required and literal — it is prose, not a template — and the
// optional triple stands in for the step level of §8.6's chain for that one
// run: request > task override > the workflow's `defaults:` > adapter
// default.
type RepairInput struct {
	Prompt string `json:"prompt"`
	Agent  string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Repair launches a one-off agent in the blocked task's existing worktree
// (§6, task 025). The task re-queues, runs the agent, and returns to
// `blocked` at the same step with the same reason whatever the agent did —
// the repair changes the worktree, and a human still decides whether to
// retry.
//
// The second return carries the §8.2 catalog warnings the selection raised,
// the way task creation reports them; an empty prompt is a 400.
func (c *Client) Repair(ctx context.Context, id int64, in RepairInput) (Task, []string, error) {
	var out repairResponse
	path := fmt.Sprintf("/v1/tasks/%d/%s", id, ActionRepair)
	if err := c.post(ctx, path, in, &out); err != nil {
		return Task{}, nil, err
	}
	return out.Task, out.Warnings, nil
}

// repairResponse decodes the repair body, whose task fields sit at the top
// level beside `warnings` — the shape archive's response uses for `branch`.
type repairResponse struct {
	Task
	Warnings []string `json:"warnings"`
}

// Skip marks the current step skipped and advances (§6).
func (c *Client) Skip(ctx context.Context, id int64) (Task, error) {
	return c.action(ctx, id, ActionSkip, nil)
}

// Approve passes a manual gate (§6).
func (c *Client) Approve(ctx context.Context, id int64) (Task, error) {
	return c.action(ctx, id, ActionApprove, nil)
}

// Reject fails a manual gate, blocking the task (§6).
func (c *Client) Reject(ctx context.Context, id int64) (Task, error) {
	return c.action(ctx, id, ActionReject, nil)
}

// Archive removes the worktree, archives the task, and — when
// `delete_empty_branch_on_archive` is on — deletes the branch if it carries no
// commits past its base (§10, task 008). The second return is what happened to
// that branch; its zero value means the branch step did not run.
//
// A dirty worktree is a 409 carrying details.reason "worktree_dirty" unless
// force is set — force is the confirmation, so a caller re-issues with it after
// asking (§6, §13.2).
func (c *Client) Archive(ctx context.Context, id int64, force bool) (Task, BranchOutcome, error) {
	var out archiveResponse
	path := fmt.Sprintf("/v1/tasks/%d/%s", id, ActionArchive)
	if err := c.post(ctx, path, archiveBody{Force: force}, &out); err != nil {
		return Task{}, BranchOutcome{}, err
	}
	if out.Branch == nil {
		return out.Task, BranchOutcome{}, nil
	}
	return out.Task, *out.Branch, nil
}

type archiveBody struct {
	Force bool `json:"force"`
}

// archiveResponse decodes the archive body, whose task fields sit at the top
// level beside `branch` — so the task is decoded from the same object rather
// than from a nested one.
type archiveResponse struct {
	Task
	Branch *BranchOutcome `json:"branch"`
}

// Branch outcomes as the daemon names them (§10, task 008). A client renders
// the string the daemon sends; these exist to key rendering onto.
const (
	BranchDeleted      = "deleted"
	BranchHasCommits   = "has_commits"
	BranchUnknown      = "unknown"
	BranchDeleteFailed = "error"

	RemoteBranchDeleted    = "deleted"
	RemoteBranchNoUpstream = "no_upstream"
	RemoteBranchFailed     = "error"
)

// BranchOutcome is what an archive did to the task's branch. A zero value means
// the branch step never ran: the setting is off, or the task had no branch of
// its own — which is what every archive did before task 008.
type BranchOutcome struct {
	Name   string               `json:"name"`
	Result string               `json:"result"`
	Error  string               `json:"error,omitempty"`
	Remote *RemoteBranchOutcome `json:"remote,omitempty"`
}

// RemoteBranchOutcome is the opt-in remote leg's outcome.
type RemoteBranchOutcome struct {
	Remote string `json:"remote,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// Summary is the one line a human gets for a branch outcome, or "" when
// nothing happened to the branch and there is nothing to say.
func (o BranchOutcome) Summary() string {
	switch o.Result {
	case BranchDeleted:
		s := "branch " + o.Name + " deleted (no commits)"
		if o.Remote != nil {
			switch o.Remote.Result {
			case RemoteBranchDeleted:
				s += ", remote too"
			case RemoteBranchFailed:
				s += ", remote kept: " + o.Remote.Error
			}
		}
		return s
	case BranchHasCommits:
		return "branch " + o.Name + " kept (it has commits)"
	case BranchUnknown, BranchDeleteFailed:
		return "branch " + o.Name + " kept: " + o.Error
	default:
		return ""
	}
}

// Answer delivers the answer to a pending input request; the run resumes in
// place (§7.4).
func (c *Client) Answer(ctx context.Context, id int64, resp InputResponse) (Task, error) {
	return c.action(ctx, id, ActionAnswer, resp.body())
}

// action posts one §6 human action and decodes the updated task. The response
// is the daemon's own view of the task after the action, which callers render
// immediately rather than predicting the transition themselves.
func (c *Client) action(ctx context.Context, id int64, name string, body any) (Task, error) {
	var out Task
	path := fmt.Sprintf("/v1/tasks/%d/%s", id, name)
	if err := c.post(ctx, path, body, &out); err != nil {
		return Task{}, err
	}
	return out, nil
}

// post performs an authenticated POST, encoding body as JSON when non-nil and
// decoding the response into out. Non-2xx responses come back as *Error, with
// the §13.1 details object intact.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.send(ctx, http.MethodPost, path, body, out)
}

// send performs an authenticated write of any method. A nil body sends none
// and a nil out discards the response, which is what a 204 needs.
func (c *Client) send(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s body: %w", path, err)
		}
		rdr = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build request %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.rest.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// Diff fetches the task's unified diff: the worktree against merge-base with
// its base branch (§13.2). The endpoint serves text/plain, and answers 409
// for a task with no worktree yet and for one whose worktree is gone — two
// situations a reader must be able to tell apart, so the *Error is returned
// as-is rather than flattened to "no diff".
func (c *Client) Diff(ctx context.Context, id int64) (string, error) {
	path := "/v1/tasks/" + strconv.FormatInt(id, 10) + "/diff"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return "", fmt.Errorf("build diff request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.rest.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET diff: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", decodeError(resp)
	}
	// A diff is bounded by the worktree, but an agent can rewrite a vendored
	// tree; the pane truncates for display and this bounds the read.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiffBytes))
	if err != nil {
		return "", fmt.Errorf("read diff: %w", err)
	}
	return string(body), nil
}

// maxDiffBytes caps a single diff read at 8 MiB; T4.3 owns real limits.
const maxDiffBytes = 8 << 20
