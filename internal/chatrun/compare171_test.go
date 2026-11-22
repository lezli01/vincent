package chatrun

import (
	"errors"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// TestSubmitPairIsAllOrNothing (task 171 decision 6): a pair starts on two
// idle chats with one prompt, and is refused — starting neither — when the
// cap has no room for two, or when either side is not idle.
func TestSubmitPairIsAllOrNothing(t *testing.T) {
	h := newHarness(t)
	a, b, c := h.chat(t), h.chat(t), h.chat(t)

	noTurns := func(ids ...int64) {
		t.Helper()
		for _, id := range ids {
			if turns, err := h.store.ListChatTurns(t.Context(), id); err != nil || len(turns) != 0 {
				t.Fatalf("chat %d got turns %+v (%v)", id, turns, err)
			}
		}
	}

	// Room for one more chat, not two.
	h.cfg.MaxParallelChats = 1
	if _, err := h.runner.SubmitPair(t.Context(), [2]int64{a.ID, b.ID}, "q"); !errors.Is(err, ErrChatCapReached) {
		t.Fatalf("pair with room for one = %v, want ErrChatCapReached", err)
	}
	noTurns(a.ID, b.ID)
	h.cfg.MaxParallelChats = 4

	if _, err := h.store.SetChatState(t.Context(), c.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runner.SubmitPair(t.Context(), [2]int64{a.ID, c.ID}, "q"); !errors.Is(err, store.ErrInvalidChatAction) {
		t.Fatalf("pair with an archived side = %v, want ErrInvalidChatAction", err)
	}
	noTurns(a.ID)

	turns, err := h.runner.SubmitPair(t.Context(), [2]int64{a.ID, b.ID}, "the same question")
	if err != nil {
		t.Fatalf("SubmitPair: %v", err)
	}
	for i, id := range []int64{a.ID, b.ID} {
		if turns[i] == nil || turns[i].ChatID != id || turns[i].Prompt != "the same question" {
			t.Fatalf("side %d turn = %+v", i, turns[i])
		}
		h.waitTurn(t, turns[i].ID)
		h.waitIdle(t, id)
	}
}
