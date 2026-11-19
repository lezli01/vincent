package chatrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// handoverRun sends "first" on a fresh chat, queues "second" and "third"
// behind it, and runs hook once, at the first queue handover. It returns the
// chat once it is idle again.
func handoverRun(t *testing.T, h *harness, hook func(chatID int64)) *store.Chat {
	t.Helper()
	// Long enough for both messages to be queued behind the first turn.
	t.Setenv("FAKEAGENT_DELAY_MS", "400")
	var fired atomic.Bool
	h.runner.handover = func(chatID int64) {
		if fired.CompareAndSwap(false, true) {
			hook(chatID)
		}
	}
	c := h.chat(t)
	first, err := h.runner.Send(t.Context(), c.ID, "first")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.submitQueued(t, c.ID, "second")
	h.submitQueued(t, c.ID, "third")
	if got := h.waitTurn(t, first.ID); got.State != chatstate.TurnDone {
		t.Fatalf("first turn = %s/%s, want done", got.State, got.FailReason)
	}
	got := h.waitIdle(t, c.ID)
	if !fired.Load() {
		t.Fatal("the handover hook never ran")
	}
	return got
}

// assertHandoverEnding checks that the popped turn ended as want and the
// rest of the queue is paused for pause.
func assertHandoverEnding(t *testing.T, h *harness, chat *store.Chat, want chatstate.TurnState, reason, pause string) {
	t.Helper()
	turns, err := h.store.ListChatTurns(t.Context(), chat.ID)
	if err != nil {
		t.Fatalf("ListChatTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("%d turns, want the done first and the popped second", len(turns))
	}
	if p := turns[1]; p.Prompt != "second" || p.State != want || p.FailReason != reason {
		t.Errorf("popped turn = %q %s/%s, want %q %s/%s", p.Prompt, p.State, p.FailReason, "second", want, reason)
	}
	// TurnWritesNoTranscript says a turn ending this way has a transcript,
	// so `vincent chat transcript` reads it rather than reporting a missing
	// file (review F5).
	if TurnWritesNoTranscript(turns[1].FailReason) {
		t.Errorf("TurnWritesNoTranscript(%q) is true for a handover ending", turns[1].FailReason)
	}
	path := filepath.Join(h.runner.transcriptDir(chat.ID), fmt.Sprintf("%d.jsonl", turns[1].Seq))
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the popped turn has no transcript: %v", err)
	}
	if !chat.QueuePaused || chat.QueuePauseReason != pause || chat.QueuedCount != 1 {
		t.Errorf("chat paused=%v reason=%q queued=%d, want true/%s/1",
			chat.QueuePaused, chat.QueuePauseReason, chat.QueuedCount, pause)
	}
	msgs, err := h.store.ListChatQueue(t.Context(), chat.ID)
	if err != nil || len(msgs) != 1 || msgs[0].Prompt != "third" {
		t.Errorf("queue = %+v, %v; want third still waiting", msgs, err)
	}
}

// TestStopTurnDuringAHandoverCancelsThePoppedTurn is the runner's `stopped`
// branch (review F3): a StopTurn — a close, or the linked task's cancel —
// landing between a `done` turn and the turn it popped stops that turn before
// it starts, ends it canceled, and pauses the rest of the queue.
func TestStopTurnDuringAHandoverCancelsThePoppedTurn(t *testing.T) {
	h := newHarness(t)
	stopped := make(chan struct{})
	chat := handoverRun(t, h, func(chatID int64) {
		// StopTurn waits for this goroutine, so it runs beside it; the hook
		// returns once the stop has been recorded.
		go func() {
			h.runner.StopTurn(context.Background(), chatID)
			close(stopped)
		}()
		wait.Poll(wait.Timeout(10*time.Second), time.Millisecond, func() bool {
			h.runner.mu.Lock()
			defer h.runner.mu.Unlock()
			ctl := h.runner.turns[chatID]
			return ctl != nil && ctl.stopped
		})
	})
	select {
	case <-stopped:
	case <-time.After(wait.Timeout(10 * time.Second)):
		t.Fatal("StopTurn never returned")
	}
	assertHandoverEnding(t, h, chat, chatstate.TurnFailed, ReasonCanceled, ReasonCanceled)
}

// TestShutdownDuringAHandoverInterruptsThePoppedTurn is the runner's
// `r.base.Err()` branch (review F3): a daemon going away mid-handover ends the
// popped turn interrupted, never re-run, with the rest of the queue paused
// (§12.4).
func TestShutdownDuringAHandoverInterruptsThePoppedTurn(t *testing.T) {
	h := newHarness(t)
	chat := handoverRun(t, h, func(int64) { h.runner.cancel() })
	assertHandoverEnding(t, h, chat, chatstate.TurnInterruptedState, "", store.QueuePauseInterrupted)
}

// TestResumeRacingATurnsEndingStillStartsTheQueue is review F4: a resume
// that reads a running chat, whose turn then ends `done` against a queue
// still paused — so the chat goes idle — must not leave the chat idle with an
// unpaused queue nothing will drain. It starts the queue instead.
func TestResumeRacingATurnsEndingStillStartsTheQueue(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKEAGENT_DELAY_MS", "300")
	c := h.chat(t)
	first, err := h.runner.Send(t.Context(), c.ID, "first")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.submitQueued(t, c.ID, "next")
	// The queue a failed turn paused, with a fresh turn now running: the
	// state the race starts from.
	if err := h.store.PauseChatQueue(t.Context(), c.ID, ReasonTimeout); err != nil {
		t.Fatalf("PauseChatQueue: %v", err)
	}
	var fired atomic.Bool
	h.runner.resumeRead = func(int64) {
		if !fired.CompareAndSwap(false, true) {
			return
		}
		// The turn ends between the resume's read and its write.
		if got := h.waitTurn(t, first.ID); got.State != chatstate.TurnDone {
			t.Errorf("first turn = %s/%s, want done", got.State, got.FailReason)
		}
		h.waitIdle(t, c.ID)
	}
	next, err := h.runner.ResumeQueue(t.Context(), c.ID)
	if err != nil || next == nil {
		t.Fatalf("ResumeQueue = %v, %v; want the queued message started", next, err)
	}
	if got := h.waitTurn(t, next.ID); got.State != chatstate.TurnDone || got.Prompt != "next" {
		t.Errorf("resumed turn = %q/%s, want the queued message done", got.Prompt, got.State)
	}
	chat := h.waitIdle(t, c.ID)
	if chat.QueuePaused || chat.QueuedCount != 0 {
		t.Errorf("after the resume paused=%v queued=%d, want false/0", chat.QueuePaused, chat.QueuedCount)
	}
}
