package store

import (
	"database/sql"
	"errors"
	"testing"
)

// interruptFixture is one task in `running` with one open step run — the rows
// a crashed daemon leaves behind (§12.4).
func interruptFixture(t *testing.T) (*Store, *Task, *StepRun) {
	t.Helper()
	s := openTest(t)
	p := testProject(t, s, "p")
	task := newTask(p.ID, "t", TaskRunning)
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	pid := 4242
	run := &StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "s", StepType: "agent",
		Attempt: 1, State: StepRunning, PID: &pid,
	}
	if err := s.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	return s, task, run
}

// blockStepRunFinalize aborts exactly the write that moves a step run out of
// `running`, over a second connection so the store's own single writer is
// untouched. It is the hermetic stand-in for a storage failure at that one
// boundary — the case §12.4 never described (issue #142).
func blockStepRunFinalize(t *testing.T, s *Store) {
	t.Helper()
	db, err := sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER store_test_block_interrupt
		BEFORE UPDATE OF state ON step_runs
		WHEN NEW.state = 'interrupted'
		BEGIN SELECT RAISE(ABORT, 'injected storage failure'); END`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
}

// The whole reason InterruptTask exists: the finalize, the transition and the
// event are one commit, so recovery cannot leave a `queued` task on top of a
// step run the database still calls `running`.
func TestInterruptTaskClosesRunsAndTransitionsTogether(t *testing.T) {
	s, task, run := interruptFixture(t)

	got, ev, err := s.InterruptTask(t.Context(), task.ID, TaskRunning, TaskQueued, "interrupted")
	if err != nil {
		t.Fatalf("InterruptTask: %v", err)
	}
	if got.State != TaskQueued {
		t.Errorf("task = %s, want queued", got.State)
	}
	if ev == nil || ev.Type != EventTaskStateChanged {
		t.Errorf("event = %+v, want a task.state_changed", ev)
	}
	after, err := s.GetStepRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if after.State != StepInterrupted || after.FailureReason != "interrupted" ||
		after.PID != nil || after.FinishedAt == nil {
		t.Errorf("step run = %+v, want interrupted, pid cleared, finished", after)
	}
}

// A failure at the step-run write rolls the task move and its event back with
// it: the caller is handed rows it can retry, not a self-contradictory pair.
func TestInterruptTaskRollsBackWholeOnFailure(t *testing.T) {
	s, task, run := interruptFixture(t)
	before, err := s.ListEvents(t.Context(), EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	blockStepRunFinalize(t, s)

	if _, _, err := s.InterruptTask(
		t.Context(), task.ID, TaskRunning, TaskQueued, "interrupted"); err == nil {
		t.Fatal("InterruptTask returned nil error with the finalize write blocked")
	}

	got, err := s.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != TaskRunning {
		t.Errorf("task = %s, want it left running — the transaction did not hold", got.State)
	}
	after, err := s.GetStepRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if after.State != StepRunning {
		t.Errorf("step run = %s, want it left running", after.State)
	}
	events, err := s.ListEvents(t.Context(), EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != len(before) {
		t.Errorf("events grew from %d to %d; a rolled-back transition wrote one anyway",
			len(before), len(events))
	}
}

// Recovery may run again after a crash inside recovery. The compare-and-swap
// is what makes that converge rather than transition a second time.
func TestInterruptTaskIsIdempotent(t *testing.T) {
	s, task, run := interruptFixture(t)
	if _, _, err := s.InterruptTask(
		t.Context(), task.ID, TaskRunning, TaskQueued, "interrupted"); err != nil {
		t.Fatalf("InterruptTask: %v", err)
	}

	_, _, err := s.InterruptTask(t.Context(), task.ID, TaskRunning, TaskQueued, "interrupted")
	var conflict *StateConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("second InterruptTask err = %v, want a *StateConflictError", err)
	}
	if conflict.Got != TaskQueued {
		t.Errorf("conflict reports %s, want queued", conflict.Got)
	}
	runs, err := s.ListStepRuns(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Errorf("step runs = %+v, want the one original row", runs)
	}
}
