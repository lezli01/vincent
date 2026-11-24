package tui

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The open's load and the chat's event stream start together, so a turn that
// ends between the load's answer and the stream's connect sends its event to
// nobody. The first connect after an open re-reads the chat to cover that
// gap; a later reconnect resumes from its cursor and reads nothing.
func TestChatStreamFirstConnectReloads(t *testing.T) {
	v := chatViewFixture()
	v.client = offlineClient()
	v.chatID = 1
	v.streamUp = false
	before := v.loadSeq
	v.applyChatNote(chatNoteMsg{chatID: 1, note: apiclient.ConnectedNote{}})
	if v.loadSeq == before {
		t.Fatal("the first connect after an open did not re-read the chat")
	}
	again := v.loadSeq
	v.applyChatNote(chatNoteMsg{chatID: 1, note: apiclient.ConnectedNote{}})
	if v.loadSeq != again {
		t.Fatal("a resuming reconnect re-read the chat")
	}
	v.applyChatNote(chatNoteMsg{chatID: 1, note: apiclient.ConnectedNote{Reset: true}})
	if v.loadSeq == again {
		t.Fatal("a reset reconnect did not re-read the chat")
	}
}
