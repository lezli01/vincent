package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// A chat's draft and stash, and the project's prompt history (task 162,
// #1012, spec §5.5, §13.2). None of these routes is an MCP tool: decision row
// 29 keeps the chat family off that surface whole.

// chatDraftBody is GET and PUT /v1/chats/{id}/draft's answer.
type chatDraftBody struct {
	Draft string `json:"draft"`
	Stash string `json:"stash"`
	// UpdatedAt is when either was last written; null before the first.
	UpdatedAt *time.Time `json:"updated_at"`
}

func renderChatDraft(c *store.Chat) chatDraftBody {
	return chatDraftBody{Draft: c.Draft, Stash: c.Stash, UpdatedAt: c.DraftUpdatedAt}
}

// putChatDraftRequest carries either field or both; an absent one is left as
// it is (task 162).
type putChatDraftRequest struct {
	Draft *string `json:"draft"`
	Stash *string `json:"stash"`
}

// handleChatDraftGet serves GET /v1/chats/{id}/draft.
func (s *Server) handleChatDraftGet(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, renderChatDraft(chat))
}

// handleChatDraftPut serves PUT /v1/chats/{id}/draft: last writer wins, and a
// write publishes chat.draft_changed. A terminal chat answers 409 — its draft
// was cleared when it ended.
func (s *Server) handleChatDraftPut(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	var req putChatDraftRequest
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if req.Draft == nil && req.Stash == nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "draft or stash is required")
		return
	}
	for name, v := range map[string]*string{"draft": req.Draft, "stash": req.Stash} {
		if v == nil {
			continue
		}
		if msg := boundString(name, *v, store.MaxChatDraftBytes); msg != "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
			return
		}
	}
	out, err := s.deps.Store.UpdateChatDraft(r.Context(), chat.ID, req.Draft, req.Stash)
	switch {
	case errors.Is(err, store.ErrInvalidChatAction):
		writeConflict(w, "this chat has ended and keeps no draft", map[string]string{"state": string(chat.State), "action": "draft"})
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, renderChatDraft(out))
}

// chatPromptBody is one prompt GET /v1/chats/prompts lists.
type chatPromptBody struct {
	Prompt    string    `json:"prompt"`
	ChatID    int64     `json:"chat_id"`
	ChatTitle string    `json:"chat_title"`
	ChatState string    `json:"chat_state"`
	Seq       int       `json:"seq"`
	SentAt    time.Time `json:"sent_at"`
}

// handleChatPrompts serves GET /v1/chats/prompts?project_id=&q=&limit=
// (task 162 decisions 5 and 6): every prompt the project's chats have sent,
// archived ones included, newest first and de-duplicated by text. A q of
// three characters or more narrows by substring on chat search's trigram
// index; a shorter or empty one lists by recency rather than answering
// `query_too_short` as GET /v1/chats/search does, so a picker always has
// something to show.
func (s *Server) handleChatPrompts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pid, err := strconv.ParseInt(q.Get("project_id"), 10, 64)
	if err != nil || pid < 1 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "project_id is required and must be a positive integer")
		return
	}
	limit := 0
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 0 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "limit must be a non-negative integer")
			return
		}
	}
	prompts, err := s.deps.Store.ListChatPrompts(r.Context(), pid, q.Get("q"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	out := make([]chatPromptBody, 0, len(prompts))
	for _, p := range prompts {
		out = append(out, chatPromptBody{
			Prompt: p.Prompt, ChatID: p.ChatID, ChatTitle: p.ChatTitle, ChatState: string(p.ChatState),
			Seq: p.Seq, SentAt: p.SentAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompts": out})
}
