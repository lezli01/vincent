package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/worktree"
)

// deleteRequest is the §13.2 body of DELETE /v1/tasks/{id} and
// DELETE /v1/chats/{id}. `delete_branch` may also arrive as a query parameter,
// because a `DELETE` with a body is awkward from curl and the gate scripts are
// written in curl.
type deleteRequest struct {
	DeleteBranch bool `json:"delete_branch"`
}

// deleteResponse is what a permanent delete says (§13.2, task 092). `branch`
// carries the same shape and the same snake_case vocabulary archive's does —
// deleted | has_commits | not_ours | unknown | error — and is omitted entirely
// when the branch step did not run.
type deleteResponse struct {
	Deleted bool            `json:"deleted"`
	Branch  *branchResponse `json:"branch,omitempty"`
}

// deleteBranchWanted reads `delete_branch` from the query or the body, in that
// order. It defaults to false: keeping a branch is the recoverable answer.
func deleteBranchWanted(w http.ResponseWriter, r *http.Request) (bool, bool) {
	switch v := r.URL.Query().Get("delete_branch"); v {
	case "true":
		return true, true
	case "", "false":
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "delete_branch must be true or false")
		return false, false
	}
	var req deleteRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return false, false
	}
	return req.DeleteBranch, true
}

// handleTaskDelete permanently removes an archived task (§13.2, task 092).
//
// It is not a §6 action and never appears in `available_actions`: the state
// check is this handler's own, through the store's delete transaction, not the
// FSM's. The precedent is DELETE /v1/projects/{id}, which is likewise no
// action. A refusal is a 409 in the snake_case envelope naming the row that is
// holding on — see store.DeleteRefusedError for the four of them.
func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	if s.deps.Runner == nil {
		s.internalError(w, "task delete", errors.New("no task runner is configured"))
		return
	}
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return
	}
	branch, ok := deleteBranchWanted(w, r)
	if !ok {
		return
	}
	out, err := s.deps.Runner.Delete(r.Context(), id, branch)
	if !s.writeDeleteError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, deleteResponse{Deleted: true, Branch: toBranchResponse(out)})
}

// handleChatDelete permanently removes an archived chat (§13.2, task 092). It
// is the task handler minus the fan-out clause, and it runs here rather than
// on a runner for the reason handleChatArchive does: the chat lifecycle's
// worktree and branch work already lives in this package.
func (s *Server) handleChatDelete(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	wantBranch, ok := deleteBranchWanted(w, r)
	if !ok {
		return
	}
	// Read the project before the delete: the row is about to stop existing
	// and the §10 judgement needs the repository it lives in.
	var projectPath string
	if wantBranch && chat.Branch != "" {
		project, err := s.deps.Store.GetProject(r.Context(), chat.ProjectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
			return
		}
		projectPath = project.Path
	}
	if err := s.deps.Store.DeleteChatCascade(r.Context(), chat.ID); !s.writeDeleteError(w, err) {
		return
	}
	// After the commit, and unable to fail the call: a failed unlink must not
	// resurrect a row already reported gone, and `vincent gc` treats a
	// transcript directory with no row as its own.
	taskrun.RemoveTranscriptDir(s.deps.Dirs.Data, worktree.ChatOwner(chat.ID).Dir(), s.deps.Logger)
	var out worktree.BranchOutcome
	if projectPath != "" {
		// ours is nil: a chat's branch is always one vincent cut, so there is
		// no pull-request head to protect and nothing to say (task 064).
		var err error
		out, err = s.deps.Worktrees.DeleteEmptyBranch(r.Context(), projectPath,
			chat.BaseBranch, chat.BaseSHA, chat.Branch, false, nil)
		if err != nil {
			s.deps.Logger.Warn("delete: branch kept",
				"chat", chat.ID, "branch", chat.Branch, "result", out.Result, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, deleteResponse{Deleted: true, Branch: toBranchResponse(out)})
}

// writeDeleteError maps a delete failure onto §13.1 and reports whether the
// handler may carry on. A nil error is the only "carry on".
func (s *Server) writeDeleteError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	default:
		var refused *store.DeleteRefusedError
		if errors.As(err, &refused) {
			writeConflict(w, refused.Message, map[string]string{
				"action": "delete", "reason": refused.Reason,
			})
			return false
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
	}
	return false
}

// parseArchivedBounds reads the `archived_before` and `archived_since` RFC3339
// query parameters shared by GET /v1/tasks and GET /v1/chats (§13.2, task
// 092). One vocabulary covers both entities, which issue #298 already settled
// for `archived` itself. Anything unparseable is a 400.
func parseArchivedBounds(w http.ResponseWriter, r *http.Request) (before, since time.Time, ok bool) {
	q := r.URL.Query()
	for name, dst := range map[string]*time.Time{"archived_before": &before, "archived_since": &since} {
		v := q.Get(name)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("%s must be an RFC3339 timestamp", name))
			return time.Time{}, time.Time{}, false
		}
		*dst = t
	}
	return before, since, true
}
