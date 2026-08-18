package taskrun

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// startTimeTolerance bounds how far a live process's start time may sit from
// the journaled spawn time and still be treated as the same process. The
// journaled time is the daemon's wall clock just after spawn and the OS
// reports kernel bookkeeping (tick/btime conversion on Linux), so the two
// legitimately differ by a little; a recycled PID differs by the whole
// lifetime of the daemon that died (PR D decision: ±5 s).
const startTimeTolerance = 5 * time.Second

// Recover is §12.4's startup crash recovery, run by the daemon before the
// scheduler or runner start. Every step run a previous daemon process left
// `running` is finalized as `interrupted` — after tree-killing its recorded
// process if, and only if, the PID still exists and its start time matches
// the journal (the PID-reuse guard). Each owning task then re-queues at its
// current step through the FSM's Interrupt action: the durable event fires,
// the retry budget is untouched (§7.2), and tasks found in `awaiting_input`
// are treated identically (§7.4). Returns how many tasks were re-queued.
//
// This replaces T1.8's blocking sweep, whose bulk UPDATE also bypassed the
// TransitionTask compare-and-swap — the invariant that no transition skips
// the FSM holds here too.
func Recover(ctx context.Context, st *store.Store, log *slog.Logger) (int, error) {
	runs, err := st.ListRunningStepRuns(ctx)
	if err != nil {
		return 0, err
	}
	for i := range runs {
		run := &runs[i]
		killOrphan(run, log.With("task", run.TaskID, "run", run.ID))
		now := time.Now()
		run.State = store.StepInterrupted
		run.FailureReason = ReasonInterrupted
		run.PID = nil
		run.FinishedAt = &now
		if err := st.UpdateStepRun(ctx, run); err != nil {
			log.Error("recovery: finalize step run", "run", run.ID, "error", err)
		}
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
			if _, _, err := st.TransitionTask(ctx, tasks[i].ID, state, tr.To, store.TaskChange{}); err != nil {
				log.Error("recovery: re-queue task", "task", tasks[i].ID, "error", err)
				continue
			}
			log.Warn("recovered task from a previous run; re-queued",
				"task", tasks[i].ID, "was", string(state), "step", tasks[i].CurrentStep)
			requeued++
		}
	}
	return requeued, nil
}

// killOrphan tree-kills the process a running step run journaled, when it is
// demonstrably still that process. A dead PID is the good case; a PID whose
// start time does not match belongs to an innocent stranger and is left
// alone (§12.4).
func killOrphan(run *store.StepRun, log *slog.Logger) {
	if run.PID == nil || run.ProcStartedAt == nil {
		return
	}
	pid := *run.PID
	started, err := procx.StartTime(pid)
	if errors.Is(err, procx.ErrProcessGone) {
		return
	}
	if err != nil {
		// Can't prove identity — do not kill what we cannot identify.
		log.Warn("recovery: process start time unavailable; leaving PID alone", "pid", pid, "error", err)
		return
	}
	diff := started.Sub(*run.ProcStartedAt)
	if diff < 0 {
		diff = -diff
	}
	if diff > startTimeTolerance {
		log.Warn("recovery: PID was reused by another process; leaving it alone",
			"pid", pid, "journaled", run.ProcStartedAt, "actual", started)
		return
	}
	if err := procx.KillPID(pid); err != nil {
		log.Error("recovery: kill orphaned process", "pid", pid, "error", err)
		return
	}
	log.Warn("recovery: killed orphaned process from a previous run", "pid", pid)
}
