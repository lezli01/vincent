package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
)

// sendTurns runs each prompt as a finished turn of chat c, in order.
func sendTurns(t *testing.T, s *Store, chatID int64, prompts ...string) {
	t.Helper()
	for _, p := range prompts {
		turn, err := s.CreateChatTurn(t.Context(), chatID, p)
		if err != nil {
			t.Fatalf("CreateChatTurn(%q): %v", p, err)
		}
		turn.State = chatstate.TurnDone
		if _, err := s.FinishChatTurn(t.Context(), turn, ""); err != nil {
			t.Fatalf("FinishChatTurn: %v", err)
		}
	}
}

// TestChatDraftRoundTrip: a draft and a stash are written independently, read
// back with the chat, and each write publishes chat.draft_changed.
func TestChatDraftRoundTrip(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	if _, err := s.SetChatDraft(t.Context(), c.ID, "half a thought"); err != nil {
		t.Fatalf("SetChatDraft: %v", err)
	}
	if _, err := s.SetChatStash(t.Context(), c.ID, "set aside"); err != nil {
		t.Fatalf("SetChatStash: %v", err)
	}
	got, err := s.GetChat(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Draft != "half a thought" || got.Stash != "set aside" || got.DraftUpdatedAt == nil {
		t.Fatalf("chat = draft %q stash %q at %v", got.Draft, got.Stash, got.DraftUpdatedAt)
	}
	evs, err := s.ListEvents(t.Context(), EventFilter{Types: []string{EventChatDraft}})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == EventChatDraft {
			n++
			if strings.Contains(string(e.Payload), "half a thought") {
				t.Fatalf("event carries the draft text: %s", e.Payload)
			}
		}
	}
	if n != 2 {
		t.Fatalf("%d chat.draft_changed events, want 2", n)
	}
	if _, err := s.SetChatDraft(t.Context(), c.ID, strings.Repeat("x", MaxChatDraftBytes+1)); !errors.Is(err, ErrDraftTooLong) {
		t.Fatalf("oversized draft = %v, want ErrDraftTooLong", err)
	}
}

// TestChatDraftClearedWhenTheChatEnds: archive clears both, and a terminal
// chat takes no new draft.
func TestChatDraftClearedWhenTheChatEnds(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	if _, err := s.UpdateChatDraft(t.Context(), c.ID, ptr("d"), ptr("s")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetChatState(t.Context(), c.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetChat(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Draft != "" || got.Stash != "" {
		t.Fatalf("archived chat kept draft %q stash %q", got.Draft, got.Stash)
	}
	if _, err := s.SetChatDraft(t.Context(), c.ID, "late"); !errors.Is(err, ErrInvalidChatAction) {
		t.Fatalf("draft on an archived chat = %v, want ErrInvalidChatAction", err)
	}
}

// TestListChatPrompts: by recency for a short query and by the trigram index
// for a long one, newest first, de-duplicated, across archived chats, and
// scoped to the project.
func TestListChatPrompts(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	other := testProject(t, s, "p2")
	a := testChat(t, s, p.ID)
	b := testChat(t, s, p.ID)
	x := testChat(t, s, other.ID)
	sendTurns(t, s, a.ID, "fix the parser", "run the tests")
	sendTurns(t, s, b.ID, "fix the parser", "explain the lexer")
	sendTurns(t, s, x.ID, "fix the other project")
	if _, err := s.SetChatState(t.Context(), b.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}

	texts := func(ps []ChatPrompt) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Prompt)
		}
		return out
	}
	for _, q := range []string{"", "fi"} {
		got, err := s.ListChatPrompts(t.Context(), p.ID, q, 0)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"explain the lexer", "fix the parser", "run the tests"}
		if strings.Join(texts(got), "|") != strings.Join(want, "|") {
			t.Fatalf("q=%q: %q, want %q", q, texts(got), want)
		}
		if got[1].ChatID != b.ID || got[1].Seq != 1 {
			t.Fatalf("duplicate collapsed to chat %d seq %d, want its newest (chat %d seq 1)", got[1].ChatID, got[1].Seq, b.ID)
		}
	}
	got, err := s.ListChatPrompts(t.Context(), p.ID, "pars", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Prompt != "fix the parser" || got[0].ChatState != chatstate.Archived {
		t.Fatalf("q=pars: %+v", got)
	}
	// A short term beside a long one still narrows (review F4): it is checked
	// on the rows the index returned, ignoring case, rather than dropping
	// the whole query to recency.
	for q, want := range map[string]string{
		"fix q":  "",
		"fix th": "fix the parser",
		"the RU": "run the tests",
		"the xp": "explain the lexer",
	} {
		got, err := s.ListChatPrompts(t.Context(), p.ID, q, 0)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(texts(got), "|") != want {
			t.Fatalf("q=%q: %q, want %q", q, texts(got), want)
		}
	}
	got, err = s.ListChatPrompts(t.Context(), p.ID, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("limit 1 gave %d", len(got))
	}
}
