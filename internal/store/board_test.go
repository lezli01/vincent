package store

import (
	"encoding/json"
	"testing"
)

// TestSetTaskProgressEmitsStepAdvanced covers the event a board's k/n column
// depends on: the engine moves the cursor without a state change, so without
// this event the step counter would sit at the starting step for the whole
// run.
func TestSetTaskProgressEmitsStepAdvanced(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "work", TaskRunning)
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	var got *Event
	s.SetEventHook(func(e *Event) {
		if e.Type == EventTaskStepAdvanced {
			got = e
		}
	})

	step := 3
	if err := s.SetTaskProgress(ctx, task.ID, &step, nil); err != nil {
		t.Fatalf("SetTaskProgress: %v", err)
	}
	if got == nil {
		t.Fatal("no task.step_advanced reached the event hook")
	}
	if got.ID == 0 {
		t.Error("event was not persisted (no id), so an SSE resume would miss it")
	}
	if got.TaskID == nil || *got.TaskID != task.ID {
		t.Errorf("task id = %v, want %d", got.TaskID, task.ID)
	}
	if got.ProjectID == nil || *got.ProjectID != p.ID {
		t.Errorf("project id = %v, want %d — ?project_id= could not filter it", got.ProjectID, p.ID)
	}

	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["current_step"] != float64(step) {
		t.Errorf("payload = %v, want current_step %d", payload, step)
	}

	// The cursor actually moved, and the event is queryable for catch-up.
	after, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if after.CurrentStep != step {
		t.Errorf("current_step = %d, want %d", after.CurrentStep, step)
	}
	events, err := s.ListEvents(ctx, EventFilter{Types: []string{EventTaskStepAdvanced}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("listed %d step-advance events, want 1", len(events))
	}
}

// TestSetTaskProgressWorktreeOnlyIsSilent guards the other half of the rule.
// Recording the worktree path is bookkeeping no client renders; emitting an
// advance for it would make k/n jump on a step that never ran.
func TestSetTaskProgressWorktreeOnlyIsSilent(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "work", TaskRunning)
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	fired := 0
	s.SetEventHook(func(e *Event) {
		if e.Type == EventTaskStepAdvanced {
			fired++
		}
	})

	path := "/tmp/wt"
	if err := s.SetTaskProgress(ctx, task.ID, nil, &path); err != nil {
		t.Fatalf("SetTaskProgress: %v", err)
	}
	if fired != 0 {
		t.Errorf("worktree-only write emitted %d step-advance events, want 0", fired)
	}
	events, err := s.ListEvents(ctx, EventFilter{Types: []string{EventTaskStepAdvanced}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("persisted %d step-advance events, want 0", len(events))
	}

	after, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if after.WorktreePath != path {
		t.Errorf("worktree_path = %q, want %q", after.WorktreePath, path)
	}
}

// TestTaskRollupsSumsEveryAttempt is the §17 rule the board reports: a step
// that failed twice before succeeding cost money three times.
func TestTaskRollupsSumsEveryAttempt(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "work", TaskRunning)
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	other := newTask(p.ID, "other", TaskRunning)
	if err := s.CreateTask(ctx, other); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	for _, c := range []struct {
		attempt       int
		state         StepRunState
		cost          float64
		inTok, outTok int64
	}{
		{1, StepFailed, 0.12, 100, 10},
		{2, StepFailed, 0.15, 120, 12},
		{3, StepSucceeded, 0.14, 130, 14},
	} {
		cost, in, out := c.cost, c.inTok, c.outTok
		run := &StepRun{
			TaskID: task.ID, StepIndex: 0, StepID: "s", StepType: "agent",
			Attempt: c.attempt, State: c.state,
			CostUSD: &cost, InputTokens: &in, OutputTokens: &out,
		}
		if err := s.CreateStepRun(ctx, run); err != nil {
			t.Fatalf("CreateStepRun: %v", err)
		}
	}

	got, err := s.TaskRollups(ctx, []int64{task.ID, other.ID})
	if err != nil {
		t.Fatalf("TaskRollups: %v", err)
	}
	r, ok := got[task.ID]
	if !ok {
		t.Fatal("no rollup for the task with step runs")
	}
	// 0.12 + 0.15 + 0.14, compared with tolerance: these are floats.
	if diff := r.CostUSD - 0.41; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost = %v, want 0.41 (every attempt, not just the surviving one)", r.CostUSD)
	}
	if !r.HasCost {
		t.Error("HasCost = false though every attempt reported a cost")
	}
	if r.InputTokens != 350 || r.OutputTokens != 36 {
		t.Errorf("tokens = %d/%d, want 350/36", r.InputTokens, r.OutputTokens)
	}
	if _, ok := got[other.ID]; ok {
		t.Error("a task with no step runs should be absent, not a zero row")
	}
}

// TestTaskRollupsDistinguishesFreeFromUnreported keeps an adapter that never
// reports cost (§9) from rendering as a confident $0.00.
func TestTaskRollupsDistinguishesFreeFromUnreported(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "work", TaskRunning)
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	in := int64(5)
	run := &StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "s", StepType: "agent",
		Attempt: 1, State: StepSucceeded, InputTokens: &in,
	}
	if err := s.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	got, err := s.TaskRollups(ctx, []int64{task.ID})
	if err != nil {
		t.Fatalf("TaskRollups: %v", err)
	}
	if got[task.ID].HasCost {
		t.Error("HasCost = true though no attempt reported a cost")
	}
	if got[task.ID].InputTokens != 5 {
		t.Errorf("input tokens = %d, want 5", got[task.ID].InputTokens)
	}
}

func TestTaskRollupsEmptyIDs(t *testing.T) {
	s := openTest(t)
	got, err := s.TaskRollups(t.Context(), nil)
	if err != nil {
		t.Fatalf("TaskRollups(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d rollups, want none", len(got))
	}
}

// TestListTasksArchivedFilter covers the default a board relies on: archives
// accumulate forever, so they are excluded unless asked for.
func TestListTasksArchivedFilter(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	live := newTask(p.ID, "live", TaskRunning)
	if err := s.CreateTask(ctx, live); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	gone := newTask(p.ID, "gone", TaskArchived)
	if err := s.CreateTask(ctx, gone); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	ids := func(f TaskFilter) []int64 {
		t.Helper()
		tasks, err := s.ListTasks(ctx, f)
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		out := make([]int64, 0, len(tasks))
		for i := range tasks {
			out = append(out, tasks[i].ID)
		}
		return out
	}

	if got := ids(TaskFilter{}); len(got) != 1 || got[0] != live.ID {
		t.Errorf("default listing = %v, want only the live task %d", got, live.ID)
	}
	if got := ids(TaskFilter{Archived: ArchivedOnly}); len(got) != 1 || got[0] != gone.ID {
		t.Errorf("ArchivedOnly = %v, want only %d", got, gone.ID)
	}
	if got := ids(TaskFilter{Archived: ArchivedAll}); len(got) != 2 {
		t.Errorf("ArchivedAll = %v, want both tasks", got)
	}
	// An explicit state wins: asking for archived and getting nothing because
	// the default excludes them would be absurd.
	if got := ids(TaskFilter{State: TaskArchived}); len(got) != 1 || got[0] != gone.ID {
		t.Errorf("state=archived = %v, want %d", got, gone.ID)
	}
}
