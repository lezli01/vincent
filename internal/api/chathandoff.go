package api

// POST /v1/chats/{id}/handoff — handing a chat's worktree and branch to a new
// task (task 074, spec §5.5, §10, §13.2).
//
// The route lives in the chats family rather than as a `source_chat_id` field
// on POST /v1/tasks, which is the shape task 064 used for `github_pull`
// (decision 1). `POST /v1/tasks` *is* the `task_create` MCP tool, and task 063
// decision 2 excluded the whole chat family from MCP precisely so an agent
// cannot start agent processes outside the `created_by_task_id` chain that
// `mcp.max_depth` and `mcp.max_tasks` walk. A chat is not in that chain, so a
// field on the task-create route would need a field-level MCP guard — a shape
// the exclusion list does not have. The exclusion is therefore extended rather
// than excepted: this route joins the literal list in internal/mcp.

import (
	"errors"
	"net/http"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// handleChatHandoff creates a task that adopts the chat's worktree, branch,
// base branch and base SHA verbatim, and leaves the chat terminal.
//
// The body is `POST /v1/tasks`' body, validated by the same code (§13.2): the
// two routes must accept the same task or the difference becomes a task one
// takes and the other refuses. `project_id`, `base_branch` and `branch_name`
// are the chat's and are ignored — a handoff has nothing to name, because the
// branch already exists and is what is being transferred.
//
// Everything is validated before anything is written, and everything that is
// written commits together. The order matters: the workflow, the fields and
// the git state are checked first, so a refusal leaves the chat exactly as it
// was; only then does one transaction create the task, link it, transition the
// chat and release its §10 claim; and only after that commit is the scheduler
// woken, so it cannot admit a task whose workspace it does not yet own.
func (s *Server) handleChatHandoff(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	var req taskCreateRequest
	// The large bound, for the reason POST /v1/tasks takes it: the
	// description is what the workflow templates into an agent's prompt, and
	// on this route it is the *only* thing carrying the conversation's
	// context — nothing about the chat is injected automatically (decision 3).
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if b := boundTaskCreate(&req); b != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, b)
		return
	}
	if !chatstate.Allowed(chat.State, chatstate.HandOff) {
		writeConflict(w, "only an idle chat can be handed off to a task",
			map[string]string{"state": string(chat.State), "action": string(chatstate.HandOff)})
		return
	}
	// A chat with no claim has nothing to hand over: creation failed partway,
	// or the row was archived. Refusing here is what stops the alternative —
	// a task with an empty worktree_path, which admission would quietly
	// resolve by cutting a *new* worktree, silently defeating the whole
	// feature.
	if chat.WorktreePath == "" {
		writeConflict(w, "this chat has no worktree to hand over",
			map[string]string{"state": string(chat.State), "action": string(chatstate.HandOff)})
		return
	}
	ctx := r.Context()
	// Git state before the database, because it is the check most likely to
	// fail and the one whose failure must leave the chat untouched. Ordinary
	// dirty state is not a refusal: uncommitted work is what a handoff
	// preserves (decision 4).
	op, err := s.deps.Worktrees.InProgressOp(ctx, chat.WorktreePath)
	if err != nil {
		s.internalError(w, "probe worktree state", err)
		return
	}
	if op != "" {
		writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
			Code: CodeRepoOperationInProgress,
			Message: "this chat's worktree is in the middle of a " + op +
				"; finish or abort it before handing off",
			Details: map[string]string{
				"state": string(chat.State), "action": string(chatstate.HandOff), "operation": op,
			},
		}})
		return
	}
	req.ProjectID = chat.ProjectID
	prep, ok := s.prepareTaskCreate(ctx, w, &req, chat.BaseBranch)
	if !ok {
		return
	}
	t := prep.task
	// The inheritance, character for character. There is no third worktree
	// creation mode behind this: taskrun's ensureWorktree returns early for a
	// task that already has a path, so admission runs no git at all and the
	// directory keeps the name the chat gave it — informational, since the
	// claim decides (063 decision 9).
	t.BranchName = chat.Branch
	t.BaseBranch = chat.BaseBranch
	t.BaseSHA = chat.BaseSHA
	t.WorktreePath = chat.WorktreePath
	updated, err := s.deps.Store.HandoffChat(ctx, chat.ID, &t)
	var claimed *store.BranchClaimedError
	switch {
	case errors.As(err, &claimed):
		// A live task already on this branch. The transaction rolled back, so
		// the chat is still idle and still owns its worktree.
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"branch "+claimed.Branch+" is already claimed by another task")
		return
	case errors.Is(err, store.ErrInvalidChatAction):
		// The compare-and-set lost: a `send` landed between the read above
		// and the write. Exactly one of the two happened, and it was not this.
		writeConflict(w, "this chat changed state while the handoff was being prepared",
			map[string]string{"state": string(chat.State), "action": string(chatstate.HandOff)})
		return
	case err != nil:
		s.internalError(w, "hand off chat", err)
		return
	}
	if s.deps.WakeRunner != nil {
		s.deps.WakeRunner()
	}
	resp := toTaskResponse(&t, s.snaps.get(t.ID, t.WorkflowSnapshot))
	resp.Warnings = prep.warnings
	resp.SourceChatID = &chat.ID
	writeJSON(w, http.StatusCreated, map[string]any{
		"task": resp, "chat": renderChat(updated),
	})
}
