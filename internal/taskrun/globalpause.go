package taskrun

import (
	"context"
	"errors"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// GlobalPauseStatus is the daemon-wide pause as clients see it (task 149):
// the stored fact plus the tally that turns it into one of three states.
type GlobalPauseStatus struct {
	// State is GlobalActive, GlobalPausing or GlobalPaused.
	State string
	// Pause is the recorded pause; nil while active.
	Pause *store.GlobalPause
	Tally store.GlobalPauseTally
}

// The three states of the global pause (task 149). Only `active` and a
// recorded pause are stored; `pausing` and `paused` are the same stored
// pause told apart by whether any task is still executing a step.
const (
	GlobalActive  = "active"
	GlobalPausing = "pausing"
	GlobalPaused  = "paused"
)

// GlobalPauseState reads the pause and its tally.
func (r *Runner) GlobalPauseState(ctx context.Context) (GlobalPauseStatus, error) {
	p, err := r.deps.Store.GetGlobalPause(ctx)
	if err != nil {
		return GlobalPauseStatus{}, err
	}
	tally, err := r.deps.Store.TallyGlobalPause(ctx)
	if err != nil {
		return GlobalPauseStatus{}, err
	}
	st := GlobalPauseStatus{State: GlobalActive, Pause: p, Tally: tally}
	switch {
	case p == nil:
	case tally.Running > 0:
		st.State = GlobalPausing
	default:
		st.State = GlobalPaused
	}
	return st, nil
}

// PauseAll takes the daemon-wide pause (task 149): the scheduler stops
// admitting, every running task parks at its next step boundary — the same
// soft pause §6's `pause` is, so no step is cut short — and nothing else
// changes. Queued tasks stay queued and simply wait; tasks a human must act
// on (gates, blocks, questions) stay where they are, since they were not
// going to move without a human anyway. It is idempotent.
func (r *Runner) PauseAll(ctx context.Context, reason string) (GlobalPauseStatus, error) {
	r.globalMu.Lock()
	defer r.globalMu.Unlock()
	if _, changed, err := r.deps.Store.SetGlobalPause(ctx, reason, time.Now()); err != nil {
		return GlobalPauseStatus{}, err
	} else if changed {
		r.deps.Logger.Info("global pause taken; admission held and running tasks park at their next step boundary",
			"reason", reason)
	}
	return r.GlobalPauseState(ctx)
}

// ContinueAll lifts the daemon-wide pause (task 149) and resumes exactly the
// tasks it parked. A task a human paused by hand — before the pause or
// during it — stays paused: lifting a global pause is not a human resuming
// every task individually. Running tasks whose pause had not taken effect
// yet simply carry on. It is idempotent; a resume that loses a race to a
// human acting on the same task is logged and skipped, since the human's
// action already decided that task.
func (r *Runner) ContinueAll(ctx context.Context) (GlobalPauseStatus, int, error) {
	r.globalMu.Lock()
	defer r.globalMu.Unlock()
	ids, changed, err := r.deps.Store.ClearGlobalPause(ctx)
	if err != nil {
		return GlobalPauseStatus{}, 0, err
	}
	resumed := 0
	for _, id := range ids {
		if _, err := r.Resume(ctx, id); err != nil {
			var invalid *InvalidActionError
			if _, lost := store.AsStateConflict(err); lost || errors.As(err, &invalid) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			r.deps.Logger.Error("continue: resume task", "task", id, "error", err)
			continue
		}
		resumed++
	}
	if changed {
		r.deps.Logger.Info("global pause lifted", "resumed", resumed)
	}
	st, err := r.GlobalPauseState(ctx)
	return st, resumed, err
}
