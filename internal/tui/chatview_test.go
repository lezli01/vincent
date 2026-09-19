package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/lezli01/vincent/internal/apiclient"
)

// chatViewFixture is a chat workspace pointed at one loaded chat.
func chatViewFixture() *chatView {
	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	v.chatID = 1
	v.chat = &apiclient.Chat{ID: 1, ProjectID: 7, Title: "a chat", State: "idle", Agent: "claude"}
	v.composer.Focus()
	return v
}

// TestChatViewNewlineKeys holds issue #500: ctrl+j, shift+enter and alt+enter
// put a newline in the draft, where the placeholder once promised one and no
// key delivered it. Each press is checked against its spelling first, because
// the composer matches on the spelling: a press that stringified differently
// would pass here and miss in a real terminal.
func TestChatViewNewlineKeys(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{"ctrl+j", tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}},
		{"shift+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}},
		{"alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.msg.String(); got != tc.name {
				t.Fatalf("test key serializes as %q, want %q", got, tc.name)
			}
			v := chatViewFixture()
			v.composer.SetValue("/re")
			v.updateKey(tc.msg)
			if got, want := v.composer.Value(), "/re\n"; got != want {
				t.Fatalf("composer value = %q, want %q", got, want)
			}
		})
	}
}

// TestChatViewNewlineKeepsTheCursorInView: a newline on the composer's last
// visible row scrolls the draft with it. The composer is three rows high, so
// a fourth line is the first that has to scroll, and a newline inserted
// around the textarea's own handling leaves the cursor on a row nobody can
// see until the next key.
func TestChatViewNewlineKeepsTheCursorInView(t *testing.T) {
	v := chatViewFixture()
	v.composer.SetValue("one\ntwo\nthree")
	v.updateKey(registryKey(t, "ctrl+j"))
	row, top, height := v.composer.Line(), v.composer.ScrollYOffset(), v.composer.Height()
	if row != 3 {
		t.Fatalf("the cursor is on line %d, want the new fourth line (3)", row)
	}
	if row < top || row >= top+height {
		t.Fatalf("the cursor is on line %d and the composer shows lines %d–%d", row, top, top+height-1)
	}
}

// TestChatViewTypedDraftEditsAndSends is task 071 decision 4 reached the way
// issue #500 meant: a draft whose lines were typed rather than pasted, ↑/↓
// moving between those lines without disturbing them, and enter sending the
// whole draft — moving the newline off enter did not move sending with it.
func TestChatViewTypedDraftEditsAndSends(t *testing.T) {
	v := chatViewFixture()
	v.client = offlineClient()
	for _, key := range []string{"o", "n", "e", "ctrl+j", "t", "w", "o"} {
		v.updateKey(registryKey(t, key))
	}
	if got := v.composer.Value(); got != "one\ntwo" {
		t.Fatalf("the typed draft is %q, want %q", got, "one\ntwo")
	}
	v.updateKey(registryKey(t, "up"))
	if got := v.composer.Line(); got != 0 {
		t.Fatalf("↑ left the cursor on line %d, want the first line", got)
	}
	v.updateKey(registryKey(t, "down"))
	if got := v.composer.Line(); got != 1 {
		t.Fatalf("↓ left the cursor on line %d, want the second line", got)
	}
	if got := v.composer.Value(); got != "one\ntwo" {
		t.Fatalf("an arrow key changed the draft to %q", got)
	}
	if _, cmd := v.updateKey(registryKey(t, "enter")); cmd == nil {
		t.Fatal("enter did not send the multi-line draft")
	}
	if got := v.composer.Value(); got != "" {
		t.Fatalf("the composer still holds %q after a send", got)
	}
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
