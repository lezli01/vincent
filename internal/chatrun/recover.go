package chatrun

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/store"
)

// identityOf is procx.Identity, indirected so a test can make the read fail —
// the same arrangement, for the same reason, as internal/taskrun's.
var identityOf = procx.Identity

// Recover finalizes the turns a previous daemon died under (spec §12.4, task
// 063 decision 5).
//
// §12.4's rule for a task is to re-run the interrupted step as an attempt that
// does not consume a retry, and that rule is safe because a fresh session over
// a surviving worktree is safe by construction. A chat turn is the case where
// that stops being true, on both halves: re-running would **re-send the
// human's message**, and the session it was resuming died with the process, so
// the "same run again" a retry promises is not available at any price.
//
// So a chat turn is finalized `interrupted` and the chat returns to idle. The
// human sees the turn that did not finish and decides whether to send it
// again — which is the one thing that cannot be wrong.
//
// The orphan kill is unchanged: the same PID-reuse guard, proving the pid
// still holds the process the row journaled before killing anything.
func (r *Runner) Recover(ctx context.Context) error {
	turns, err := r.deps.Store.ListRunningChatTurns(ctx)
	if err != nil {
		return err
	}
	for i := range turns {
		t := &turns[i]
		killOrphan(t, r.deps.Logger)
		now := r.deps.Now()
		ms := now.Sub(t.StartedAt).Milliseconds()
		t.State, t.EndedAt, t.DurationMS, t.PID = chatstate.TurnInterruptedState, &now, &ms, nil
		if err := r.deps.Store.UpdateChatTurn(ctx, t); err != nil {
			return fmt.Errorf("recover chat turn %d: %w", t.ID, err)
		}
		chat, err := r.deps.Store.GetChat(ctx, t.ChatID)
		if err != nil {
			return err
		}
		if chatstate.HoldsProcess(chat.State) {
			if _, err := r.deps.Store.SetChatState(ctx, chat.ID, chatstate.Idle); err != nil {
				return fmt.Errorf("recover chat %d: %w", chat.ID, err)
			}
		}
	}
	return nil
}

// killOrphan kills the process a recovered turn journaled, but only once the
// pid is proved to still hold it.
func killOrphan(t *store.ChatTurn, log *slog.Logger) {
	if t.PID == nil {
		return
	}
	pid := *t.PID
	if t.ProcIdentity == nil {
		// Nothing to compare: the turn crashed before the journal write, or
		// the identity read failed at spawn. "Cannot prove, do not kill" —
		// the cost of a wrong yes is killing a stranger's process.
		return
	}
	id, err := identityOf(pid)
	if err != nil || id != *t.ProcIdentity {
		return
	}
	if err := procx.KillPID(pid); err != nil {
		log.Error("recovery: kill orphaned chat process", "pid", pid, "error", err)
		return
	}
	log.Warn("recovery: killed orphaned chat process from a previous run", "pid", pid)
}
