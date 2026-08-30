package taskrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// startTimeTolerance bounds how far a live process's start time may sit from
// the journaled spawn time and still be treated as the same process. The
// journaled time is the daemon's wall clock just after spawn and the OS
// reports kernel bookkeeping (tick/btime conversion on Linux), so the two
// legitimately differ by a little; a recycled PID differs by the whole
// lifetime of the daemon that died.
//
// This is the **legacy fallback**, not the guard. PR D's ±5 s rule (grill
// session 2026-08-08, docs/history/v0-tasks.md) was superseded by issue #149
// and task 031: a step run journals procx.Identity beside its PID, and
// recovery compares that token exactly. The tolerance survives only for rows
// that carry no identity — written before migration 0013, or by a spawn whose
// identity read failed — where it is still strictly better than nothing.
const startTimeTolerance = 5 * time.Second

// identityOf is procx.Identity, indirected so a test can make the read fail.
// "Cannot prove, do not kill" is the invariant that matters most on the path
// where the proof itself is unavailable, and there is no other way to reach
// that branch deterministically.
var identityOf = procx.Identity

// journalIdentity reads the native identity of a process just spawned, for the
// PID column's sake. A read that fails is journaled as no identity at all: the
// step runs regardless, and its row falls back to the start-time tolerance —
// a workspace where the read never works is exactly as safe as it was before
// issue #149, and no safer. The failure is logged because a host that cannot
// read process identity at all is worth knowing about.
func journalIdentity(pid int, log *slog.Logger) *string {
	id, err := identityOf(pid)
	if err != nil {
		log.Warn("process identity unavailable; the PID-reuse guard falls back to the start-time tolerance",
			"pid", pid, "error", err)
		return nil
	}
	return &id
}

// Recover is §12.4's startup crash recovery, run by the daemon before the
// scheduler or runner start. Every step run a previous daemon process left
// `running` is finalized as `interrupted` — after tree-killing its recorded
// process if, and only if, the PID still exists and still holds the process
// the row journaled (the PID-reuse guard). Each owning task then re-queues at its
// current step through the FSM's Interrupt action: the durable event fires,
// the retry budget is untouched (§7.2), and tasks found in `awaiting_input`
// are treated identically (§7.4). Returns how many tasks were re-queued.
//
// It works one task at a time, and that is the whole point (issue #142).
// Finalizing a task's runs and re-queueing it is a single store transaction
// (store.InterruptTask), so the two halves of §12.4's order can never come
// apart: recovery cannot hand the scheduler a `queued` task whose previous
// attempt is still, durably, `running`. A task whose transaction will not
// commit is left exactly as found — recoverable, not re-queued, not counted —
// and the failure is returned so the daemon fails startup on it. Continuing
// past a storage failure is least defensible precisely when storage is
// failing.
//
// This replaces T1.8's blocking sweep, whose bulk UPDATE also bypassed the
// TransitionTask compare-and-swap — the invariant that no transition skips
// the FSM holds here too.
func Recover(ctx context.Context, st *store.Store, log *slog.Logger) (int, error) {
	runs, err := st.ListRunningStepRuns(ctx)
	if err != nil {
		return 0, err
	}
	// The open runs, grouped by the task that owns them, so recovery is driven
	// per task rather than as two independent sweeps whose outcomes nothing
	// connects. owners preserves the id ASC order the query returned.
	open := make(map[int64][]*store.StepRun, len(runs))
	var owners []int64
	for i := range runs {
		run := &runs[i]
		if _, seen := open[run.TaskID]; !seen {
			owners = append(owners, run.TaskID)
		}
		open[run.TaskID] = append(open[run.TaskID], run)
	}

	requeued := 0
	for _, state := range []store.TaskState{store.TaskRunning, store.TaskAwaitingInput} {
		tr, ok := taskstate.Next(state, taskstate.Interrupt)
		if !ok {
			continue // the FSM defines Interrupt from both; belt and braces
		}
		// ChildrenInclude explicitly: recovery is about every task the daemon
		// left running, and a fan-out lane is one of those (task 014). The
		// list default hides lanes for the board's benefit, which is exactly
		// the wrong behaviour here.
		tasks, err := st.ListTasks(ctx, store.TaskFilter{State: state, Children: store.ChildrenInclude})
		if err != nil {
			return requeued, err
		}
		for i := range tasks {
			id := tasks[i].ID
			// Killing happens before the transaction and outside it: a kill
			// cannot be rolled back, and killing then failing to commit is
			// the already-tolerated case — the row stays `running` and the
			// next recovery finds a dead PID.
			killOwned(open[id], id, log)
			delete(open, id)
			if _, _, err := st.InterruptTask(ctx, id, state, tr.To, ReasonInterrupted); err != nil {
				return requeued, fmt.Errorf("recover task %d from %s: %w", id, state, err)
			}
			log.Warn("recovered task from a previous run; re-queued",
				"task", id, "was", string(state), "step", tasks[i].CurrentStep)
			requeued++
		}
	}

	// What is left owns a `running` step run while sitting in a state that
	// re-queues through no Interrupt transition — an `awaiting_gate` task's
	// manual row, whose actor wrote it before exiting (§6). There is no task
	// move to pair the write with, so the rows are finalized on their own.
	for _, id := range owners {
		runs, ok := open[id]
		if !ok {
			continue // already reconciled, with its task, above
		}
		killOwned(runs, id, log)
		if _, err := st.TerminalizeOpenStepRuns(ctx, id, store.StepInterrupted, ReasonInterrupted); err != nil {
			return requeued, fmt.Errorf("recover task %d: finalize open step runs: %w", id, err)
		}
	}
	if err := sweepCursorMCP(ctx, st, log); err != nil {
		return requeued, err
	}
	return requeued, nil
}

// sweepCursorMCP removes the `.cursor/mcp.json` a cursor step writes into its
// task worktree (§9.7, §12.4, task 057). The adapter removes it in Wait; a
// daemon that died mid-step never got there, and the file is untracked inside
// a git worktree — so a leftover shows up in `git status`, in the task diff
// and in dirty detection, on a task that is about to be re-queued.
//
// It sweeps every live task's worktree rather than only the ones that were
// running: the file is only ever written by a run, so removing it where there
// was none costs a failed os.Remove, and the crash that left it behind is
// exactly the case where the step_run rows may not say which task had it.
//
// A removal failure is logged, not returned: recovery's job is to get the
// daemon running again, and a stale config file is a nuisance rather than a
// correctness problem — its token died with the daemon that minted it.
func sweepCursorMCP(ctx context.Context, st *store.Store, log *slog.Logger) error {
	claims, err := st.ListWorktreeClaims(ctx)
	if err != nil {
		return fmt.Errorf("recover: list worktree claims: %w", err)
	}
	for _, c := range claims {
		if c.Path == "" {
			continue
		}
		if err := agent.RemoveCursorMCPConfig(c.Path); err != nil {
			log.Warn("remove leftover cursor mcp config",
				"task", c.TaskID, "path", c.Path, "error", err)
		}
	}
	return nil
}

// killOwned tree-kills the journaled process of each of one task's open runs.
func killOwned(runs []*store.StepRun, taskID int64, log *slog.Logger) {
	for _, run := range runs {
		killOrphan(run, log.With("task", taskID, "run", run.ID))
	}
}

// killOrphan tree-kills the process a running step run journaled, when it is
// demonstrably still that process. A dead PID is the good case; a PID that
// now holds somebody else belongs to an innocent stranger and is left alone
// (§12.4).
func killOrphan(run *store.StepRun, log *slog.Logger) {
	if run.PID == nil {
		return
	}
	pid := *run.PID
	if !stillTheJournaledProcess(run, pid, log) {
		return
	}
	if err := procx.KillPID(pid); err != nil {
		log.Error("recovery: kill orphaned process", "pid", pid, "error", err)
		return
	}
	log.Warn("recovery: killed orphaned process from a previous run", "pid", pid)
}

// stillTheJournaledProcess answers §12.4's only question about a recorded PID:
// is the process holding it now the one this row spawned? It answers false
// whenever it cannot prove otherwise — a gone PID, an unreadable identity, a
// row that journaled nothing to compare — because the cost of a wrong "yes"
// is killing a stranger's process and the cost of a wrong "no" is a stray
// orphan the next recovery may still reap.
func stillTheJournaledProcess(run *store.StepRun, pid int, log *slog.Logger) bool {
	if run.ProcIdentity != nil {
		return identityStillHolds(pid, *run.ProcIdentity, log)
	}
	if run.ProcStartedAt == nil {
		return false // crashed before the journal write; nothing to compare
	}
	return startTimeStillHolds(pid, *run.ProcStartedAt, log)
}

// identityStillHolds compares the platform-native identity journaled at spawn
// with the one the OS reports for that PID now — byte for byte, never parsed
// (issue #149). A reused PID reports a different token by construction, and a
// reboot changes it too where the token carries a boot id.
func identityStillHolds(pid int, journaled string, log *slog.Logger) bool {
	live, err := identityOf(pid)
	if errors.Is(err, procx.ErrProcessGone) {
		return false
	}
	if err != nil {
		// Can't prove identity — do not kill what we cannot identify.
		log.Warn("recovery: process identity unavailable; leaving PID alone", "pid", pid, "error", err)
		return false
	}
	if live != journaled {
		log.Warn("recovery: PID now belongs to a different process; leaving it alone",
			"pid", pid, "journaled", journaled, "actual", live)
		return false
	}
	return true
}

// startTimeStillHolds is the pre-#149 guard, kept for rows with no journaled
// identity: two clocks compared within startTimeTolerance.
func startTimeStillHolds(pid int, journaled time.Time, log *slog.Logger) bool {
	started, err := procx.StartTime(pid)
	if errors.Is(err, procx.ErrProcessGone) {
		return false
	}
	if err != nil {
		log.Warn("recovery: process start time unavailable; leaving PID alone", "pid", pid, "error", err)
		return false
	}
	diff := started.Sub(journaled)
	if diff < 0 {
		diff = -diff
	}
	if diff > startTimeTolerance {
		log.Warn("recovery: PID was reused by another process; leaving it alone",
			"pid", pid, "journaled", journaled, "actual", started)
		return false
	}
	return true
}
