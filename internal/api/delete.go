package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
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
	// Force removes the row from any state without a live process, and
	// never fails on its cleanup (task 153).
	Force bool `json:"force"`
}

// deleteResponse is what a permanent delete says (§13.2, task 092). `branch`
// carries the same shape and the same snake_case vocabulary archive's does —
// deleted | has_commits | not_ours | unknown | error — and is omitted entirely
// when the branch step did not run.
type deleteResponse struct {
	Deleted bool            `json:"deleted"`
	Branch  *branchResponse `json:"branch,omitempty"`
	// Forced marks a force delete (task 153); Warnings is what its
	// best-effort cleanup left behind — a worktree directory, a stale git
	// registration — each naming the row it belonged to. Both are omitted
	// on an ordinary delete.
	Forced   bool     `json:"forced,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// deleteOptions is what a DELETE asked for.
type deleteOptions struct {
	branch, force bool
}

// deleteOptionsWanted reads `delete_branch` and `force` from the query or the
// body, a query value winning. Both default to false: keeping a branch is the
// recoverable answer, and so is refusing a delete. The two are exclusive — a
// force delete never deletes a branch (task 153).
func deleteOptionsWanted(w http.ResponseWriter, r *http.Request) (deleteOptions, bool) {
	var opts deleteOptions
	for _, f := range []struct {
		name string
		dst  *bool
	}{{"delete_branch", &opts.branch}, {"force", &opts.force}} {
		switch v := r.URL.Query().Get(f.name); v {
		case "true":
			*f.dst = true
		case "", "false":
		default:
			writeError(w, http.StatusBadRequest, CodeValidationFailed, f.name+" must be true or false")
			return opts, false
		}
	}
	var req deleteRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return opts, false
	}
	opts.branch = opts.branch || req.DeleteBranch
	opts.force = opts.force || req.Force
	if opts.branch && opts.force {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"force and delete_branch cannot be combined: a force delete never deletes a branch")
		return opts, false
	}
	return opts, true
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
	opts, ok := deleteOptionsWanted(w, r)
	if !ok {
		return
	}
	if opts.force {
		warnings, err := s.deps.Runner.ForceDelete(r.Context(), id)
		if !s.writeDeleteError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, deleteResponse{Deleted: true, Forced: true, Warnings: warnings})
		return
	}
	branch := opts.branch
	// The task's linked chats go with it through the schema's cascade (task
	// 119); their transcripts are listed first, while the rows still exist.
	taskID := id
	linked, err := s.deps.Store.ListChats(r.Context(),
		store.ChatFilter{TaskID: &taskID, Archived: store.ArchivedAll})
	if err != nil {
		s.internalError(w, "list linked chats", err)
		return
	}
	out, err := s.deps.Runner.Delete(r.Context(), id, branch)
	if !s.writeDeleteError(w, err) {
		return
	}
	for i := range linked {
		taskrun.RemoveTranscriptDir(s.deps.Dirs.Data, worktree.ChatOwner(linked[i].ID).Dir(), s.deps.Logger)
		// A linked chat's checkpoints live in the task's repository and go
		// with its row (task 167 decision 12).
		s.pruneChatRefs(r.Context(), linked[i].ProjectID, linked[i].ID)
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
	opts, ok := deleteOptionsWanted(w, r)
	if !ok {
		return
	}
	if opts.force {
		warnings, err := s.forceDeleteChat(r, chat.ID)
		if !s.writeDeleteError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, deleteResponse{Deleted: true, Forced: true, Warnings: warnings})
		return
	}
	wantBranch := opts.branch
	if wantBranch && chat.Linked() {
		// The branch copy on a linked chat is display history, never trusted
		// for a delete: the branch is the task's (task 119 decision 1).
		writeChatLinked(w, chat, "delete")
		return
	}
	if wantBranch && chat.OnIssue() {
		// An issue chat's branch is its issue's main branch (task 140
		// decision 4): archive never deletes it, and neither does this.
		writeErrorDetails(w, http.StatusConflict, CodeIssueChatBranch,
			fmt.Sprintf("branch %q is issue %d's main branch; delete the chat without delete_branch",
				chat.Branch, *chat.IssueID),
			map[string]string{"issue_id": fmt.Sprint(*chat.IssueID), "branch": chat.Branch})
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
	// The checkpoint refs go with the row (task 167 decision 12), with or
	// without delete_branch: nothing can rewind to them once it is gone.
	s.pruneChatRefs(r.Context(), chat.ProjectID, chat.ID)
	var out worktree.BranchOutcome
	if projectPath != "" {
		// A chat has no pull-request head to protect (task 064), but since
		// task 125 it may run on a branch the user named, and vincent deletes
		// only branches it cut.
		ours := !chat.AdoptedBranch
		var err error
		out, err = s.deps.Worktrees.DeleteEmptyBranch(r.Context(), projectPath,
			chat.BaseBranch, chat.BaseSHA, chat.Branch, false, &ours)
		if err != nil {
			s.deps.Logger.Warn("delete: branch kept",
				"chat", chat.ID, "branch", chat.Branch, "result", out.Result, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, deleteResponse{Deleted: true, Branch: toBranchResponse(out)})
}

// forceDeleteChat removes a chat from any state without a live turn (task
// 153) and then cleans up after it, best-effort: its transcripts, and its
// worktree when the directory is the chat's own. A linked chat's directory is
// its task's (task 119), a handed-off chat's belongs to the task it was
// handed to (task 074), and an issue chat's stays when a main task waits to
// receive it (task 140 decision 3) — exactly what archive would keep.
func (s *Server) forceDeleteChat(r *http.Request, id int64) ([]string, error) {
	ctx := r.Context()
	if s.deps.Chats != nil && s.deps.Chats.Running(id) {
		return nil, &store.DeleteRefusedError{
			Kind: "chat", ID: id, Reason: store.DeleteRefusedLive,
			Message: fmt.Sprintf("chat %d still has a live turn; cancel it first", id),
		}
	}
	// A shell command still running in the chat is stopped first (task
	// 170), so its output file is closed before the directory goes.
	// It stays held until the row is gone, so none starts meanwhile.
	if s.deps.Chats != nil {
		defer s.deps.Chats.HoldShell(ctx, id)()
	}
	chat, err := s.deps.Store.ForceDeleteChat(ctx, id)
	if err != nil {
		return nil, err
	}
	s.deps.Logger.Warn("force delete: chat removed", "chat", id, "state", chat.State)
	taskrun.RemoveTranscriptDir(s.deps.Dirs.Data, worktree.ChatOwner(id).Dir(), s.deps.Logger)
	// Even a handed-off chat's: a hand-off keeps the refs only while the
	// chat's row exists (task 167 decision 12).
	s.pruneChatRefs(ctx, chat.ProjectID, id)
	warnings := []string{}
	if chat.WorktreePath == "" || chat.Linked() || chat.State == chatstate.HandedOff {
		return warnings, nil
	}
	if chat.OnIssue() {
		waiting, err := s.deps.Store.IssueHasWaitingMainTask(ctx, *chat.IssueID)
		if err != nil || waiting {
			// A failed read keeps the directory: one left for gc is
			// recoverable, one taken from a waiting task is not.
			return warnings, nil
		}
	}
	projectPath := ""
	if p, err := s.deps.Store.GetProject(ctx, chat.ProjectID); err == nil {
		projectPath = p.Path
	}
	if err := s.deps.Worktrees.Discard(ctx, projectPath, chat.WorktreePath); err != nil {
		s.deps.Logger.Warn("force delete: worktree left behind", "chat", id, "error", err)
		warnings = append(warnings, fmt.Sprintf("chat %d: %v", id, err))
	}
	return warnings, nil
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
