package api

// POST /v1/tasks/{id}/chat and POST /v1/chats/{id}/close — a chat linked to a
// task (task 119, spec §5.5, §6, §13.2).
//
// Both routes join internal/mcp's literal exclusion list, extending 063
// decision 2 rather than excepting it: a linked chat starts agent processes
// outside the `created_by_task_id` chain `mcp.max_depth` walks, exactly as a
// free one does.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
)

// CodeTaskLockedByChat is a §6 action refused because a chat linked to the
// task is open (task 119). `details.chat_id` names the chat to close.
const CodeTaskLockedByChat = "task_locked_by_chat"

// CodeTaskHasNoWorktree is a chat refused on a task that never got a
// worktree — blocked on `branch_exists`, or aborted before admission. The
// daemon does not create one for a chat: the engine owns worktree preparation.
const CodeTaskHasNoWorktree = "task_has_no_worktree"

// CodeChatLinkedToTask is a chat action refused because the chat works in a
// task's worktree, which that task owns: `archive`, `hand_off`, and
// `DELETE ?delete_branch=true` (task 119). `details.task_id` names the task.
const CodeChatLinkedToTask = "chat_linked_to_task"

// writeTaskLocked renders a *store.TaskLockedError.
func writeTaskLocked(w http.ResponseWriter, e *store.TaskLockedError, state string) {
	details := map[string]string{"chat_id": fmt.Sprint(e.ChatID)}
	if state != "" {
		details["state"] = state
	}
	writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
		Code: CodeTaskLockedByChat, Message: e.Error(), Details: details,
	}})
}

// writeChatLinked refuses a chat-side action that would reach the task's
// worktree or branch, naming the task.
func writeChatLinked(w http.ResponseWriter, c *store.Chat, action string) {
	writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
		Code: CodeChatLinkedToTask,
		Message: fmt.Sprintf("this chat works in task %d's worktree, which that task owns; "+
			"close the chat instead", *c.LinkedTaskID),
		Details: map[string]string{
			"state": string(c.State), "action": action, "task_id": fmt.Sprint(*c.LinkedTaskID),
		},
	}})
}

// openTaskChatRequest is the §13.2 body of POST /v1/tasks/{id}/chat. Every
// field is optional: the title defaults to the task's, and the triple stands
// in for the step level of §8.6's chain, as a repair's does (025 decision 6).
type openTaskChatRequest struct {
	Title  *string `json:"title"`
	Agent  *string `json:"agent"`
	Model  *string `json:"model"`
	Effort *string `json:"effort"`
}

// handleTaskChat opens a chat linked to a stopped task (task 119). It moves
// nothing: the task stays in its state, locked, until the chat closes.
func (s *Server) handleTaskChat(w http.ResponseWriter, r *http.Request) {
	var req openTaskChatRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	if s.deps.Runner == nil {
		s.internalError(w, "task chat", errors.New("no task runner is configured"))
		return
	}
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	task, err := s.deps.Store.GetTask(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, "get task", err)
		return
	}
	if !taskstate.Can(task.State, taskstate.Chat) {
		s.writeActionError(w, &taskrun.InvalidActionError{TaskID: id, Action: taskstate.Chat, State: task.State})
		return
	}
	if err := s.deps.Store.RefuseLocked(ctx, id); err != nil {
		s.writeActionError(w, err)
		return
	}
	if task.WorktreePath == "" {
		writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
			Code: CodeTaskHasNoWorktree,
			Message: fmt.Sprintf("task %d has no worktree to talk about; "+
				"vincent does not create one for a chat", id),
			Details: map[string]string{"state": string(task.State), "action": string(taskstate.Chat)},
		}})
		return
	}
	var wf *workflow.Workflow
	if parsed, _, perr := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{}); perr == nil {
		wf = parsed
	}
	var defaults agent.Level
	if wf != nil {
		defaults = agent.Level{Agent: wf.Defaults.Agent, Model: wf.Defaults.Model, Effort: wf.Defaults.Effort}
	}
	sel := agent.Resolve(
		agent.Level{
			Agent:  strings.TrimSpace(ptrValue(req.Agent)),
			Model:  strings.TrimSpace(ptrValue(req.Model)),
			Effort: strings.TrimSpace(ptrValue(req.Effort)),
		},
		agent.Level{Agent: task.AgentOverride, Model: task.ModelOverride, Effort: task.EffortOverride},
		defaults,
	)
	name := sel.Agent
	if name == "" && s.deps.Agents != nil {
		for _, a := range s.deps.Agents.All() {
			if agent.CanResume(a) {
				name = a.Name()
				break
			}
		}
	}
	if s.deps.Agents == nil {
		s.internalError(w, "task chat", errors.New("no agent registry is configured"))
		return
	}
	adapter, ok := s.deps.Agents.Get(name)
	if !ok {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("unknown agent %q", name))
		return
	}
	if !agent.CanResume(adapter) {
		writeError(w, http.StatusBadRequest, CodeAgentCannotResume, fmt.Sprintf(
			"agent %q cannot resume its own session, so it cannot hold a conversation", name))
		return
	}
	// A repair's permission rule (025 decision 7): the workflow's defaults,
	// full-auto as the fallback, and the task's `restricted` clamp on top —
	// a task whose workflow says restricted does not get a full-auto chat.
	permission := agent.FullAuto
	if wf.ClampedPermissionMode(workflow.Step{}, task.Restricted) == workflow.PermissionRestricted {
		permission = agent.Restricted
	}
	if permission == agent.Restricted && !agent.CanResumeRestricted(adapter) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf(
			"task %d runs restricted, and agent %q cannot keep a resumed turn restricted; "+
				"choose an agent that can", id, name))
		return
	}
	opening, err := s.deps.Runner.ChatContext(ctx, task)
	if err != nil {
		s.internalError(w, "assemble chat context", err)
		return
	}
	title := strings.TrimSpace(ptrValue(req.Title))
	if title == "" {
		title = task.Title
	}
	chat := &store.Chat{
		Title: title, Agent: name, Model: sel.Model, Effort: sel.Effort,
		PermissionMode: string(permission), OpeningContext: opening,
	}
	err = s.deps.Store.OpenLinkedChat(ctx, id, task.State, chat)
	switch {
	case errors.Is(err, store.ErrTaskHasNoWorktree):
		writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
			Code: CodeTaskHasNoWorktree, Message: err.Error(),
			Details: map[string]string{"state": string(task.State), "action": string(taskstate.Chat)},
		}})
		return
	case err != nil:
		s.writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, renderChat(chat))
}

// handleChatClose ends a linked chat (task 119). A live turn is cancelled
// first; the chat then moves `idle → closed` and the task's lock lifts. The
// worktree and branch are the task's and are not touched.
func (s *Server) handleChatClose(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if !chat.Linked() {
		writeConflict(w, "only a chat opened on a task can be closed; archive this one instead",
			map[string]string{"state": string(chat.State), "action": string(chatstate.Close)})
		return
	}
	if chatstate.Terminal(chat.State) {
		writeConflict(w, "this chat is already "+string(chat.State),
			map[string]string{"state": string(chat.State), "action": string(chatstate.Close)})
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	updated, err := s.deps.Chats.Close(r.Context(), chat.ID)
	switch {
	case errors.Is(err, store.ErrInvalidChatAction):
		fresh, gerr := s.deps.Store.GetChat(r.Context(), chat.ID)
		st := chat.State
		if gerr == nil {
			st = fresh.State
		}
		writeConflict(w, "this chat changed state while it was being closed",
			map[string]string{"state": string(st), "action": string(chatstate.Close)})
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, renderChat(updated))
}

// lockAwareActions is availableActions for a task that may be locked (task
// 119): `[cancel]` where cancel is legal and `[]` elsewhere while a chat is
// open.
func lockAwareActions(st store.TaskState, openChatID int64) []string {
	if openChatID == 0 {
		return availableActions(st)
	}
	out := []string{}
	for _, a := range taskstate.LockedActionsFrom(st) {
		out = append(out, string(a))
	}
	return out
}
