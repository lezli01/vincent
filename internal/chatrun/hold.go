package chatrun

import (
	"context"
	"errors"
	"sync"

	"github.com/lezli01/vincent/internal/chatstate"
)

// ErrTurnLive is WhileNoTurn refusing because the chat has a turn: it is
// running, awaiting input, or its queue is draining.
var ErrTurnLive = errors.New("the chat has a live turn")

// startGate is one chat's start lock and how many goroutines hold or await
// it, which is what says when Runner.starts may forget it.
type startGate struct {
	mu   sync.Mutex
	refs int
}

// lockStart takes chatID's start lock and returns its release. The caller
// must not hold r.mu.
func (r *Runner) lockStart(chatID int64) (unlock func()) {
	r.mu.Lock()
	if r.starts == nil {
		r.starts = make(map[int64]*startGate)
	}
	g := r.starts[chatID]
	if g == nil {
		g = &startGate{}
		r.starts[chatID] = g
	}
	g.refs++
	r.mu.Unlock()
	g.mu.Lock()
	return func() {
		g.mu.Unlock()
		r.mu.Lock()
		if g.refs--; g.refs == 0 {
			delete(r.starts, chatID)
		}
		r.mu.Unlock()
	}
}

// WhileNoTurn runs fn while the chat provably has no turn and none can start
// (task 169 decision 7): rejecting a hunk edits the worktree with `git apply`,
// and an agent writing the same files in the same instant would make either
// one's result undefined.
//
// It is a guard, not a §5.5 transition — the chat's state never changes. The
// chat's own start lock is held throughout, so a send or a queue resume for
// this chat arriving meanwhile waits for fn rather than being refused. That
// wait is as long as fn: for a reject, a snapshot of the worktree, a diff and
// the apply, bounded by gitx.WorktreeTimeout — so a caller keeps out of fn
// whatever need not be in it, as accept keeps its diff. No other chat waits
// (review F7). A chat with an accepted turn — from Send until its actor
// goroutine returns, which covers the placement and checkpoint before a
// process exists and every handover of a draining queue — or whose row says
// it holds a process (a daemon that has not finished recovering it) is
// ErrTurnLive, and fn does not run.
func (r *Runner) WhileNoTurn(ctx context.Context, chatID int64, fn func() error) error {
	defer r.lockStart(chatID)()
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
