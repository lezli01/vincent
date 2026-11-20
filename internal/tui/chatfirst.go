package tui

// A chat started from its first message (task 156, §15): the new-chat form
// and the chat-with form both send the message in the create, and a send the
// daemon refused puts the message back in the composer rather than losing it.

import (
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
)

// unsentFirstMessage is the draft and note to open a just-created chat with:
// empty when no message was given or the daemon started its turn, and the
// message with the daemon's reason when the chat was created but the message
// not sent (task 156 decision 1).
func unsentFirstMessage(chat *apiclient.Chat, message string) (draft, note string) {
	if chat == nil || message == "" || chat.FirstTurn != nil {
		return "", ""
	}
	note = "the first message was not sent — it is back in the composer"
	if len(chat.Warnings) > 0 {
		note = strings.Join(chat.Warnings, "; ") + " — the message is back in the composer"
	}
	return message, note
}

// restoreDraft puts an unsent first message back into the composer and says
// why. It runs after open, which clears both.
func (v *chatView) restoreDraft(draft, note string) {
	if draft != "" {
		v.composer.SetValue(draft)
	}
	if note != "" {
		v.note, v.noteBad = note, true
	}
}
