package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// A chat's shell passthrough (task 170, §5.5, §13.2): a human runs a command
// in the chat's workspace through the daemon, which owns the process. The
// family stays off the MCP surface with the rest of the chat routes — an
// agent has a shell of its own, and one that could mark output for the next
// prompt would be writing its own context.

// chatShellRunResponse is one shell run on the wire.
type chatShellRunResponse struct {
	ID          int64      `json:"id"`
	ChatID      int64      `json:"chat_id"`
	Command     string     `json:"command"`
	State       string     `json:"state"`
	ExitCode    *int       `json:"exit_code"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at"`
	OutputBytes int64      `json:"output_bytes"`
	Truncated   bool       `json:"truncated"`
	Attach      bool       `json:"attach"`
	// AttachedTurn is the seq of the turn that took the run's output, null
	// until one has.
	AttachedTurn *int `json:"attached_turn"`
	// AfterTurn is the seq of the last turn that existed when the run
	// started, 0 for none: where a client draws the run.
	AfterTurn int `json:"after_turn"`
}

func renderChatShellRun(run *store.ChatShellRun) chatShellRunResponse {
	return chatShellRunResponse{
		ID: run.ID, ChatID: run.ChatID, Command: run.Command, State: run.State,
		ExitCode: run.ExitCode, StartedAt: run.StartedAt.UTC(), EndedAt: run.EndedAt,
		OutputBytes: run.OutputBytes, Truncated: run.Truncated, Attach: run.Attach,
		AttachedTurn: run.AttachedTurn, AfterTurn: run.AfterTurn,
	}
}

type chatShellRequest struct {
	Command string `json:"command"`
}

// handleChatShellRun is POST /v1/chats/{id}/shell: start a command and
// answer 202 with its row, `running`. A terminal chat, and a chat that
// already has a command running, are 409 invalid_state.
func (s *Server) handleChatShellRun(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	var req chatShellRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "command is required")
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	run, err := s.deps.Chats.RunShell(r.Context(), chat.ID, req.Command)
	switch {
	case errors.Is(err, store.ErrInvalidChatAction):
		writeConflict(w, fmt.Sprintf("a %s chat cannot run a shell command", chat.State),
			map[string]string{"state": string(chat.State), "action": "shell"})
	case errors.Is(err, store.ErrChatShellBusy):
		writeConflict(w, "this chat already has a shell command running",
			map[string]string{"state": string(chat.State), "action": "shell"})
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, renderChatShellRun(run))
	}
}

// handleChatShellList is GET /v1/chats/{id}/shell: the chat's runs, oldest
// first. It is a read and legal in every chat state.
func (s *Server) handleChatShellList(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	runs, err := s.deps.Store.ListChatShellRuns(r.Context(), chat.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	out := make([]chatShellRunResponse, 0, len(runs))
	for _, run := range runs {
		out = append(out, renderChatShellRun(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// chatShellRunFromPath resolves {id} and {run_id}, answering 400 and 404
// itself.
func (s *Server) chatShellRunFromPath(w http.ResponseWriter, r *http.Request) (*store.Chat, *store.ChatShellRun, bool) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return nil, nil, false
	}
	id, err := strconv.ParseInt(r.PathValue("run_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "shell run id must be an integer")
		return nil, nil, false
	}
	run, err := s.deps.Store.GetChatShellRun(r.Context(), chat.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("chat %d has no shell run %d", chat.ID, id))
		return nil, nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return nil, nil, false
	}
	return chat, run, true
}

// handleChatShellGet is GET /v1/chats/{id}/shell/{run_id}.
func (s *Server) handleChatShellGet(w http.ResponseWriter, r *http.Request) {
	if _, run, ok := s.chatShellRunFromPath(w, r); ok {
		writeJSON(w, http.StatusOK, renderChatShellRun(run))
	}
}

// handleChatShellOutput is GET /v1/chats/{id}/shell/{run_id}/output: the
// stored output as text/plain — capped, and ending in the truncation marker
// when the cap was reached. A running command's output is what has been
// written so far. A run that never opened its file answers an empty body.
func (s *Server) handleChatShellOutput(w http.ResponseWriter, r *http.Request) {
	_, run, ok := s.chatShellRunFromPath(w, r)
	if !ok {
		return
	}
	var data []byte
	if run.OutputPath != "" {
		var err error
		if data, err = os.ReadFile(run.OutputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

type chatShellAttachRequest struct {
	Attach *bool `json:"attach"`
}

// handleChatShellAttach is POST /v1/chats/{id}/shell/{run_id}/attach: mark,
// or unmark, a finished run for the chat's next prompt. A run still running,
// one a turn already took, and a terminal chat are 409 invalid_state.
func (s *Server) handleChatShellAttach(w http.ResponseWriter, r *http.Request) {
	chat, run, ok := s.chatShellRunFromPath(w, r)
	if !ok {
		return
	}
	var req chatShellAttachRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Attach == nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "attach is required")
		return
	}
	out, err := s.deps.Store.SetChatShellAttach(r.Context(), chat.ID, run.ID, *req.Attach)
	switch {
	case errors.Is(err, store.ErrChatShellNotAttachable):
		msg := "this shell run is still running"
		if run.AttachedTurn != nil {
			msg = fmt.Sprintf("turn %d already took this shell run", *run.AttachedTurn)
		}
		writeConflict(w, msg, map[string]string{"state": run.State, "action": "attach"})
	case errors.Is(err, store.ErrInvalidChatAction):
		writeConflict(w, fmt.Sprintf("a %s chat has no next prompt to attach to", chat.State),
			map[string]string{"state": string(chat.State), "action": "attach"})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
	default:
		writeJSON(w, http.StatusOK, renderChatShellRun(out))
	}
}
