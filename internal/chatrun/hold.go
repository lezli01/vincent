package chatrun

import (
	"context"
	"errors"
	"sync"

	"github.com/lezli01/vincent/internal/chatstate"
)

// startStripes is how many locks Runner.starts spreads chats over.
const startStripes = 64

// ErrTurnLive is WhileNoTurn refusing because the chat has a turn: it is
// running, awaiting input, or its queue is draining.
var ErrTurnLive = errors.New("the chat has a live turn")

func (r *Runner) startLock(chatID int64) *sync.Mutex {
	return &r.starts[uint64(chatID)%startStripes] //nolint:gosec // G115: a stripe index; any wrap is as good as any other
}

// WhileNoTurn runs fn while the chat provably has no turn and none can start
// (task 168 decision 7): rejecting a hunk edits the worktree with `git apply`,
// and an agent writing the same files in the same instant would make either
// one's result undefined.
//
// It is a guard, not a §5.5 transition — the chat's state never changes. The
// chat's start lock is held throughout, so a send or a queue resume arriving
// meanwhile waits for fn rather than being refused; the wait is one git
// command long. A chat with an accepted turn — from Send until its actor
// goroutine returns, which covers the placement and checkpoint before a
// process exists and every handover of a draining queue — or whose row says
// it holds a process (a daemon that has not finished recovering it) is
// ErrTurnLive, and fn does not run.
func (r *Runner) WhileNoTurn(ctx context.Context, chatID int64, fn func() error) error {
	lock := r.startLock(chatID)
	lock.Lock()
	defer lock.Unlock()
	r.mu.Lock()
	_, accepted := r.turns[chatID]
	r.mu.Unlock()
	if accepted {
		return ErrTurnLive
	}
	chat, err := r.deps.Store.GetChat(ctx, chatID)
	if err != nil {
		return err
	}
	if chatstate.HoldsProcess(chat.State) {
		return ErrTurnLive
	}
	return fn()
}
