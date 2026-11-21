package chatrun

import (
	"errors"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// TestChatHunkGuardWhileNoTurn is task 169 decision 7 at the runner: fn runs
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

// TestChatHunkGuardIsPerChat is review F7: one chat's guard — held for a
// whole reject, which is a snapshot of the worktree, a diff and an apply —
// never holds off another chat. The second chat's id is 64 past the first's
// on purpose: the lock used to be one of 64 stripes, and those two shared it.
func TestChatHunkGuardIsPerChat(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	held := h.chat(t)
	var other *store.Chat
	for other == nil || other.ID < held.ID+64 {
		other = &store.Chat{
			ProjectID: h.project.ID, Title: "another talk", State: chatstate.Idle,
			Agent: "claude", PermissionMode: string(agent.FullAuto), BaseBranch: "main",
		}
		if err := h.store.CreateChat(ctx, other); err != nil {
			t.Fatalf("CreateChat: %v", err)
		}
	}
	if other.ID != held.ID+64 {
		t.Fatalf("chat ids %d and %d are not 64 apart", held.ID, other.ID)
	}

	inside, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- h.runner.WhileNoTurn(ctx, held.ID, func() error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside
	ran := make(chan error, 1)
	go func() { ran <- h.runner.WhileNoTurn(ctx, other.ID, func() error { return nil }) }()
	select {
	case err := <-ran:
		if err != nil {
			t.Fatalf("the other chat's guard: %v", err)
		}
	case <-time.After(wait.Timeout(10 * time.Second)):
		t.Fatal("another chat's guard waited for this one's")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("WhileNoTurn: %v", err)
	}
	// A lock lives only as long as someone holds or awaits it.
	h.runner.mu.Lock()
	left := len(h.runner.starts)
	h.runner.mu.Unlock()
	if left != 0 {
		t.Errorf("%d start locks left behind", left)
	}
}
