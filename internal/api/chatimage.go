package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/store"
)

// A chat's image attachments (task 170, §5.5, §13.2). The upload is the one
// route whose request body is not JSON: it is the image's bytes, and the
// daemon decides what they are from the bytes alone.

// chatImageResponse is one stored image on the wire.
type chatImageResponse struct {
	Number    int    `json:"number"`
	Chip      string `json:"chip"`
	MediaType string `json:"media_type"`
	Bytes     int64  `json:"bytes"`
	Name      string `json:"name,omitempty"`
}

func renderChatImage(img *store.ChatImage) chatImageResponse {
	return chatImageResponse{
		Number: img.Number, Chip: chatrun.ImageChip(img.Number), MediaType: img.MediaType,
		Bytes: img.Bytes, Name: img.Name,
	}
}

// handleChatImageAdd is POST /v1/chats/{id}/images: store the request body
// as the chat's next image and answer 201 with its number and chip. `name`
// in the query is the original file name, kept for display only. Anything
// that is not a PNG, JPEG, GIF or WebP by content, anything over 10 MiB, and
// a chat whose agent cannot be given images are 422 validation_failed; a
// terminal chat is 409 invalid_state.
func (s *Server) handleChatImageAdd(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	// One byte past the limit is read so "exactly the limit" and "over it"
	// are told apart without trusting Content-Length.
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, chatrun.MaxImageBytes+1))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || len(data) > chatrun.MaxImageBytes {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed,
			fmt.Sprintf("the image is larger than %d MiB", chatrun.MaxImageBytes>>20))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "the image could not be read: "+err.Error())
		return
	}
	img, err := s.deps.Chats.AddImage(r.Context(), chat, r.URL.Query().Get("name"), data)
	if msg, refused := chatrun.IsImageRefusal(err); refused {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, msg)
		return
	}
	switch {
	case errors.Is(err, store.ErrInvalidChatAction):
		writeConflict(w, fmt.Sprintf("a %s chat cannot take an image", chat.State),
			map[string]string{"state": string(chat.State), "action": "image"})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
	default:
		writeJSON(w, http.StatusCreated, renderChatImage(img))
	}
}

// handleChatImageList is GET /v1/chats/{id}/images: the chat's images in
// number order. A read, legal in every chat state.
func (s *Server) handleChatImageList(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	images, err := s.deps.Store.ListChatImages(r.Context(), chat.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	out := make([]chatImageResponse, 0, len(images))
	for i := range images {
		out = append(out, renderChatImage(&images[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": out})
}

// checkChatImages refuses a message whose `[image N]` chips cannot be
// resolved for the chat's agent — an image the chat does not have, or an
// agent that cannot be given one — with 422 validation_failed, before the
// message becomes a turn or a queued row. It reports whether the send may
// go on.
func (s *Server) checkChatImages(w http.ResponseWriter, r *http.Request, chat *store.Chat, message string) bool {
	_, err := s.deps.Chats.ResolveImages(r.Context(), chat, message)
	if err == nil {
		return true
	}
	if msg, refused := chatrun.IsImageRefusal(err); refused {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, msg)
		return false
	}
	writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
	return false
}
