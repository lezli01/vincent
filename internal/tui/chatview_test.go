package tui

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// chatViewFixture is a chat workspace pointed at one loaded chat.
func chatViewFixture() *chatView {
	v := newChatView(newLevelHolder(), newRawHolder())
	v.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	v.chatID = 1
	v.chat = &apiclient.Chat{ID: 1, ProjectID: 7, Title: "a chat", State: "idle", Agent: "claude"}
	v.composer.Focus()
	return v
}

// TestChatViewRefusesCapReached holds §11's refusal: a 409 renders as a
// refusal, never as a queued turn and never as a spinner.
func TestChatViewRefusesCapReached(t *testing.T) {
	v := chatViewFixture()
	v.applySent(chatSentMsg{chatID: 1, err: &apiclient.Error{Code: "chat_cap_reached"}})
	if !v.noteBad || v.note == "" {
		t.Fatalf("a cap refusal rendered as %q (bad=%v), want a refusal", v.note, v.noteBad)
	}
	if len(v.turns) != 0 {
		t.Fatalf("a refused send created %d turn rows, want none", len(v.turns))
	}
}

// TestChatViewSeamsTranscriptToStream is the catch-up seam: a chunk at or
// before the fetch's X-Next-Offset is one the fetch already returned, and
// printing it again would duplicate the line.
//
// The chunks carry §13.3's normalized fields, which is what the daemon
// publishes for a chat since task 071 — the payload is the same vocabulary a
// step's chunks use, not the agent's verbatim line.
func TestChatViewSeamsTranscriptToStream(t *testing.T) {
	v := chatViewFixture()
	v.turns = []apiclient.ChatTurn{{ID: 9, Seq: 1, State: "running"}}
	v.applyTranscript(chatTranscriptMsg{chatID: 1, seq: 1, next: 120, records: []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "one"},
	}})
	if got := len(v.turnRecords[1]); got != 1 {
		t.Fatalf("the fetch produced %d records, want 1", got)
	}
	// Already covered by the fetch: dropped.
	v.applyChatNote(chatNoteMsg{chatID: 1, note: apiclient.OutputNote{
		Type: "agent.output", TurnID: 9, Offset: 120,
		Payload: []byte(`{"text":"one","raw":"{}"}`),
	}})
	if got := len(v.turnRecords[1]); got != 1 {
		t.Fatalf("a chunk at the seam was recorded again: %v", v.turnRecords[1])
	}
	// Past the seam: new.
	v.applyChatNote(chatNoteMsg{chatID: 1, note: apiclient.OutputNote{
		Type: "agent.output", TurnID: 9, Offset: 121,
		Payload: []byte(`{"text":"two","raw":"{}"}`),
	}})
	if got := len(v.turnRecords[1]); got != 2 {
		t.Fatalf("a chunk past the seam was dropped: %v", v.turnRecords[1])
	}
}

// TestChatViewOpensTheSameAnswerPopup proves the §7.4 popup is reused rather
// than forked: the chat's own pending request builds an answerForm, and the
// answer goes out over AnswerChat.
func TestChatViewOpensTheSameAnswerPopup(t *testing.T) {
	v := chatViewFixture()
	v.applyLoaded(chatLoadedMsg{id: 1, chat: &apiclient.Chat{
		ID: 1, State: "awaiting_input",
		PendingInput: []byte(`{"kind":"question","questions":[` +
			`{"text":"Which files?","options":["a.go","b.go"],"multi_select":true}]}`),
	}})
	if v.form == nil {
		t.Fatal("an awaiting_input chat did not open the answer popup")
	}
	if len(v.form.req.Questions) != 1 || !v.form.req.Questions[0].MultiSelect {
		t.Fatalf("the popup lost the request's shape: %+v", v.form.req)
	}
	// Answering elsewhere closes it: the popup follows the daemon's state.
	v.applyLoaded(chatLoadedMsg{id: 1, chat: &apiclient.Chat{ID: 1, State: "running"}})
	if v.form != nil {
		t.Fatal("the popup outlived the awaiting_input state")
	}
}

// TestChatViewIgnoresAnotherChatsStream holds the per-chat filter on the
// client side too: a note from a subscription we left changes nothing.
func TestChatViewIgnoresAnotherChatsStream(t *testing.T) {
	v := chatViewFixture()
	if cmd := v.applyChatNote(chatNoteMsg{chatID: 2, note: apiclient.OutputNote{}}); cmd != nil {
		t.Fatal("a stale subscription's note was folded in")
	}
}
