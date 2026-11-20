package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/store"
)

// chatQueuedBody is one follow-up message waiting behind a chat's live turn
// (task 156, §5.5, §13.2).
type chatQueuedBody struct {
	ID       int64  `json:"id"`
	ChatID   int64  `json:"chat_id"`
	Position int    `json:"position"`
	Prompt   string `json:"prompt"`
	// Model, Effort and PermissionMode are the message's per-turn overrides
	// (task 160 decision 6); absent is "the chat's setting at drain time".
	Model          string    `json:"model,omitempty"`
	Effort         string    `json:"effort,omitempty"`
	PermissionMode string    `json:"permission_mode,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func renderChatQueued(m *store.ChatQueuedMessage) chatQueuedBody {
	return chatQueuedBody{
		ID: m.ID, ChatID: m.ChatID, Position: m.Position, Prompt: m.Prompt,
		Model: m.Settings.Model, Effort: m.Settings.Effort, PermissionMode: m.Settings.PermissionMode,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// chatQueueBody is GET /v1/chats/{id}/queue: the messages in the order they
// will run, and the pause that holds them.
type chatQueueBody struct {
	Messages    []chatQueuedBody `json:"messages"`
	Paused      bool             `json:"paused"`
	PauseReason string           `json:"pause_reason,omitempty"`
}

func (s *Server) writeChatQueue(w http.ResponseWriter, r *http.Request, chatID int64) {
	chat, err := s.deps.Store.GetChat(r.Context(), chatID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	msgs, err := s.deps.Store.ListChatQueue(r.Context(), chatID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	body := chatQueueBody{
		Messages: make([]chatQueuedBody, 0, len(msgs)),
		Paused:   chat.QueuePaused, PauseReason: chat.QueuePauseReason,
	}
	for i := range msgs {
		body.Messages = append(body.Messages, renderChatQueued(&msgs[i]))
	}
	writeJSON(w, http.StatusOK, body)
}

// handleChatQueueList is GET /v1/chats/{id}/queue.
func (s *Server) handleChatQueueList(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	s.writeChatQueue(w, r, chat.ID)
}

// queuedIDFromPath parses {qid}.
func queuedIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("qid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "queued message id must be an integer")
		return 0, false
	}
	return id, true
}

// writeQueueGone answers an edit or delete of a message that already started
// (it is a turn now) or was removed: 409, because the caller's view of the
// queue is stale rather than its id wrong.
func writeQueueGone(w http.ResponseWriter, err error, action string) {
	writeConflict(w, err.Error(), map[string]string{"action": action})
}

type editQueuedRequest struct {
	Message string `json:"message"`
	// Any of the three present replaces the message's overrides with the
	// three given (task 160 decision 6); all absent leaves them alone.
	chatSettingsRequest
}

// handleChatQueueEdit is PATCH /v1/chats/{id}/queue/{qid}: replace a queued
// message's text before it starts.
func (s *Server) handleChatQueueEdit(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	qid, ok := queuedIDFromPath(w, r)
	if !ok {
		return
	}
	var req editQueuedRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Message == "" && req.empty() {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "message is required")
		return
	}
	var ov *store.TurnSettings
	if !req.empty() {
		settings := req.turnSettings()
		if !s.checkTurnOverrides(w, r, chat, settings) {
			return
		}
		ov = &settings
	}
	m, err := s.deps.Store.EditChatQueuedWith(r.Context(), chat.ID, qid, req.Message, ov)
	switch {
	case errors.Is(err, store.ErrQueuedMessageGone):
		writeQueueGone(w, err, "edit")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, renderChatQueued(m))
}

// handleChatQueueDelete is DELETE /v1/chats/{id}/queue/{qid}.
func (s *Server) handleChatQueueDelete(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	qid, ok := queuedIDFromPath(w, r)
	if !ok {
		return
	}
	err := s.deps.Store.DeleteChatQueued(r.Context(), chat.ID, qid)
	switch {
	case errors.Is(err, store.ErrQueuedMessageGone):
		writeQueueGone(w, err, "delete")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleChatQueueResume is POST /v1/chats/{id}/queue/resume: lift the pause a
// failed, interrupted or cancelled turn left (task 156 decision 3). On an idle
// chat the oldest message starts at once — a new turn, so the cap applies to
// it exactly as to a send.
func (s *Server) handleChatQueueResume(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	_, err := s.deps.Chats.ResumeQueue(r.Context(), chat.ID)
	switch {
	case errors.Is(err, chatrun.ErrChatCapReached):
		writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
			Code: CodeChatCapReached, Message: err.Error(),
			Details: map[string]string{"state": string(chat.State)},
		}})
		return
	case errors.Is(err, store.ErrInvalidChatAction):
		writeConflict(w, err.Error(), map[string]string{"state": string(chat.State), "action": "resume_queue"})
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	s.writeChatQueue(w, r, chat.ID)
}

// handleChatQueueClear is POST /v1/chats/{id}/queue/clear: delete every
// queued message and lift the pause.
func (s *Server) handleChatQueueClear(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if err := s.deps.Store.ClearChatQueue(r.Context(), chat.ID); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
