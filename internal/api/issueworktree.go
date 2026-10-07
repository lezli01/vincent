package api

// A main task whose issue worktree moved on (task 134.12, task 134
// decision 14).

import (
	"context"
	"fmt"
	"net/http"

	"github.com/lezli01/vincent/internal/store"
)

// CodeIssueWorktreeMoved is a follow-up or chat refused on a settled main
// task whose issue's main worktree has since been handed to another main
// task (task 134 decision 14). `details.holder_task_id` names the task that
// holds the directory now: the work continues there, or in a new main task.
const CodeIssueWorktreeMoved = "issue_worktree_moved"

// CodeIssueHasLiveMainTask is an issue delete refused while one of its main
// tasks has not settled (task 134.12). `details.task_id` names the lowest
// such task.
const CodeIssueHasLiveMainTask = "issue_has_live_main_task"

// issueWorktreeHolder reports the task that took over t's issue main
// worktree, or nil when t still has its own or never had one to give. A task
// is moved when it is a main task of an issue, done or aborted, holds no
// worktree, and another main task of the issue does. An archived one is not
// asked: the §6 machine already refuses it every action this guards.
func (s *Server) issueWorktreeHolder(ctx context.Context, t *store.Task) (*store.Task, error) {
	if t.IssueWorktree != store.IssueWorktreeMain || t.IssueID == nil || t.WorktreePath != "" {
		return nil, nil
	}
	if t.State != store.TaskDone && t.State != store.TaskAborted {
		return nil, nil
	}
	holder, err := s.deps.Store.IssueMainWorktreeHolder(ctx, *t.IssueID, t.ID)
	if err != nil {
		return nil, fmt.Errorf("issue %d main worktree holder: %w", *t.IssueID, err)
	}
	return holder, nil
}

// refuseIssueWorktreeMoved writes the 409 issue_worktree_moved for an action
// on t when its issue's main worktree moved on, and reports whether it did
// (including a 500 for a failed read).
func (s *Server) refuseIssueWorktreeMoved(ctx context.Context, w http.ResponseWriter, t *store.Task) bool {
	holder, err := s.issueWorktreeHolder(ctx, t)
	if err != nil {
		s.internalError(w, "issue main worktree holder", err)
		return true
	}
	if holder == nil {
		return false
	}
	writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
		Code: CodeIssueWorktreeMoved,
		Message: fmt.Sprintf("task %d handed issue %d's main worktree to task %d; "+
			"continue there, or start a new main task", t.ID, *t.IssueID, holder.ID),
		Details: map[string]string{
			"holder_task_id": fmt.Sprint(holder.ID),
			"state":          string(t.State),
		},
	}})
	return true
}
