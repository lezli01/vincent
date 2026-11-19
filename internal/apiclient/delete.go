package apiclient

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

// deleteResponse decodes DELETE /v1/tasks/{id} and DELETE /v1/chats/{id}
// (§13.2, task 092). `branch` carries archive's vocabulary unchanged, so the
// same BranchOutcome and the same Summary render both.
type deleteResponse struct {
	Deleted  bool           `json:"deleted"`
	Branch   *BranchOutcome `json:"branch"`
	Forced   bool           `json:"forced"`
	Warnings []string       `json:"warnings"`
}

// ForceDeleteTask permanently removes a task from any state but one with a
// live agent process (running, awaiting_input), together with its fan-out
// lanes and the chats tied to it (task 153). It is the way out for a task
// whose archive or delete keeps failing — a git error vincent cannot resolve.
// Its worktree, container and transcripts are cleaned best-effort after the
// row is gone; what could not be cleaned comes back as warnings, never as an
// error. A branch is never deleted. A live process is refused with a 409
// whose DeleteRefused reason is `live_process`.
func (c *Client) ForceDeleteTask(ctx context.Context, id int64) ([]string, error) {
	return c.forceDeleteRow(ctx, "/v1/tasks/", id)
}

// ForceDeleteChat is ForceDeleteTask for a chat (task 153): any state but
// running and awaiting_input. A linked or handed-off chat's worktree is its
// task's, and is left alone.
func (c *Client) ForceDeleteChat(ctx context.Context, id int64) ([]string, error) {
	return c.forceDeleteRow(ctx, "/v1/chats/", id)
}

func (c *Client) forceDeleteRow(ctx context.Context, prefix string, id int64) ([]string, error) {
	var out deleteResponse
	if err := c.send(ctx, http.MethodDelete, prefix+strconv.FormatInt(id, 10)+"?force=true", nil, &out); err != nil {
		return nil, err
	}
	return out.Warnings, nil
}

// DeleteTask permanently removes an archived task: the row, its step_runs and
// its transcripts. A task in any other state is refused with a 409 naming what
// is holding on, and so is an archived fan-out parent whose lanes still exist
// or one a handed-off chat still points at.
//
// deleteBranch asks for the branch as well, and §10's standing rule is
// unchanged by the asking: a branch carrying any commit past its base is kept
// and reported `has_commits`. The remote branch is never touched — that leg is
// archive's alone.
func (c *Client) DeleteTask(ctx context.Context, id int64, deleteBranch bool) (BranchOutcome, error) {
	return c.deleteRow(ctx, "/v1/tasks/", id, deleteBranch)
}

// DeleteChat permanently removes an archived chat: the row, its turns and its
// transcripts. A `handed_off` chat is refused — the task it was handed to owns
// the worktree and branch, and that task is what to delete.
func (c *Client) DeleteChat(ctx context.Context, id int64, deleteBranch bool) (BranchOutcome, error) {
	return c.deleteRow(ctx, "/v1/chats/", id, deleteBranch)
}

func (c *Client) deleteRow(ctx context.Context, prefix string, id int64, deleteBranch bool) (BranchOutcome, error) {
	path := prefix + strconv.FormatInt(id, 10)
	if deleteBranch {
		// A query parameter rather than a body: a DELETE with a body is
		// awkward from curl, and the gates are written in curl. The daemon
		// accepts both.
		path += "?delete_branch=true"
	}
	var out deleteResponse
	if err := c.send(ctx, http.MethodDelete, path, nil, &out); err != nil {
		return BranchOutcome{}, err
	}
	if out.Branch == nil {
		return BranchOutcome{}, nil
	}
	return *out.Branch, nil
}

// DeleteRefused reports whether err is a delete the daemon refused — a 409 in
// the §13.1 envelope naming the row that is holding on — and, if so, the
// snake_case reason from its `details`. It exists so a client can count
// refusals apart from failures without matching on message text: a refusal is
// acted on by deleting whatever is named, and a failure is not.
func DeleteRefused(err error) (string, bool) {
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusConflict {
		return "", false
	}
	return e.Details["reason"], true
}
