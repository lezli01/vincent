package chatrun

import (
	"errors"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// TestChatHunkGuardWhileNoTurn is task 168 decision 7 at the runner: fn runs
// on a chat at rest and holds off a send until it returns, and is refused
// while a turn is accepted.
func TestChatHunkGuardWhileNoTurn(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	chat := h.chat(t)
	ctx := t.Context()

	// At rest: fn runs, and a send that arrives meanwhile waits for it.
	inside, release, sent := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- h.runner.WhileNoTurn(ctx, chat.ID, func() error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside
	go func() {
		_, err := h.runner.Send(ctx, chat.ID, "hold")
		sent <- err
	}()
	select {
	case err := <-sent:
		t.Fatalf("a send got through while the guard was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if c, err := h.store.GetChat(ctx, chat.ID); err != nil || c.State != chatstate.Idle {
		t.Fatalf("chat under the guard = %+v, %v; want idle", c, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("WhileNoTurn: %v", err)
	}
	if err := <-sent; err != nil {
		t.Fatalf("Send after the guard: %v", err)
	}

	// The turn is accepted now — before it has a process, too — so the
	// guard refuses and fn never runs.
	err := h.runner.WhileNoTurn(ctx, chat.ID, func() error {
		t.Error("fn ran during a live turn")
		return nil
	})
	if !errors.Is(err, ErrTurnLive) {
		t.Fatalf("WhileNoTurn during a turn = %v, want ErrTurnLive", err)
	}
	if err := h.runner.Cancel(ctx, chat.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	h.waitIdle(t, chat.ID)
	// fn's own error comes back as it is.
	boom := errors.New("boom")
	h.runner.StopTurn(ctx, chat.ID)
	if err := h.runner.WhileNoTurn(ctx, chat.ID, func() error { return boom }); !errors.Is(err, boom) {
		t.Errorf("WhileNoTurn = %v, want fn's error", err)
	}
}
