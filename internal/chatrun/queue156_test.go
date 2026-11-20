package chatrun

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// submitQueued sends prompt to a live chat and fails unless it was queued.
func (h *harness) submitQueued(t *testing.T, chatID int64, prompt string) *store.ChatQueuedMessage {
	t.Helper()
	turn, q, err := h.runner.Submit(t.Context(), chatID, prompt)
	if err != nil {
		t.Fatalf("Submit(%q): %v", prompt, err)
	}
	if turn != nil || q == nil {
		t.Fatalf("Submit(%q) on a live chat started turn %v; want it queued", prompt, turn)
	}
	return q
}

// TestQueuedMessagesDrainInOrderWithoutDroppingTheSlot is task 156 decision
// 1 on every adapter, through fakeagent's four dialects: messages sent while
// a turn runs run after it, in order, and the chat never leaves its
// `max_parallel_chats` slot in between — it is never seen idle until the
// queue is empty, and a second chat's send still sees the cap full.
func TestQueuedMessagesDrainInOrderWithoutDroppingTheSlot(t *testing.T) {
	for _, name := range []string{"claude", "codex", "cursor", "copilot"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.cfg.MaxParallelChats = 1
			t.Setenv("FAKEAGENT_DELAY_MS", "600")
			c := h.chatOn(t, name)
			other := h.chatOn(t, name)

			first, err := h.runner.Send(t.Context(), c.ID, "first")
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			h.submitQueued(t, c.ID, "second")
			h.submitQueued(t, c.ID, "third")

			// Watch the chat from beside the runner: an idle sighting
			// before the third turn exists is a dropped slot.
			var sawIdleEarly atomic.Bool
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					select {
					case <-stop:
						return
					default:
					}
					got, err := h.store.GetChat(t.Context(), c.ID)
					if err != nil {
						return
					}
					if got.State == chatstate.Idle && got.QueuedCount > 0 {
						sawIdleEarly.Store(true)
					}
				}
			}()
			if _, err := h.runner.Send(t.Context(), other.ID, "me too"); !errors.Is(err, ErrChatCapReached) {
				t.Errorf("a second chat's send mid-queue = %v, want ErrChatCapReached", err)
			}
			h.waitTurn(t, first.ID)
			waitFor(t, func() bool {
				turns, err := h.store.ListChatTurns(t.Context(), c.ID)
				return err == nil && len(turns) == 3 && chatstate.TurnTerminal(turns[2].State)
			})
			got := h.waitIdle(t, c.ID)
			close(stop)
			<-done
			if sawIdleEarly.Load() {
				t.Error("the chat was idle with messages queued: the drain released its slot")
			}
			if got.QueuedCount != 0 || got.QueuePaused {
				t.Errorf("after the drain queued=%d paused=%v, want 0/false", got.QueuedCount, got.QueuePaused)
			}
			turns, err := h.store.ListChatTurns(t.Context(), c.ID)
			if err != nil {
				t.Fatalf("ListChatTurns: %v", err)
			}
			for i, want := range []string{"first", "second", "third"} {
				if turns[i].Prompt != want || turns[i].State != chatstate.TurnDone {
					t.Errorf("turn %d = %q/%s, want %q/done", i+1, turns[i].Prompt, turns[i].State, want)
				}
			}
			assertSlotFree(t, h)
		})
	}
}

// TestCancelPausesTheQueueAndResumeDrains is task 156 decision 3 for a human
// cancel: the queue stays, paused with the reason, and the chat is idle and
// usable; resume starts the next message.
func TestCancelPausesTheQueueAndResumeDrains(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	c := h.chat(t)
	first, err := h.runner.Send(t.Context(), c.ID, "hold the line")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, func() bool { return h.runner.Running(c.ID) })
	h.submitQueued(t, c.ID, "after the hang")
	if err := h.runner.Cancel(t.Context(), c.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := h.waitTurn(t, first.ID); got.FailReason != ReasonCanceled {
		t.Fatalf("first turn = %s/%s, want failed/canceled", got.State, got.FailReason)
	}
	chat := h.waitIdle(t, c.ID)
	if !chat.QueuePaused || chat.QueuePauseReason != ReasonCanceled || chat.QueuedCount != 1 {
		t.Fatalf("after cancel paused=%v reason=%q queued=%d, want true/canceled/1",
			chat.QueuePaused, chat.QueuePauseReason, chat.QueuedCount)
	}

	t.Setenv("FAKEAGENT_SCENARIO", "")
	next, err := h.runner.ResumeQueue(t.Context(), c.ID)
	if err != nil || next == nil {
		t.Fatalf("ResumeQueue = %v, %v; want the queued message started", next, err)
	}
	if got := h.waitTurn(t, next.ID); got.State != chatstate.TurnDone || got.Prompt != "after the hang" {
		t.Fatalf("resumed turn = %q/%s, want the queued message done", got.Prompt, got.State)
	}
	chat = h.waitIdle(t, c.ID)
	if chat.QueuePaused || chat.QueuedCount != 0 {
		t.Fatalf("after resume paused=%v queued=%d, want false/0", chat.QueuePaused, chat.QueuedCount)
	}
}

// TestFailedTurnPausesTheQueue covers a failure ending (agent_timeout): the
// rest of the queue waits rather than running into the same wall.
func TestFailedTurnPausesTheQueue(t *testing.T) {
	h := newHarness(t)
	h.cfg.Defaults.AgentTimeout = config.Duration(400 * time.Millisecond)
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	c := h.chat(t)
	first, err := h.runner.Send(t.Context(), c.ID, "run forever")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.submitQueued(t, c.ID, "next")
	h.waitTurn(t, first.ID)
	chat := h.waitIdle(t, c.ID)
	if !chat.QueuePaused || chat.QueuePauseReason != ReasonTimeout || chat.QueuedCount != 1 {
		t.Fatalf("after a timeout paused=%v reason=%q queued=%d, want true/%s/1",
			chat.QueuePaused, chat.QueuePauseReason, chat.QueuedCount, ReasonTimeout)
	}
	turns, _ := h.store.ListChatTurns(t.Context(), c.ID)
	if len(turns) != 1 {
		t.Fatalf("a paused queue started %d turns, want only the failed one", len(turns))
	}
}

// TestSendWhileAwaitingInputQueuesAndNeverAnswers is task 156 decision 2.
func TestSendWhileAwaitingInputQueuesAndNeverAnswers(t *testing.T) {
	h := newHarness(t)
	h.cfg.Defaults.AgentTimeout = config.Duration(30 * time.Second)
	h.cfg.Defaults.InputTimeout = config.Duration(30 * time.Second)
	t.Setenv("FAKEAGENT_SCENARIO", "ask-question")
	c := h.chat(t)
	if _, err := h.runner.Send(t.Context(), c.ID, "ask me something"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.waitState(t, c.ID, chatstate.AwaitingInput)
	h.submitQueued(t, c.ID, "blue")
	got, err := h.store.GetChat(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.State != chatstate.AwaitingInput || len(got.PendingInput) == 0 {
		t.Fatalf("after a queued send the chat is %s (pending %d bytes); the question must still wait",
			got.State, len(got.PendingInput))
	}
	if err := h.runner.Answer(t.Context(), c.ID, agent.InputResponse{
		Answers: map[string][]string{"Which colour?": {"blue"}},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	_ = h.runner.Cancel(t.Context(), c.ID)
}

// TestRecoverPausesTheQueueAndNeverReRuns is §12.4 for a queue: a started
// message is a turn, finalized interrupted and never re-run, and the rest of
// the queue is left paused, so a restart drains nothing on its own.
func TestRecoverPausesTheQueueAndNeverReRuns(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	turn, err := h.store.CreateChatTurn(t.Context(), c.ID, "was running")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	for _, p := range []string{"two", "three"} {
		if _, err := h.store.EnqueueChatMessage(t.Context(), c.ID, p); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	if err := h.runner.Recover(t.Context()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	got, err := h.store.GetChatTurn(t.Context(), turn.ID)
	if err != nil || got.State != chatstate.TurnInterruptedState {
		t.Fatalf("recovered turn = %v, %v; want interrupted", got, err)
	}
	chat, err := h.store.GetChat(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if chat.State != chatstate.Idle || !chat.QueuePaused ||
		chat.QueuePauseReason != store.QueuePauseInterrupted || chat.QueuedCount != 2 {
		t.Fatalf("recovered chat %s paused=%v reason=%q queued=%d, want idle/true/interrupted/2",
			chat.State, chat.QueuePaused, chat.QueuePauseReason, chat.QueuedCount)
	}
	turns, _ := h.store.ListChatTurns(t.Context(), c.ID)
	if len(turns) != 1 {
		t.Fatalf("recovery left %d turns, want the interrupted one only", len(turns))
	}
}
