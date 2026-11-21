package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
)

// finishTurnWithCost runs prompt as a finished turn of chat chatID that spent
// cost and tokens, so a rewind can be shown not to touch either.
func finishTurnWithCost(t *testing.T, s *Store, chatID int64, prompt string, cost float64, in, out int64) *ChatTurn {
	t.Helper()
	turn, err := s.CreateChatTurn(t.Context(), chatID, prompt)
	if err != nil {
		t.Fatalf("CreateChatTurn(%q): %v", prompt, err)
	}
	turn.State = chatstate.TurnDone
	turn.CostUSD, turn.InputTokens, turn.OutputTokens = &cost, in, out
	if _, err := s.FinishChatTurn(t.Context(), turn, ""); err != nil {
		t.Fatalf("FinishChatTurn: %v", err)
	}
	return turn
}

// chatTurnTotals is the §17 sum over every turn row of a chat.
func chatTurnTotals(t *testing.T, s *Store, chatID int64) (cost float64, in, out int64) {
	t.Helper()
	turns, err := s.ListChatTurns(t.Context(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range turns {
		if tr.CostUSD != nil {
			cost += *tr.CostUSD
		}
		in += tr.InputTokens
		out += tr.OutputTokens
	}
	return cost, in, out
}

// TestChatRewindColumnsRoundTrip: the fork and resume-plan columns go in
// through CreateChat and come back through GetChat, and a turn's seeded flag
// and resume anchor go in through UpdateChatTurn and come back through
// ListChatTurns.
func TestChatRewindColumnsRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	src := testChat(t, s, p.ID)

	fromID, fromSeq := src.ID, 3
	fork := &Chat{
		ProjectID: p.ID, Title: "a fork", Agent: "claude",
		Branch: "vincent/2-a-fork", BaseBranch: "main", BaseSHA: "abc123",
		WorktreePath: "/wt/chat-2", PermissionMode: "full_auto",
		SessionID: "sess-1", ResumeAt: "uuid-7", PendingPreamble: "earlier: hello",
		ForkedFromChatID: &fromID, ForkedFromSeq: &fromSeq,
	}
	if err := s.CreateChat(ctx, fork); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	got, err := s.GetChat(ctx, fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "sess-1" || got.ResumeAt != "uuid-7" || got.PendingPreamble != "earlier: hello" {
		t.Fatalf("plan = %q %q %q", got.SessionID, got.ResumeAt, got.PendingPreamble)
	}
	if got.ForkedFromChatID == nil || *got.ForkedFromChatID != src.ID || got.ForkedFromSeq == nil || *got.ForkedFromSeq != 3 {
		t.Fatalf("forked from = %v / %v", got.ForkedFromChatID, got.ForkedFromSeq)
	}

	plain, err := s.GetChat(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plain.ForkedFromChatID != nil || plain.ForkedFromSeq != nil || plain.ResumeAt != "" || plain.PendingPreamble != "" {
		t.Fatalf("a chat that is not a fork reads %+v", plain)
	}

	turn, err := s.CreateChatTurn(ctx, src.ID, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if turn.Seeded || turn.ResumeAnchor != "" {
		t.Fatalf("new turn = seeded %v anchor %q", turn.Seeded, turn.ResumeAnchor)
	}
	turn.Seeded, turn.ResumeAnchor = true, "uuid-9"
	if err := s.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	turns, err := s.ListChatTurns(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || !turns[0].Seeded || turns[0].ResumeAnchor != "uuid-9" ||
		turns[0].RewoundAt != nil || turns[0].RewindID != "" {
		t.Fatalf("turns = %+v", turns)
	}
}

// TestSetChatResumePlan writes the three plan values, clears them again, and
// publishes nothing.
func TestSetChatResumePlan(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	var mu sync.Mutex
	var seen []string
	s.SetEventHook(func(e *Event) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, e.Type)
	})

	got, err := s.SetChatResumePlan(ctx, c.ID, "sess-2", "uuid-1", "seed")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "sess-2" || got.ResumeAt != "uuid-1" || got.PendingPreamble != "seed" {
		t.Fatalf("plan = %+v", got)
	}
	if got, err = s.SetChatResumePlan(ctx, c.ID, "sess-2", "", ""); err != nil {
		t.Fatal(err)
	}
	read, err := s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.SessionID != "sess-2" || read.ResumeAt != "" || read.PendingPreamble != "" || got.ResumeAt != "" {
		t.Fatalf("cleared plan reads %q %q %q", read.SessionID, read.ResumeAt, read.PendingPreamble)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 0 {
		t.Fatalf("SetChatResumePlan published %v", seen)
	}
	if _, err := s.SetChatResumePlan(ctx, 9999, "", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown chat: err = %v, want ErrNotFound", err)
	}
}

// TestRewindChatConversation is task 167 decision F's conversation half:
// exactly the turns from FromSeq on that no earlier rewind took are marked
// under the one rewind id, the plan and draft are set verbatim, chat.updated
// is published, nothing is spent or lost, and the next turn's seq continues
// after the rewound ones.
func TestRewindChatConversation(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	for i, prompt := range []string{"one", "two", "three", "four"} {
		finishTurnWithCost(t, s, c.ID, prompt, 0.25*float64(i+1), int64(100*(i+1)), int64(10*(i+1)))
	}
	costBefore, inBefore, outBefore := chatTurnTotals(t, s, c.ID)

	// An earlier rewind already took turn 4; the later one must not re-stamp it.
	if _, err := s.RewindChatConversation(ctx, c.ID, ConversationRewind{FromSeq: 4, RewindID: "rw-a"}); err != nil {
		t.Fatalf("first rewind: %v", err)
	}

	var mu sync.Mutex
	var seen []string
	s.SetEventHook(func(e *Event) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, e.Type)
	})
	got, err := s.RewindChatConversation(ctx, c.ID, ConversationRewind{
		FromSeq: 2, RewindID: "rw-b", SessionID: "sess-x", ResumeAt: "uuid-1", Preamble: "", Draft: "two",
	})
	if err != nil {
		t.Fatalf("RewindChatConversation: %v", err)
	}
	if got.SessionID != "sess-x" || got.ResumeAt != "uuid-1" || got.PendingPreamble != "" || got.Draft != "two" {
		t.Fatalf("returned chat = %+v", got)
	}
	read, err := s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.SessionID != "sess-x" || read.ResumeAt != "uuid-1" || read.Draft != "two" || read.DraftUpdatedAt == nil {
		t.Fatalf("stored chat = %+v", read)
	}
	mu.Lock()
	updated := 0
	for _, ty := range seen {
		if ty == EventChatUpdated {
			updated++
		}
	}
	mu.Unlock()
	if updated != 1 {
		t.Fatalf("events = %v, want one %s", seen, EventChatUpdated)
	}

	turns, err := s.ListChatTurns(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantID := map[int]string{1: "", 2: "rw-b", 3: "rw-b", 4: "rw-a"}
	for _, tr := range turns {
		if tr.RewindID != wantID[tr.Seq] || (tr.RewoundAt == nil) != (wantID[tr.Seq] == "") {
			t.Errorf("turn %d: rewind id %q at %v, want %q", tr.Seq, tr.RewindID, tr.RewoundAt, wantID[tr.Seq])
		}
		if tr.State != chatstate.TurnDone || tr.Prompt == "" {
			t.Errorf("turn %d otherwise changed: %+v", tr.Seq, tr)
		}
	}
	if turns[1].RewoundAt != nil && turns[2].RewoundAt != nil && !turns[1].RewoundAt.Equal(*turns[2].RewoundAt) {
		t.Errorf("one rewind stamped two times: %v, %v", turns[1].RewoundAt, turns[2].RewoundAt)
	}

	costAfter, inAfter, outAfter := chatTurnTotals(t, s, c.ID)
	if costAfter != costBefore || inAfter != inBefore || outAfter != outBefore {
		t.Fatalf("totals changed: %v/%d/%d -> %v/%d/%d", costBefore, inBefore, outBefore, costAfter, inAfter, outAfter)
	}

	next, err := s.CreateChatTurn(ctx, c.ID, "two, again")
	if err != nil {
		t.Fatal(err)
	}
	if next.Seq != 5 {
		t.Fatalf("turn after a rewind has seq %d, want 5", next.Seq)
	}
	if next.SessionID != "sess-x" {
		t.Fatalf("turn after a rewind runs on session %q, want the rewind's", next.SessionID)
	}

	if _, err := s.RewindChatConversation(ctx, 9999, ConversationRewind{FromSeq: 1, RewindID: "rw-c"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown chat: err = %v, want ErrNotFound", err)
	}
}

// TestExistingChatIDs: a live and an archived chat exist, a permanently
// deleted one and an id never issued do not, and every id asked about has an
// answer.
func TestExistingChatIDs(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	live := testChat(t, s, p.ID)
	archived := testChat(t, s, p.ID)
	deleted := testChat(t, s, p.ID)
	for _, id := range []int64{archived.ID, deleted.ID} {
		if _, err := s.SetChatState(ctx, id, chatstate.Archived); err != nil {
			t.Fatalf("archive %d: %v", id, err)
		}
	}
	if err := s.DeleteChatCascade(ctx, deleted.ID); err != nil {
		t.Fatalf("DeleteChatCascade: %v", err)
	}

	got, err := s.ExistingChatIDs(ctx, []int64{live.ID, archived.ID, deleted.ID, 9999})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]bool{live.ID: true, archived.ID: true, deleted.ID: false, 9999: false}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("chat %d exists = %v, want %v", id, got[id], w)
		}
	}
	empty, err := s.ExistingChatIDs(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("no ids: %v, %v", empty, err)
	}
}
