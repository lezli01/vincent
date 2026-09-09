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
	Deleted bool           `json:"deleted"`
	Branch  *BranchOutcome `json:"branch"`
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
