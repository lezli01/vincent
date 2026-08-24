package taskrun

import (
	"errors"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// admissionRounds is how many times a race test replays the interleave. One
// round is enough on most runs — the single store connection serializes the
// two goroutines so the losing order is the usual one — but a handful of
// rounds keeps the test from depending on that.
const admissionRounds = 50

// raceAdmission runs one round of "the scheduler admits the task while a
// human acts on it": a queued task, the admission CAS `internal/scheduler`
// performs (scheduler.go `start`: queued → running) and act, released
// together. It reports whether the admission committed, which is the only
// round that says anything — when the human wins instead, the scheduler is
// the one that must tolerate the conflict, and it already does.
func raceAdmission(t *testing.T, h *actionHarness, act func(int64) error) (admitted bool, actErr error) {
	t.Helper()
	task := h.task(t, store.TaskQueued)
	var (
		wg       sync.WaitGroup
		admitErr error
	)
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _, admitErr = h.store.TransitionTask(t.Context(), task.ID,
			store.TaskQueued, store.TaskRunning, store.TaskChange{})
	}()
	go func() {
		defer wg.Done()
		<-start
		actErr = act(task.ID)
	}()
	close(start)
	wg.Wait()
	if admitErr != nil {
		if _, conflict := store.AsStateConflict(admitErr); !conflict {
			t.Fatalf("admission: %v", admitErr)
		}
		return false, actErr
	}
	return true, actErr
}

// TestCancelRacingAdmission pins §6's cancel row: `cancel` is valid from
// `queued` **and** from `running`, so a cancel that the scheduler's admission
// overtakes asked for something legal before the admission and after it. It
// must not come back as a state conflict — the human cannot see the race, and
// the state they lost to is one they were entitled to act from.
//
// This is issue #127. `humanAction` (actions.go) reads the task, validates the
// action against the state it read, and hands that state to `transitionFrom`
// as the compare-and-swap's `from`; an admission landing in between fails the
// swap with "task N is running, not queued", which the API answers as 409 and
// the TUI renders as "cancel: the task is running".
func TestCancelRacingAdmission(t *testing.T) {
	h := newActionHarness(t)
	raced := 0
	for range admissionRounds {
		admitted, err := raceAdmission(t, h, func(id int64) error {
			_, err := h.runner.Cancel(t.Context(), id)
			return err
		})
		if !admitted {
			continue
		}
		raced++
		if err != nil {
			var conflict *store.StateConflictError
			if errors.As(err, &conflict) {
				t.Fatalf("cancel lost its CAS to the scheduler and gave up: %v — "+
					"§6 allows cancel from %s as well as from queued", conflict, conflict.Got)
			}
			t.Fatalf("cancel: %v", err)
		}
	}
	if raced == 0 {
		t.Fatal("the scheduler never won a round; the race was not exercised")
	}
}

// TestPauseRacingAdmission is the same claim for the other action §6 allows
// from both `queued` and a slot-holding state. Pause differs in what it does
// once it re-reads: from `running` it is deferred — the task holds at the next
// step boundary — so the task legitimately stays `running` with the request
// persisted, and only a state conflict is a failure.
func TestPauseRacingAdmission(t *testing.T) {
	h := newActionHarness(t)
	raced := 0
	for range admissionRounds {
		admitted, err := raceAdmission(t, h, func(id int64) error {
			_, err := h.runner.Pause(t.Context(), id)
			return err
		})
		if !admitted {
			continue
		}
		raced++
		if err != nil {
			var conflict *store.StateConflictError
			if errors.As(err, &conflict) {
				t.Fatalf("pause lost its CAS to the scheduler and gave up: %v — "+
					"§6 allows pause from %s as well as from queued", conflict, conflict.Got)
			}
			t.Fatalf("pause: %v", err)
		}
	}
	if raced == 0 {
		t.Fatal("the scheduler never won a round; the race was not exercised")
	}
}

// TestPauseRacingAdmissionDefers guards the half of the fix that is easy to
// get wrong: re-reading is not the same as re-swapping. `pause` from `running`
// is deferred (§6) — the step finishes first — so a pause that re-reads
// `running` must take the deferred path, not swap running → paused and park a
// task whose process is still live.
func TestPauseRacingAdmissionDefers(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskRunning)
	if _, err := h.runner.Pause(t.Context(), task.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	got := h.get(t, task.ID)
	if got.State != store.TaskRunning || !got.PauseRequested {
		t.Fatalf("pause from running: state = %s, pause_requested = %v; want running with the request persisted",
			got.State, got.PauseRequested)
	}
	if !taskstate.Can(store.TaskRunning, taskstate.Pause) {
		t.Fatal("§6 allows pause from running")
	}
}
