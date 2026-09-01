package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// CodeAgentCannotResume is the typed refusal a chat on an adapter that cannot
// resume its own session gets (§9.1, §9.3, §9.7; task 063 decision 3). It is
// its own code rather than a generic validation failure because the client's
// remedy is specific and nameable: pick an adapter that can, and do not expect
// the conversation to be reconstructed some other way.
const CodeAgentCannotResume = "agent_cannot_resume"

// CodeChatCapReached is a turn refused because `max_parallel_chats` chats are
// already running (§11, decision 1). It is a 409 and never a queue.
const CodeChatCapReached = "chat_cap_reached"

// CodeRepoOperationInProgress is a handoff refused because the chat's worktree
// is partway through a git operation — a conflicted merge, a stopped rebase, a
// cherry-pick awaiting a commit (task 074 decision 4). It is its own code
// because the remedy is specific and the details name the operation: finish or
// abort it, then hand off.
//
// Ordinary uncommitted work is *not* refused. Preserving it is the feature.
const CodeRepoOperationInProgress = worktree.ReasonRepoOperationInProgress

// chatBody is a chat as the API renders it (§5.5, §13.2).
type chatBody struct {
	ID           int64           `json:"id"`
	ProjectID    int64           `json:"project_id"`
	Title        string          `json:"title"`
	State        string          `json:"state"`
	Agent        string          `json:"agent"`
	Model        string          `json:"model,omitempty"`
	Effort       string          `json:"effort,omitempty"`
	Branch       string          `json:"branch"`
	BaseBranch   string          `json:"base_branch"`
	BaseSHA      string          `json:"base_sha,omitempty"`
	WorktreePath string          `json:"worktree_path,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	PendingInput json.RawMessage `json:"pending_input,omitempty"`
	// HandoffTaskID is the task this chat's worktree and branch were handed
	// to (§5.5, task 074). It is the one authoritative edge; a task's
	// `source_chat_id` is this read backwards.
	HandoffTaskID *int64    `json:"handoff_task_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// chatTurnBody is one turn as the API renders it.
type chatTurnBody struct {
	ID           int64      `json:"id"`
	ChatID       int64      `json:"chat_id"`
	Seq          int        `json:"seq"`
	Prompt       string     `json:"prompt"`
	State        string     `json:"state"`
	FailReason   string     `json:"fail_reason,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	ResultText   string     `json:"result_text,omitempty"`
	SessionID    string     `json:"session_id,omitempty"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	CostUSD      *float64   `json:"cost_usd"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	DurationMS   *int64     `json:"duration_ms,omitempty"`
}

func renderChat(c *store.Chat) chatBody {
	return chatBody{
		ID: c.ID, ProjectID: c.ProjectID, Title: c.Title, State: string(c.State),
		Agent: c.Agent, Model: c.Model, Effort: c.Effort, Branch: c.Branch,
		BaseBranch: c.BaseBranch, BaseSHA: c.BaseSHA, WorktreePath: c.WorktreePath,
		SessionID: c.SessionID, PendingInput: c.PendingInput, HandoffTaskID: c.HandoffTaskID,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func renderChatTurn(t *store.ChatTurn) chatTurnBody {
	return chatTurnBody{
		ID: t.ID, ChatID: t.ChatID, Seq: t.Seq, Prompt: t.Prompt, State: string(t.State),
		FailReason: t.FailReason, ErrorMessage: t.ErrorMessage, ResultText: t.ResultText,
		SessionID: t.SessionID, InputTokens: t.InputTokens, OutputTokens: t.OutputTokens,
		CostUSD: t.CostUSD, ExitCode: t.ExitCode, StartedAt: t.StartedAt,
		EndedAt: t.EndedAt, DurationMS: t.DurationMS,
	}
}

type createChatRequest struct {
	ProjectID  int64  `json:"project_id"`
	Title      string `json:"title"`
	Agent      string `json:"agent"`
	Model      string `json:"model"`
	Effort     string `json:"effort"`
	BaseBranch string `json:"base_branch"`
}

// handleChatCreate creates a chat, allocating its worktree and
// `vincent/{id}-{slug}` branch exactly as a task's is (§10).
//
// The adapter is checked for resume support *before* anything is written: a
// chat on an adapter that cannot resume would be a conversation that silently
// forgets itself, and refusing it here is the same shape as §9.4's restricted
// refusal (decision 3).
func (s *Server) handleChatCreate(w http.ResponseWriter, r *http.Request) {
	var req createChatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "title is required")
		return
	}
	project, err := s.deps.Store.GetProject(r.Context(), req.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("project %d not found", req.ProjectID))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	// An omitted agent resolves to the first registered adapter that can
	// resume, rather than to a configured default: there is no
	// `defaults.agent` key — a task's agent comes from its workflow — and a
	// chat's whole premise is continuity, so "any adapter that can hold a
	// conversation" is the only default that is not a trap.
	name := req.Agent
	if name == "" {
		for _, a := range s.deps.Agents.All() {
			if agent.CanResume(a) {
				name = a.Name()
				break
			}
		}
	}
	adapter, ok := s.deps.Agents.Get(name)
	if !ok {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("unknown agent %q", name))
		return
	}
	if !agent.CanResume(adapter) {
		writeError(w, http.StatusBadRequest, CodeAgentCannotResume, fmt.Sprintf(
			"agent %q cannot resume its own session, so it cannot hold a conversation; "+
				"vincent refuses this rather than replaying the log as prompt context", name))
		return
	}
	base := req.BaseBranch
	if base == "" {
		base = project.DefaultBranch
	}
	chat := &store.Chat{
		ProjectID: project.ID, Title: req.Title, State: chatstate.Idle, Agent: name,
		Model: req.Model, Effort: req.Effort, PermissionMode: string(agent.FullAuto),
		BaseBranch: base,
	}
	if err := s.deps.Store.CreateChat(r.Context(), chat); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	// The branch name needs the id, and the id needs the row: the chat is
	// written first and named second, exactly as a task is.
	chat.Branch = worktree.BranchName(chat.ID, chat.Title)
	if err := s.deps.Store.SetChatBranch(r.Context(), chat.ID, chat.Branch); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	created, err := s.deps.Worktrees.CreateAndClaim(
		r.Context(), project.Path, worktree.ChatOwner(chat.ID), chat.Branch, base, true,
		func(c worktree.Created) error {
			_, err := s.deps.Store.SetChatWorktree(r.Context(), chat.ID, c.Path, c.BaseSHA)
			return err
		})
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	chat.WorktreePath, chat.BaseSHA = created.Path, created.BaseSHA
	writeJSON(w, http.StatusCreated, renderChat(chat))
}

// handleChatList lists chats. They are here and nowhere else: chats never
// appear in GET /v1/tasks, which is the whole point of the separate entity.
func (s *Server) handleChatList(w http.ResponseWriter, r *http.Request) {
	var f store.ChatFilter
	if v := r.URL.Query().Get("project_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "project_id must be an integer")
			return
		}
		f.ProjectID = &id
	}
	for _, st := range r.URL.Query()["state"] {
		if !chatstate.Valid(chatstate.State(st)) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("unknown chat state %q", st))
			return
		}
		f.States = append(f.States, chatstate.State(st))
	}
	chats, err := s.deps.Store.ListChats(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	out := make([]chatBody, 0, len(chats))
	for i := range chats {
		out = append(out, renderChat(&chats[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": out})
}

// handleChatGet returns one chat with its turns — the whole conversation, in
// order, which is what a pane renders.
func (s *Server) handleChatGet(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	turns, err := s.deps.Store.ListChatTurns(r.Context(), chat.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	body := make([]chatTurnBody, 0, len(turns))
	for i := range turns {
		body = append(body, renderChatTurn(&turns[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat": renderChat(chat), "turns": body})
}

type sendChatRequest struct {
	Message string `json:"message"`
}

// handleChatSend starts a turn. Over the cap it is refused with 409 and never
// queued: a chat turn is a foreground reply and must not wait behind batch
// work (decision 1).
func (s *Server) handleChatSend(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	var req sendChatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "message is required")
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	turn, err := s.deps.Chats.Send(r.Context(), chat.ID, req.Message)
	switch {
	case errors.Is(err, chatrun.ErrChatCapReached):
		writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
			Code: CodeChatCapReached, Message: err.Error(),
			Details: map[string]string{"state": string(chat.State)},
		}})
		return
	case errors.Is(err, store.ErrInvalidChatAction):
		writeConflict(w, err.Error(), map[string]string{"state": string(chat.State), "action": "send"})
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, renderChatTurn(turn))
}

// handleChatAnswer answers the chat's pending §7.4 request. It is the same
// flow a task's answer takes, down to the adapter's Respond (decision 8).
func (s *Server) handleChatAnswer(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if chat.State != chatstate.AwaitingInput {
		writeConflict(w, "this chat is not waiting for an answer", map[string]string{"state": string(chat.State), "action": "answer"})
		return
	}
	var resp agent.InputResponse
	if !decodeJSON(w, r, &resp) {
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	if err := s.deps.Chats.Answer(r.Context(), chat.ID, resp); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleChatCancel stops a live turn.
func (s *Server) handleChatCancel(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if !chatstate.Allowed(chat.State, chatstate.Cancel) {
		writeConflict(w, "this chat has no live turn", map[string]string{"state": string(chat.State), "action": "cancel"})
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	if err := s.deps.Chats.Cancel(r.Context(), chat.ID); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleChatArchive archives a chat: the worktree goes and the branch may go
// with it, exactly as task 008 archives a task (§10). A dirty worktree is
// refused unless `force` is set — the same refusal, and the same way out.
func (s *Server) handleChatArchive(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if !chatstate.Allowed(chat.State, chatstate.Archive) {
		writeConflict(w, "a chat with a live turn cannot be archived", map[string]string{"state": string(chat.State), "action": "archive"})
		return
	}
	force := r.URL.Query().Get("force") == "true"
	project, err := s.deps.Store.GetProject(r.Context(), chat.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if chat.WorktreePath != "" {
		err = s.deps.Worktrees.RemoveAndRelease(r.Context(), project.Path, chat.WorktreePath, force,
			func() error {
				_, err := s.deps.Store.SetChatWorktree(r.Context(), chat.ID, "", "")
				return err
			})
		var wtErr *worktree.Error
		if errors.As(err, &wtErr) {
			writeConflict(w, wtErr.Error(), map[string]string{"state": string(chat.State), "action": "archive"})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
			return
		}
	}
	updated, err := s.deps.Store.SetChatState(r.Context(), chat.ID, chatstate.Archived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, renderChat(updated))
}

// chatFromPath resolves {id}, answering 400 or 404 itself.
func (s *Server) chatFromPath(w http.ResponseWriter, r *http.Request) (*store.Chat, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "chat id must be an integer")
		return nil, false
	}
	chat, err := s.deps.Store.GetChat(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("chat %d not found", id))
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return nil, false
	}
	return chat, true
}
