package store

import (
	"errors"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
)

// liveChatWithQueue is a chat with a running turn and prompts queued behind it.
func liveChatWithQueue(t *testing.T, s *Store, prompts ...string) (*Chat, *ChatTurn, []*ChatQueuedMessage) {
	t.Helper()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	turn, err := s.CreateChatTurn(t.Context(), c.ID, "first")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	var qs []*ChatQueuedMessage
	for _, pr := range prompts {
		q, err := s.EnqueueChatMessage(t.Context(), c.ID, pr)
		if err != nil {
			t.Fatalf("EnqueueChatMessage: %v", err)
		}
		qs = append(qs, q)
	}
	return c, turn, qs
}

// TestEnqueueNeedsALiveTurn: a queued message exists only behind a live turn
// (task 156 decision 1), and a running chat never takes a second turn row.
func TestEnqueueNeedsALiveTurn(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	if _, err := s.EnqueueChatMessage(t.Context(), c.ID, "x"); !errors.Is(err, ErrInvalidChatAction) {
		t.Fatalf("enqueue on an idle chat = %v, want ErrInvalidChatAction", err)
	}
	if _, err := s.CreateChatTurn(t.Context(), c.ID, "one"); err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	if _, err := s.CreateChatTurn(t.Context(), c.ID, "two"); !errors.Is(err, ErrInvalidChatAction) {
		t.Fatalf("a second turn on a running chat = %v, want ErrInvalidChatAction", err)
	}
}

// TestFinishPopsIntoATurnAtomically: a done ending turns the oldest queued
// row into a turn in the same transaction that deletes it — never both, never
// neither — and the chat stays running.
func TestFinishPopsIntoATurnAtomically(t *testing.T) {
	s := openTest(t)
	c, turn, qs := liveChatWithQueue(t, s, "second", "third")
	turn.State = chatstate.TurnDone
	next, err := s.FinishChatTurn(t.Context(), turn, "")
	if err != nil {
		t.Fatalf("FinishChatTurn: %v", err)
	}
	if next == nil || next.Prompt != "second" || next.Seq != 2 {
		t.Fatalf("popped turn = %+v, want seq 2 \"second\"", next)
	}
	chat, _ := s.GetChat(t.Context(), c.ID)
	if chat.State != chatstate.Running || chat.QueuedCount != 1 {
		t.Fatalf("chat %s queued=%d, want running/1", chat.State, chat.QueuedCount)
	}
	queue, _ := s.ListChatQueue(t.Context(), c.ID)
	turns, _ := s.ListChatTurns(t.Context(), c.ID)
	if len(queue)+len(turns) != 3 {
		t.Fatalf("%d queued + %d turns, want 3 messages in all", len(queue), len(turns))
	}
	if _, err := s.EditChatQueued(t.Context(), c.ID, qs[0].ID, "too late"); !errors.Is(err, ErrQueuedMessageGone) {
		t.Fatalf("edit of a started message = %v, want ErrQueuedMessageGone", err)
	}
	if err := s.DeleteChatQueued(t.Context(), c.ID, qs[0].ID); !errors.Is(err, ErrQueuedMessageGone) {
		t.Fatalf("delete of a started message = %v, want ErrQueuedMessageGone", err)
	}
	if m, err := s.EditChatQueued(t.Context(), c.ID, qs[1].ID, "third, edited"); err != nil || m.Prompt != "third, edited" {
		t.Fatalf("edit of a queued message = %+v, %v", m, err)
	}
}

// TestFailurePausesAndClearLifts: a non-done ending returns the chat to idle
// and pauses the queue with the reason; clear deletes it and lifts the pause.
func TestFailurePausesAndClearLifts(t *testing.T) {
	s := openTest(t)
	c, turn, _ := liveChatWithQueue(t, s, "second")
	turn.State, turn.FailReason = chatstate.TurnFailed, "agent_error"
	if next, err := s.FinishChatTurn(t.Context(), turn, "agent_error"); err != nil || next != nil {
		t.Fatalf("FinishChatTurn = %v, %v; want no next turn", next, err)
	}
	chat, _ := s.GetChat(t.Context(), c.ID)
	if chat.State != chatstate.Idle || !chat.QueuePaused || chat.QueuePauseReason != "agent_error" {
		t.Fatalf("chat %s paused=%v reason=%q, want idle/true/agent_error",
			chat.State, chat.QueuePaused, chat.QueuePauseReason)
	}
	if err := s.ClearChatQueue(t.Context(), c.ID); err != nil {
		t.Fatalf("ClearChatQueue: %v", err)
	}
	chat, _ = s.GetChat(t.Context(), c.ID)
	if chat.QueuePaused || chat.QueuedCount != 0 {
		t.Fatalf("after clear paused=%v queued=%d", chat.QueuePaused, chat.QueuedCount)
	}
}

// TestTerminalTransitionsDeleteTheQueue is task 156 decision 6 for archive,
// close and hand-off: each deletes the chat's queued messages in the
// transaction that ends it.
func TestTerminalTransitionsDeleteTheQueue(t *testing.T) {
	end := map[string]func(t *testing.T, s *Store, c *Chat){
		"archive": func(t *testing.T, s *Store, c *Chat) {
			if _, err := s.SetChatState(t.Context(), c.ID, chatstate.Archived); err != nil {
				t.Fatalf("archive: %v", err)
			}
		},
		"handoff": func(t *testing.T, s *Store, c *Chat) {
			task := newTask(c.ProjectID, "carry on", TaskQueued)
			task.BranchName, task.BaseBranch, task.BaseSHA = c.Branch, c.BaseBranch, c.BaseSHA
			task.WorktreePath = c.WorktreePath
			if _, err := s.HandoffChat(t.Context(), c.ID, task); err != nil {
				t.Fatalf("handoff: %v", err)
			}
		},
		"close": func(t *testing.T, s *Store, c *Chat) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			if _, _, err := closeChatTx(t.Context(), tx, c); err != nil {
				t.Fatalf("closeChatTx: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}
		},
	}
	for name, fn := range end {
		t.Run(name, func(t *testing.T) {
			s := openTest(t)
			c, turn, _ := liveChatWithQueue(t, s, "a", "b")
			turn.State, turn.FailReason = chatstate.TurnFailed, "canceled"
			if _, err := s.FinishChatTurn(t.Context(), turn, "canceled"); err != nil {
				t.Fatalf("FinishChatTurn: %v", err)
			}
			c, _ = s.GetChat(t.Context(), c.ID)
			fn(t, s, c)
			queue, err := s.ListChatQueue(t.Context(), c.ID)
			if err != nil || len(queue) != 0 {
				t.Fatalf("%s left %d queued messages (%v)", name, len(queue), err)
			}
		})
	}
}
