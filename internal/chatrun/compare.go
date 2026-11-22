package chatrun

import (
	"context"
	"fmt"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// SubmitPair starts one turn on each of two idle chats with the same prompt
// — a compare's two sides (task 171 decision 6) — or on neither. Both
// chats' start locks are held, in id order so two pairs cannot deadlock,
// from the reads that find them idle to both launches; the capacity check
// asks for two slots before either turn row is written. Unlike Submit it
// never queues: a side that is not idle is a refusal, because a queued
// message would not answer the compare's prompt at the same point.
func (r *Runner) SubmitPair(ctx context.Context, ids [2]int64, prompt string) ([2]*store.ChatTurn, error) {
	var out [2]*store.ChatTurn
	if ids[0] == ids[1] {
		return out, fmt.Errorf("a pair needs two chats, got chat %d twice", ids[0])
	}
	lo, hi := min(ids[0], ids[1]), max(ids[0], ids[1])
	defer r.lockStart(lo)()
	defer r.lockStart(hi)()
	var chats [2]*store.Chat
	for i, id := range ids {
		r.mu.Lock()
		_, accepted := r.turns[id]
		r.mu.Unlock()
		if accepted {
			return out, ErrTurnLive
		}
		chat, err := r.deps.Store.GetChat(ctx, id)
		if err != nil {
			return out, err
		}
		if chat.State != chatstate.Idle || !chatstate.AllowedFor(chat.Kind(), chat.State, chatstate.Send) {
			return out, fmt.Errorf("chat %d is %s, not idle: %w", id, chat.State, store.ErrInvalidChatAction)
		}
		chats[i] = chat
	}
	if err := r.CheckCapacity(ctx, 2); err != nil {
		return out, err
	}
	for i, id := range ids {
		turn, err := r.deps.Store.CreateChatTurnWith(ctx, id, prompt, store.TurnSettings{})
		if err != nil {
			if i == 1 {
				// The first side's row is written and the second's is not:
				// launch what exists rather than leave a turn row that no
				// actor will ever run. The caller reports the failure.
				r.launch(chats[0], out[0])
			}
			return out, err
		}
		out[i] = turn
	}
	r.launch(chats[0], out[0])
	r.launch(chats[1], out[1])
	return out, nil
}
