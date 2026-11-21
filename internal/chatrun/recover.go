package chatrun

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

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
		chat, err := r.deps.Store.GetChat(ctx, t.ChatID)
		if err != nil {
			return err
		}
		// A linked turn on a containerized task ran inside the task's
		// container, where the host pid is only the runtime client's: the
		// agent is reached through its pid file (task 119 decision 3, 061
		// decision 9). The host kill still runs for the client.
		if chat.Linked() && r.deps.StopOrphan != nil {
			r.deps.StopOrphan(ctx, *chat.LinkedTaskID, t.ID)
		}
		killOrphan(t, r.deps.Logger)
		now := r.deps.Now()
		ms := now.Sub(t.StartedAt).Milliseconds()
		t.State, t.EndedAt, t.DurationMS, t.PID = chatstate.TurnInterruptedState, &now, &ms, nil
		if err := r.deps.Store.UpdateChatTurn(ctx, t); err != nil {
			return fmt.Errorf("recover chat turn %d: %w", t.ID, err)
		}
		// The chat returns to idle and stays open, so a task it locked
		// stays locked with no extra code (task 119).
		if chatstate.HoldsProcess(chat.State) {
			if _, err := r.deps.Store.SetChatState(ctx, chat.ID, chatstate.Idle); err != nil {
				return fmt.Errorf("recover chat %d: %w", chat.ID, err)
			}
		}
		// The rest of the queue waits for a human (task 156 decision 3): a
		// restart drains nothing on its own.
		if err := r.deps.Store.PauseChatQueue(ctx, chat.ID, store.QueuePauseInterrupted); err != nil {
			return fmt.Errorf("recover chat %d queue: %w", chat.ID, err)
		}
	}
	if err := r.releaseStuckChats(ctx); err != nil {
		return err
	}
	if err := r.recoverShells(ctx); err != nil {
		return err
	}
	return r.sweepSkillProbes(ctx)
}

// releaseStuckChats returns to idle every chat still holding a process once
// the walk above has finalized every running turn (#960). Such a chat has no
// running turn at all — an earlier daemon failed its turn before the process
// was up and never moved the chat back — so nothing will ever release it, and
// it would refuse every send and hold a §11 cap slot forever. Recover runs
// before any turn is admitted, so there is no live turn to race. The chat
// stays open, so a task it locks stays locked.
func (r *Runner) releaseStuckChats(ctx context.Context) error {
	chats, err := r.deps.Store.ListChats(ctx, store.ChatFilter{
		States: []chatstate.State{chatstate.Running, chatstate.AwaitingInput},
	})
	if err != nil {
		return err
	}
	for i := range chats {
		if _, err := r.deps.Store.SetChatState(ctx, chats[i].ID, chatstate.Idle); err != nil {
			return fmt.Errorf("recover chat %d: %w", chats[i].ID, err)
		}
		if err := r.deps.Store.PauseChatQueue(ctx, chats[i].ID, store.QueuePauseInterrupted); err != nil {
			return fmt.Errorf("recover chat %d queue: %w", chats[i].ID, err)
		}
	}
	return nil
}

// skillProbeSweepWorkers bounds how many probe kills run at once (task 124.17
// decision 4). Each is a runtime lookup, a TERM, the probe's short grace and a
// KILL, so a serial sweep would add that per open linked chat to every
// restart; the workers keep it flat without letting a board with hundreds of
// chats spawn hundreds of goroutines. Eight is well above the number of chats
// anyone has open on containerized tasks at once, and a worker costs a
// sleeping goroutine and one runtime client.
const skillProbeSweepWorkers = 8

// sweepSkillProbes kills the skill probes a previous daemon died under (spec
// §12.4, task 124.17 decision 4).
//
// A probe is not a turn. It writes no row, so the walk over running turns
// above — which is how a turn's orphan is found — cannot find one. What it
// does have is a pid file named after its chat, inside a container that
// outlives the daemon (061 decision 9), so the sweep is over the chats that
// could have left one: the open ones linked to a task. StopSkillProbe answers
// false for a task that runs on the host, where the dead daemon's own child
// is already gone, and for one whose container has since been removed.
//
// Signalling a chat that had no probe running is a pid file that is not
// there, which the runtime reports and the kill logs; sweeping only the chats
// that did would need a row this deliberately does not write.
func (r *Runner) sweepSkillProbes(ctx context.Context) error {
	if r.deps.StopSkillProbe == nil {
		return nil
	}
	chats, err := r.deps.Store.ListChats(ctx, store.ChatFilter{
		States: []chatstate.State{chatstate.Idle, chatstate.Running, chatstate.AwaitingInput},
	})
	if err != nil {
		return err
	}
	sem := make(chan struct{}, skillProbeSweepWorkers)
	var wg sync.WaitGroup
	for i := range chats {
		c := &chats[i]
		if !c.Linked() {
			continue
		}
		taskID, chatID := *c.LinkedTaskID, c.ID
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.deps.StopSkillProbe(ctx, taskID, chatID)
		}()
	}
	wg.Wait()
	return nil
}

// killOrphan kills the process a recovered turn journaled, but only once the
// pid is proved to still hold it.
func killOrphan(t *store.ChatTurn, log *slog.Logger) { killOrphanPID(t.PID, t.ProcIdentity, log) }

// killOrphanPID is the guard itself, shared by a recovered turn and a
// recovered shell run (task 170).
func killOrphanPID(pidp *int, ident *string, log *slog.Logger) {
	if pidp == nil {
		return
	}
	pid := *pidp
	if ident == nil {
		// Nothing to compare: the row crashed before the journal write, or
		// the identity read failed at spawn. "Cannot prove, do not kill" —
		// the cost of a wrong yes is killing a stranger's process.
		return
	}
	id, err := identityOf(pid)
	if err != nil || id != *ident {
		return
	}
	if err := procx.KillPID(pid); err != nil {
		log.Error("recovery: kill orphaned chat process", "pid", pid, "error", err)
		return
	}
	log.Warn("recovery: killed orphaned chat process from a previous run", "pid", pid)
}
