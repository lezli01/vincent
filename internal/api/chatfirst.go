package api

// A chat started from its first message, and a chat renamed (task 161, spec
// §5.5, §13.2): POST /v1/chats and POST /v1/tasks/{id}/chat take an optional
// `message` sent as turn 1 in the same request, an empty title is derived from
// that message, and PATCH /v1/chats/{id} renames (its handler, shared with
// task 162's settings, is in chatsettings.go).

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/chatrun"
)

// ChatTitleMaxRunes bounds a chat title derived from a message, and a rename
// (task 161 decision 2): about one line of a card, and a branch slug that
// stays readable.
const ChatTitleMaxRunes = 72

// chatCreateBody is a create's 201: the chat, plus the first turn the request
// started — null when no message was given or the send failed (decision 1).
type chatCreateBody struct {
	chatBody
	FirstTurn *chatTurnBody `json:"first_turn"`
}

// DeriveChatTitle is the one title rule every chat-creating route uses (task
// 161 decision 2): a non-empty title, trimmed, wins; otherwise the first
// non-empty line of message, with its whitespace collapsed, cut at a word
// boundary to ChatTitleMaxRunes with `…` when cut. Both empty is "".
func DeriveChatTitle(title, message string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	for line := range strings.SplitSeq(message, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 {
			continue
		}
		return cutTitle(strings.Join(words, " "))
	}
	return ""
}

// cutTitle bounds s to ChatTitleMaxRunes, cutting at the last word boundary
// that fits — or mid-word when one word alone is longer — and marking the cut.
func cutTitle(s string) string {
	if utf8.RuneCountInString(s) <= ChatTitleMaxRunes {
		return s
	}
	runes := []rune(s)
	keep := runes[:ChatTitleMaxRunes-1] // room for the ellipsis
	if !unicode.IsSpace(runes[ChatTitleMaxRunes-1]) {
		if i := strings.LastIndexFunc(string(keep), unicode.IsSpace); i > 0 {
			keep = []rune(string(keep)[:i])
		}
	}
	return strings.TrimRightFunc(string(keep), unicode.IsSpace) + "…"
}

// sendFirstTurn starts turn 1 of a just-created chat (decision 1). A refusal
// never undoes the create: the chat is kept, and the reason comes back as a
// warning for the 201 instead of an error status.
func (s *Server) sendFirstTurn(ctx context.Context, chatID int64, message string) (*chatTurnBody, string) {
	if message == "" {
		return nil, ""
	}
	notSent := "the first message was not sent: "
	if s.deps.Chats == nil {
		return nil, notSent + "no chat runner is wired"
	}
	turn, err := s.deps.Chats.Send(ctx, chatID, message)
	switch {
	case errors.Is(err, chatrun.ErrChatCapReached):
		return nil, notSent + CodeChatCapReached + ": " + err.Error()
	case err != nil:
		s.deps.Logger.Warn("send a new chat's first message", "chat", chatID, "error", err)
		return nil, notSent + err.Error()
	}
	body := renderChatTurn(turn)
	return &body, ""
}

// writeChatCreated answers a create: the chat, the first turn when message
// started one, and any warning the send left.
func (s *Server) writeChatCreated(ctx context.Context, w http.ResponseWriter, body chatBody, message string) {
	turn, warning := s.sendFirstTurn(ctx, body.ID, message)
	if warning != "" {
		body.Warnings = append(body.Warnings, warning)
	}
	if turn != nil {
		// The send moved the chat to running; the 201 says so rather than
		// showing the idle row the create wrote.
		if fresh, err := s.deps.Store.GetChat(ctx, body.ID); err == nil {
			warnings := body.Warnings
			body = renderChat(fresh)
			body.Warnings = warnings
		}
	}
	writeJSON(w, http.StatusCreated, chatCreateBody{chatBody: body, FirstTurn: turn})
}
