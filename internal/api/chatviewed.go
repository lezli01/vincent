package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// handleChatViewed is POST /v1/chats/{id}/viewed (task 165): a person looked
// at the chat, so its unread mark clears — in every client, since there is
// one last-viewed value per chat. The time is the daemon's clock, never the
// client's: the mark is compared against turn end times the daemon wrote, and
// a client clock running fast would hide a turn that finished after the view.
// The store keeps it monotonic, so a late request is harmless. It is legal in
// every chat state — looking at an archived chat is still looking at it.
func (s *Server) handleChatViewed(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "chat id must be an integer")
		return
	}
	chat, err := s.deps.Store.MarkChatViewed(r.Context(), id, time.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("chat %d not found", id))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, renderChat(chat))
}
