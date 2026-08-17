package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestTransitionTaskWritesStateAndEventTogether(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "work", TaskQueued)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, ev, err := s.TransitionTask(ctx, task.ID, TaskQueued, TaskRunning, TaskChange{})
	if err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if got.State != TaskRunning {
		t.Errorf("state = %s, want running", got.State)
	}
	if got.StartedAt == nil {
		t.Error("started_at was not stamped on the first run")
	}
	if ev == nil || ev.ID == 0 || ev.Type != EventTaskStateChanged {
		t.Fatalf("event = %+v, want a persisted task.state_changed", ev)
	}
	if ev.TaskID == nil || *ev.TaskID != task.ID || ev.ProjectID == nil || *ev.ProjectID != p.ID {
		t.Errorf("event ids = %+v, want task %d project %d", ev, task.ID, p.ID)
	}

	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["from"] != "queued" || payload["to"] != "running" {
		t.Errorf("payload = %v, want from queued to running", payload)
	}

	// The event is in the table, readable by an SSE catch-up query.
	events, err := s.ListEvents(ctx, EventFilter{Types: []string{EventTaskStateChanged}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0].ID != ev.ID {
		t.Fatalf("events = %+v, want exactly the one just written", events)
	}

	// Reloading confirms the state was committed, not just returned.
	reloaded, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if reloaded.State != TaskRunning {
		t.Errorf("reloaded state = %s, want running", reloaded.State)
	}
}

func TestTransitionTaskConflictLeavesNothingBehind(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "work", TaskRunning)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	_, _, err := s.TransitionTask(ctx, task.ID, TaskQueued, TaskRunning, TaskChange{})
	conflict, ok := AsStateConflict(err)
	if !ok {
		t.Fatalf("err = %v, want a StateConflictError", err)
	}
	if conflict.Got != TaskRunning || conflict.Want != TaskQueued || conflict.TaskID != task.ID {
		t.Errorf("conflict = %+v, want task %d want=queued got=running", conflict, task.ID)
	}

	// A failed transition writes no event: the rollback covers both writes.
	events, err := s.ListEvents(ctx, EventFilter{Types: []string{EventTaskStateChanged}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("events = %+v, want none after a conflicted transition", events)
	}
}

func TestTransitionTaskMissingTask(t *testing.T) {
	s := openTest(t)
	_, _, err := s.TransitionTask(t.Context(), 9999, TaskQueued, TaskRunning, TaskChange{})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestTransitionTaskAppliesChanges(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "work", TaskRunning)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	reason := "nonzero_exit"
	step := 2
	worktree := "/data/worktrees/1"
	snapshot := "name: edited\nsteps: []\n"
	got, ev, err := s.TransitionTask(ctx, task.ID, TaskRunning, TaskBlocked, TaskChange{
		BlockReason:  &reason,
		CurrentStep:  &step,
		WorktreePath: &worktree,
		Snapshot:     &snapshot,
		EventPayload: map[string]any{"step_id": "implement"},
	})
	if err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if got.BlockReason != reason || got.CurrentStep != step ||
		got.WorktreePath != worktree || got.WorkflowSnapshot != snapshot {
		t.Errorf("task = %+v, want the change applied", got)
	}

	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["block_reason"] != reason || payload["step_id"] != "implement" {
		t.Errorf("payload = %v, want block_reason and the extra field", payload)
	}

	// Leaving blocked clears the reason without the caller asking.
	got, _, err = s.TransitionTask(ctx, task.ID, TaskBlocked, TaskQueued, TaskChange{})
	if err != nil {
		t.Fatalf("TransitionTask(retry): %v", err)
	}
	if got.BlockReason != "" {
		t.Errorf("block_reason = %q after leaving blocked, want empty", got.BlockReason)
	}
}

func TestTransitionTaskTimestamps(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	task := newTask(p.ID, "work", TaskQueued)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	running, _, err := s.TransitionTask(ctx, task.ID, TaskQueued, TaskRunning, TaskChange{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	firstStart := running.StartedAt

	// An interrupted task is re-queued and re-admitted; started_at keeps the
	// original first-run stamp (§6 timestamps are about the task, not the
	// attempt).
	if _, _, err := s.TransitionTask(ctx, task.ID, TaskRunning, TaskQueued, TaskChange{}); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	running, _, err = s.TransitionTask(ctx, task.ID, TaskQueued, TaskRunning, TaskChange{})
	if err != nil {
		t.Fatalf("re-admit: %v", err)
	}
	if running.StartedAt == nil || !running.StartedAt.Equal(*firstStart) {
		t.Errorf("started_at = %v, want the first run's %v", running.StartedAt, firstStart)
	}

	done, _, err := s.TransitionTask(ctx, task.ID, TaskRunning, TaskDone, TaskChange{})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.FinishedAt == nil {
		t.Error("finished_at was not stamped on done")
	}
	archived, _, err := s.TransitionTask(ctx, task.ID, TaskDone, TaskArchived, TaskChange{})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.ArchivedAt == nil {
		t.Error("archived_at was not stamped on archive")
	}
}

// TestCountStepAttemptsCursorBoundsOnlyFailures: the retry cursor resets the
// budget without restarting attempt numbering — a bounded MAX(attempt) would
// collide transcript filenames and truncate earlier attempts.
func TestCountStepAttemptsCursorBoundsOnlyFailures(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "work", TaskBlocked)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	base := time.Now().Add(-time.Hour)
	for i := 1; i <= 3; i++ {
		run := &StepRun{
			TaskID: task.ID, StepIndex: 0, StepID: "step", StepType: "command",
			Attempt: i, State: StepFailed,
			StartedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := s.CreateStepRun(ctx, run); err != nil {
			t.Fatalf("CreateStepRun: %v", err)
		}
	}

	cursor := base.Add(2*time.Minute + 30*time.Second) // after attempt 2, before 3
	got, err := s.CountStepAttempts(ctx, task.ID, 0, "step", cursor)
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if got.Last != 3 {
		t.Errorf("Last = %d, want 3 — attempt numbers must not restart at the cursor", got.Last)
	}
	if got.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — only failures after the cursor count", got.Failed)
	}

	all, err := s.CountStepAttempts(ctx, task.ID, 0, "step", time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts (zero cursor): %v", err)
	}
	if all.Last != 3 || all.Failed != 3 {
		t.Errorf("zero cursor = %+v, want Last 3 / Failed 3", all)
	}
}
