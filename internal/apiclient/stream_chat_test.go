package apiclient_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// The per-chat stream and the per-turn transcript, against the real handlers
// (task 067). These are the two routes 063.3 left missing: without them a
// reopened TUI shows finished turns as ResultText and nothing else, and
// §13.3's catch-up seam is exact for tasks and approximate for chats.

// chat creates a chat on the harness's project and returns it.
func (h *harness) chat(t *testing.T, title string) *store.Chat {
	t.Helper()
	c := &store.Chat{
		ProjectID: h.projectID, Title: title, State: chatstate.Idle,
		Agent: "claude", BaseBranch: "main", Branch: "vincent/1-x",
	}
	if err := h.st.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	return c
}

// writeChatTranscript lays down one turn's transcript where the route
// derives its path: {data_dir}/transcripts/chat-{id}/{seq}.jsonl.
func (h *harness) writeChatTranscript(t *testing.T, chatID int64, seq int, body string) {
	t.Helper()
	dir := filepath.Join(h.dataDir, "transcripts", worktree.ChatOwner(chatID).Dir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.jsonl", seq))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

// TestStreamChatFiltersToOneChat is the filter internal/store's chatEvent
// promises: a chat event carries no task_id, so the per-chat stream narrows
// on the payload's id. Another chat's events must not arrive, and a task's
// must not either.
func TestStreamChatFiltersToOneChat(t *testing.T) {
	h := newHarness(t)
	mine := h.chat(t, "mine")
	other := h.chat(t, "other")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	notes := h.client().StreamChat(ctx, mine.ID, testStreamOptions())
	if _, ok := nextNote(t, notes).(apiclient.ConnectedNote); !ok {
		t.Fatal("first note was not ConnectedNote")
	}
	waitForChatSubscriber(t, h, mine.ID)

	// Neither of these belongs on this stream.
	if _, err := h.st.SetChatState(t.Context(), other.ID, chatstate.Running); err != nil {
		t.Fatalf("SetChatState(other): %v", err)
	}
	h.append(t, "task.state_changed")
	// This one does, and it must be the *next* note — proving the two above
	// were filtered rather than merely late.
	if _, err := h.st.SetChatState(t.Context(), mine.ID, chatstate.Running); err != nil {
		t.Fatalf("SetChatState(mine): %v", err)
	}

	ev, ok := nextNote(t, notes).(apiclient.EventNote)
	if !ok {
		t.Fatalf("expected an EventNote, got %T", ev)
	}
	if ev.Event.Type != store.EventChatState {
		t.Fatalf("first event on the stream was %q, want this chat's state change", ev.Event.Type)
	}
}

// TestStreamChatDeliversOutput covers the live half: chunks ride the chat's
// own broker key, carry (turn_id, offset), and are not durable events.
func TestStreamChatDeliversOutput(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t, "live")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	notes := h.client().StreamChat(ctx, c.ID, testStreamOptions())
	if _, ok := nextNote(t, notes).(apiclient.ConnectedNote); !ok {
		t.Fatal("first note was not ConnectedNote")
	}
	waitForChatSubscriber(t, h, c.ID)

	// The chunk is what chatrun publishes since task 071: a §13.3 type with
	// normalized fields, and the agent's verbatim line kept beside them. The
	// raw here is a claude stream-json line, which is what a chat actually
	// carries — the older fixture used a `raw` of `{"type":"agent.output"}`,
	// a shape no adapter emits.
	h.broker.PublishOutput(chatrun.ChatOutputKey(c.ID), events.Chunk{
		Type: "agent.output",
		Payload: map[string]any{
			"chat_id": c.ID, "turn_id": int64(4), "offset": int64(2048),
			"text": "hi",
			"raw":  `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		},
	})
	out, ok := nextNote(t, notes).(apiclient.OutputNote)
	if !ok {
		t.Fatalf("expected an OutputNote, got %T", out)
	}
	if out.TurnID != 4 || out.Offset != 2048 || out.ChatID != c.ID {
		t.Fatalf("chunk identity = chat %d turn %d offset %d, want %d/4/2048",
			out.ChatID, out.TurnID, out.Offset, c.ID)
	}
	if out.Type != "agent.output" || out.Text() != "hi" {
		t.Fatalf("chunk = %s %q, want a normalized agent.output carrying \"hi\"", out.Type, out.Text())
	}
}

// TestStreamChatResumesDurableEventsOnly holds §13.3's asymmetry: a
// Last-Event-ID replay delivers the durable events behind the cursor and
// never live output, which is ephemeral by construction.
func TestStreamChatResumesDurableEvents(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t, "resume")
	// The create event is already behind us; move the chat so there is one
	// durable event to replay.
	if _, err := h.st.SetChatState(t.Context(), c.ID, chatstate.Running); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}
	maxID, err := h.st.MaxEventID(t.Context())
	if err != nil {
		t.Fatalf("MaxEventID: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notes := h.client().StreamChat(ctx, c.ID, apiclient.StreamOptions{
		LastEventID:    maxID - 1,
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	})
	if _, ok := nextNote(t, notes).(apiclient.ConnectedNote); !ok {
		t.Fatal("first note was not ConnectedNote")
	}
	ev, ok := nextNote(t, notes).(apiclient.EventNote)
	if !ok || ev.Event.ID != maxID {
		t.Fatalf("replay note = %+v, want event %d", ev, maxID)
	}
}

// TestChatTurnTranscriptSeam is the exactness the TUI's live tail depends on:
// a fetch reports X-Next-Offset, and resuming from it returns exactly the
// bytes appended since — no duplicated and no dropped record.
func TestChatTurnTranscriptSeam(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t, "seam")
	turn, err := h.st.CreateChatTurn(t.Context(), c.ID, "go")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	// vincent's own annotations pass through normalization under their own
	// names, so the fixture uses them rather than a dialect line: what this
	// test is about is the byte range, not the parser.
	first := `{"type":"vincent.note","kind":"one"}` + "\n"
	h.writeChatTranscript(t, c.ID, turn.Seq, first)

	records, next, err := h.client().ChatTurnTranscript(
		t.Context(), c.ID, turn.Seq, apiclient.TranscriptOptions{})
	if err != nil {
		t.Fatalf("ChatTurnTranscript: %v", err)
	}
	if len(records) != 1 || records[0].Kind != "one" {
		t.Fatalf("first fetch = %+v, want one record", records)
	}
	if next != int64(len(first)) {
		t.Fatalf("X-Next-Offset = %d, want %d", next, len(first))
	}

	h.writeChatTranscript(t, c.ID, turn.Seq,
		first+`{"type":"vincent.note","kind":"two"}`+"\n")
	records, _, err = h.client().ChatTurnTranscript(
		t.Context(), c.ID, turn.Seq, apiclient.TranscriptOptions{Offset: next})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(records) != 1 || records[0].Kind != "two" {
		t.Fatalf("resume returned %+v, want only the appended record", records)
	}
}

// TestChatTurnTranscriptRejectsOffsetAndTail keeps the step route's range
// contract on the chat route: the two are mutually exclusive.
func TestChatTurnTranscriptRejectsOffsetAndTail(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t, "range")
	if _, err := h.st.CreateChatTurn(t.Context(), c.ID, "go"); err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	url := fmt.Sprintf("%s/v1/chats/%d/turns/1/transcript?offset=1&tail=1", h.ts.URL, c.ID)
	if code := h.getStatus(t, url); code != 400 {
		t.Fatalf("offset+tail answered %d, want 400", code)
	}
}

// TestChatTurnTranscriptUnknownTurn is a 404 rather than an empty body: a
// turn that is not on this chat is not a turn with nothing in it.
func TestChatTurnTranscriptUnknownTurn(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t, "missing")
	url := fmt.Sprintf("%s/v1/chats/%d/turns/9/transcript", h.ts.URL, c.ID)
	if code := h.getStatus(t, url); code != 404 {
		t.Fatalf("unknown turn answered %d, want 404", code)
	}
}

// waitForChatSubscriber blocks until the server's output subscription for the
// chat is registered, so a publish cannot race the stream opening.
func waitForChatSubscriber(t *testing.T, h *harness, chatID int64) {
	t.Helper()
	key := chatrun.ChatOutputKey(chatID)
	deadline := time.Now().Add(noteTimeout)
	for h.broker.OutputSubscribers(key) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no output subscriber registered for the chat")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
