package chatrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// Issue #960: spec §5.5 says `idle` is "the one every finished turn returns
// it to", and a chat that failed before its agent ever started is no
// exception. runTurn's three earliest endings — no adapter, transcript.Open,
// adapter.Start — used to hand finish a nil chat, so the turn row failed and
// the chat stayed `running` forever: every later send a 409, archive refused,
// and a max_parallel_chats slot held for good.

// waitIdleAfterFailure waits a short, scaled budget for the chat to return to
// idle. finish writes the turn row before the chat, so a terminal turn is not
// yet proof — but the chat follows within milliseconds, never seconds.
func (h *harness) waitIdleAfterFailure(t *testing.T, chatID int64) {
	t.Helper()
	wait.UntilWithin(t, 5*time.Second, "chat to return to idle after a failed start", func() bool {
		got, err := h.store.GetChat(t.Context(), chatID)
		if err != nil {
			t.Fatalf("GetChat: %v", err)
		}
		return got.State == chatstate.Idle
	})
	assertSlotFree(t, h)
}

func TestEarlyTurnFailureReturnsChatToIdle(t *testing.T) {
	t.Run("agent_unavailable", func(t *testing.T) {
		h := newHarness(t)
		c := h.chatOn(t, "no-such-agent")
		turn := h.sendAndWait(t, c.ID, "hello?")
		if turn.State != chatstate.TurnFailed || turn.FailReason != ReasonAgentUnavailable {
			t.Fatalf("turn = %s/%s, want failed/%s", turn.State, turn.FailReason, ReasonAgentUnavailable)
		}
		h.waitIdleAfterFailure(t, c.ID)
	})

	t.Run("transcript_io_error", func(t *testing.T) {
		h := newHarness(t)
		c := h.chat(t)
		// A regular file where the chat's transcript directory belongs makes
		// transcript.Open fail on every OS.
		dir := h.runner.transcriptDir(c.ID)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir, []byte("in the way"), 0o600); err != nil {
			t.Fatal(err)
		}
		turn := h.sendAndWait(t, c.ID, "hello")
		if turn.State != chatstate.TurnFailed || turn.FailReason != ReasonTranscriptIOError {
			t.Fatalf("turn = %s/%s (%s), want failed/%s",
				turn.State, turn.FailReason, turn.ErrorMessage, ReasonTranscriptIOError)
		}
		h.waitIdleAfterFailure(t, c.ID)
	})

	t.Run("start_failed", func(t *testing.T) {
		h := newHarness(t)
		_, c := h.linkedTask(t, h.repo)
		h.runner.deps.Launchers = func(context.Context, int64, int64) (
			agent.Launcher, string, []string, error,
		) {
			return failingLauncher{}, "", nil, nil
		}
		turn := h.sendAndWait(t, c.ID, "hello")
		if turn.State != chatstate.TurnFailed || turn.FailReason != ReasonAgentError {
			t.Fatalf("turn = %s/%s (%s), want failed/%s",
				turn.State, turn.FailReason, turn.ErrorMessage, ReasonAgentError)
		}
		h.waitIdleAfterFailure(t, c.ID)
	})
}

// TestRecoverRepairsAChatStuckWithNoRunningTurn is the other half of #960:
// rows a released daemon already left behind — a chat `running` whose only
// turn is long since `failed` — must be repaired on restart, since Recover's
// walk over running turns never reaches them.
func TestRecoverRepairsAChatStuckWithNoRunningTurn(t *testing.T) {
	for _, stuck := range []chatstate.State{chatstate.Running, chatstate.AwaitingInput} {
		t.Run(string(stuck), func(t *testing.T) {
			h := newHarness(t)
			c := h.chat(t)
			turn, err := h.store.CreateChatTurn(t.Context(), c.ID, "never started")
			if err != nil {
				t.Fatalf("CreateChatTurn: %v", err)
			}
			now := time.Now()
			turn.State, turn.FailReason, turn.EndedAt = chatstate.TurnFailed, ReasonAgentUnavailable, &now
			if err := h.store.UpdateChatTurn(t.Context(), turn); err != nil {
				t.Fatalf("UpdateChatTurn: %v", err)
			}
			if _, err := h.store.SetChatState(t.Context(), c.ID, stuck); err != nil {
				t.Fatalf("SetChatState: %v", err)
			}

			if err := h.runner.Recover(context.Background()); err != nil {
				t.Fatalf("Recover: %v", err)
			}

			got, err := h.store.GetChat(t.Context(), c.ID)
			if err != nil {
				t.Fatalf("GetChat: %v", err)
			}
			if got.State != chatstate.Idle {
				t.Fatalf("chat state after Recover = %s, want idle", got.State)
			}
			assertSlotFree(t, h)
		})
	}
}
