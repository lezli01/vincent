package store

import (
	"errors"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// finishTurn runs one turn of chat c to st, ending at `at`, and returns the
// chat to idle so the next turn may start.
func finishTurn(t *testing.T, s *Store, chatID int64, st chatstate.TurnState, at time.Time) {
	t.Helper()
	ctx := t.Context()
	turn, err := s.CreateChatTurn(ctx, chatID, "go")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	if st == chatstate.TurnRunning {
		return
	}
	turn.State = st
	turn.EndedAt = &at
	if err := s.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	if _, err := s.SetChatState(ctx, chatID, chatstate.Idle); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}
}

// unreadEverywhere reads the chat through GetChat and ListChats and fails
// unless both derive the same mark — the list must not be a second rule.
func unreadEverywhere(t *testing.T, s *Store, id int64) bool {
	t.Helper()
	c, err := s.GetChat(t.Context(), id)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	list, err := s.ListChats(t.Context(), ChatFilter{})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	for i := range list {
		if list[i].ID == id && list[i].Unread != c.Unread {
			t.Fatalf("ListChats unread = %v, GetChat = %v", list[i].Unread, c.Unread)
		}
	}
	return c.Unread
}

// TestChatUnreadDerivation walks task 156's rule: only a terminal turn
// finishing after the last view makes a chat unread.
func TestChatUnreadDerivation(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	c := testChat(t, s, p.ID)
	base := time.Now().UTC().Add(-time.Hour)

	if unreadEverywhere(t, s, c.ID) {
		t.Fatal("a chat with no turns is unread")
	}

	finishTurn(t, s, c.ID, chatstate.TurnDone, base)
	if !unreadEverywhere(t, s, c.ID) {
		t.Fatal("a done turn on a never-viewed chat is not unread")
	}

	got, err := s.MarkChatViewed(ctx, c.ID, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("MarkChatViewed: %v", err)
	}
	if got.Unread || got.LastViewedAt == nil || !got.LastViewedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("after view: unread=%v last_viewed_at=%v", got.Unread, got.LastViewedAt)
	}
	if unreadEverywhere(t, s, c.ID) {
		t.Fatal("a chat viewed after its last turn is unread")
	}

	finishTurn(t, s, c.ID, chatstate.TurnRunning, time.Time{})
	if unreadEverywhere(t, s, c.ID) {
		t.Fatal("a running turn made the chat unread")
	}
	// Let the running turn fail later than the view.
	turns, err := s.ListChatTurns(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListChatTurns: %v", err)
	}
	last := turns[len(turns)-1]
	end := base.Add(2 * time.Minute)
	last.State, last.EndedAt = chatstate.TurnFailed, &end
	if err := s.UpdateChatTurn(ctx, &last); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	if _, err := s.SetChatState(ctx, c.ID, chatstate.Idle); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}
	if !unreadEverywhere(t, s, c.ID) {
		t.Fatal("a failed turn after the view did not make the chat unread")
	}

	if _, err := s.MarkChatViewed(ctx, c.ID, base.Add(3*time.Minute)); err != nil {
		t.Fatalf("MarkChatViewed: %v", err)
	}
	finishTurn(t, s, c.ID, chatstate.TurnInterruptedState, base.Add(4*time.Minute))
	if !unreadEverywhere(t, s, c.ID) {
		t.Fatal("an interrupted turn after the view did not make the chat unread")
	}
}

// TestMarkChatViewedIsMonotonic: an earlier `at` never moves the mark back,
// and only an advance writes chat.viewed.
func TestMarkChatViewedIsMonotonic(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	c := testChat(t, s, p.ID)
	at := time.Now().UTC().Truncate(time.Second)

	countViewed := func() int {
		t.Helper()
		evs, err := s.ListEvents(ctx, EventFilter{Types: []string{EventChatViewed}})
		if err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
		return len(evs)
	}

	if _, err := s.MarkChatViewed(ctx, c.ID, at); err != nil {
		t.Fatalf("MarkChatViewed: %v", err)
	}
	if n := countViewed(); n != 1 {
		t.Fatalf("chat.viewed events after first view = %d, want 1", n)
	}

	got, err := s.MarkChatViewed(ctx, c.ID, at.Add(-time.Hour))
	if err != nil {
		t.Fatalf("MarkChatViewed(earlier): %v", err)
	}
	if got.LastViewedAt == nil || !got.LastViewedAt.Equal(at) {
		t.Fatalf("last_viewed_at moved back to %v, want %v", got.LastViewedAt, at)
	}
	if _, err := s.MarkChatViewed(ctx, c.ID, at); err != nil {
		t.Fatalf("MarkChatViewed(same): %v", err)
	}
	if n := countViewed(); n != 1 {
		t.Fatalf("chat.viewed events after non-advancing views = %d, want 1", n)
	}

	if _, err := s.MarkChatViewed(ctx, c.ID, at.Add(time.Second)); err != nil {
		t.Fatalf("MarkChatViewed(later): %v", err)
	}
	if n := countViewed(); n != 2 {
		t.Fatalf("chat.viewed events after an advance = %d, want 2", n)
	}
}

func TestMarkChatViewedUnknownChat(t *testing.T) {
	s := openTest(t)
	if _, err := s.MarkChatViewed(t.Context(), 999, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkChatViewed(999) = %v, want ErrNotFound", err)
	}
}
