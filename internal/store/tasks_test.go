package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// testProject creates a project for tasks to reference.
func testProject(t *testing.T, s *Store, name string) *Project {
	t.Helper()
	p := &Project{Name: name, Path: "/" + name, DefaultBranch: "main"}
	if err := s.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return p
}

func newTask(projectID int64, title string, state TaskState) *Task {
	return &Task{
		ProjectID:        projectID,
		Title:            title,
		WorkflowName:     "adhoc",
		WorkflowSnapshot: "steps: []",
		BaseBranch:       "main",
		BranchName:       "vincent/0-" + title,
		State:            state,
	}
}

func TestTaskRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	started := time.Now().Add(-time.Minute)
	in := &Task{
		ProjectID:        p.ID,
		Title:            "Add login",
		Description:      "Some *markdown*.",
		Fields:           map[string]string{"ticket": "OPS-123", "env": "dev"},
		WorkflowName:     "feature-pr",
		WorkflowSnapshot: "name: feature-pr\nsteps: []\n",
		BaseBranch:       "main",
		BranchName:       "vincent/1-add-login",
		WorktreePath:     "/data/worktrees/1",
		Priority:         5,
		AgentOverride:    "claude",
		ModelOverride:    "opus",
		EffortOverride:   "max",
		State:            TaskRunning,
		CurrentStep:      2,
		BlockReason:      "",
		StartedAt:        &started,
	}
	if err := s.CreateTask(ctx, in); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if in.ID == 0 {
		t.Fatal("CreateTask did not assign an ID")
	}

	got, err := s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Title != in.Title || got.Description != in.Description ||
		got.WorkflowName != in.WorkflowName || got.WorkflowSnapshot != in.WorkflowSnapshot ||
		got.BaseBranch != in.BaseBranch || got.BranchName != in.BranchName ||
		got.WorktreePath != in.WorktreePath || got.Priority != in.Priority ||
		got.AgentOverride != in.AgentOverride || got.ModelOverride != in.ModelOverride ||
		got.EffortOverride != in.EffortOverride ||
		got.State != in.State || got.CurrentStep != in.CurrentStep {
		t.Errorf("got %+v, want %+v", got, in)
	}
	if !reflect.DeepEqual(got.Fields, in.Fields) {
		t.Errorf("Fields = %v, want %v", got.Fields, in.Fields)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, started)
	}
	if got.FinishedAt != nil || got.ArchivedAt != nil {
		t.Errorf("unset time pointers came back non-nil: %+v", got)
	}
}

func TestSweepInterrupted(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	running := newTask(p.ID, "was-running", TaskRunning)
	queued := newTask(p.ID, "still-queued", TaskQueued)
	for _, tk := range []*Task{running, queued} {
		if err := s.CreateTask(ctx, tk); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}
	run := &StepRun{TaskID: running.ID, StepIndex: 0, StepID: "run", StepType: "agent", Attempt: 1, State: StepRunning}
	if err := s.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	n, err := s.SweepInterrupted(ctx)
	if err != nil {
		t.Fatalf("SweepInterrupted: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d tasks, want 1", n)
	}
	got, err := s.GetTask(ctx, running.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != TaskBlocked || got.BlockReason != "interrupted" {
		t.Errorf("running task = %s/%q, want blocked/interrupted", got.State, got.BlockReason)
	}
	if q, _ := s.GetTask(ctx, queued.ID); q.State != TaskQueued {
		t.Errorf("queued task = %s, want untouched", q.State)
	}
	r, err := s.GetStepRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if r.State != StepInterrupted || r.FinishedAt == nil {
		t.Errorf("step run = %s finished=%v, want interrupted with finished_at", r.State, r.FinishedAt)
	}
}

func TestTaskNullableDefaults(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	in := newTask(p.ID, "bare", TaskQueued)
	if err := s.CreateTask(ctx, in); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got, err := s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Fields != nil {
		t.Errorf("Fields = %v, want nil", got.Fields)
	}
	if got.WorktreePath != "" || got.BlockReason != "" {
		t.Errorf("nullable strings not empty: %+v", got)
	}
	if got.StartedAt != nil || got.FinishedAt != nil || got.ArchivedAt != nil {
		t.Errorf("time pointers not nil: %+v", got)
	}
}

func TestTaskUpdate(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	task := newTask(p.ID, "t", TaskQueued)
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	finished := time.Now()
	task.State = TaskBlocked
	task.BlockReason = "check command failed (exit 1)"
	task.WorktreePath = "/data/worktrees/9"
	task.CurrentStep = 1
	task.Priority = 10
	task.FinishedAt = &finished
	task.Fields = map[string]string{"k": "v"}
	if err := s.UpdateTask(ctx, task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != TaskBlocked || got.BlockReason != task.BlockReason ||
		got.WorktreePath != task.WorktreePath || got.CurrentStep != 1 || got.Priority != 10 {
		t.Errorf("update not persisted: %+v", got)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(finished) {
		t.Errorf("FinishedAt = %v, want %v", got.FinishedAt, finished)
	}
	if !reflect.DeepEqual(got.Fields, task.Fields) {
		t.Errorf("Fields = %v, want %v", got.Fields, task.Fields)
	}

	if err := s.UpdateTask(ctx, newTask(p.ID, "missing", TaskQueued)); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateTask(missing) = %v, want ErrNotFound", err)
	}
}

func TestTaskListFilters(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p1 := testProject(t, s, "p1")
	p2 := testProject(t, s, "p2")

	mk := func(pid int64, title string, state TaskState) *Task {
		task := newTask(pid, title, state)
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask(%s): %v", title, err)
		}
		return task
	}
	a := mk(p1.ID, "a", TaskQueued)
	b := mk(p1.ID, "b", TaskRunning)
	c := mk(p2.ID, "c", TaskQueued)

	ids := func(tasks []Task) []int64 {
		out := make([]int64, len(tasks))
		for i, task := range tasks {
			out[i] = task.ID
		}
		return out
	}

	cases := []struct {
		name   string
		filter TaskFilter
		want   []int64 // newest first
	}{
		{"all", TaskFilter{}, []int64{c.ID, b.ID, a.ID}},
		{"by project", TaskFilter{ProjectID: p1.ID}, []int64{b.ID, a.ID}},
		{"by state", TaskFilter{State: TaskQueued}, []int64{c.ID, a.ID}},
		{"project and state", TaskFilter{ProjectID: p1.ID, State: TaskRunning}, []int64{b.ID}},
		{"limit", TaskFilter{Limit: 2}, []int64{c.ID, b.ID}},
		{"offset", TaskFilter{Offset: 1}, []int64{b.ID, a.ID}},
		{"limit and offset", TaskFilter{Limit: 1, Offset: 1}, []int64{b.ID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListTasks(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListTasks: %v", err)
			}
			if !reflect.DeepEqual(ids(got), tc.want) {
				t.Errorf("ListTasks(%+v) ids = %v, want %v", tc.filter, ids(got), tc.want)
			}
		})
	}
}

func TestSchedulerQueries(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p1 := testProject(t, s, "p1")
	p2 := testProject(t, s, "p2")

	base := time.Now().Add(-time.Hour)
	mk := func(pid int64, title string, state TaskState, priority int, offset time.Duration) *Task {
		task := newTask(pid, title, state)
		task.Priority = priority
		task.CreatedAt = base.Add(offset)
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask(%s): %v", title, err)
		}
		return task
	}

	// Admission order (spec §11): priority DESC, then created_at ASC.
	older := mk(p1.ID, "older", TaskQueued, 0, 0)
	newer := mk(p1.ID, "newer", TaskQueued, 0, time.Minute)
	urgent := mk(p2.ID, "urgent", TaskQueued, 5, 2*time.Minute)
	mk(p1.ID, "running1", TaskRunning, 0, 3*time.Minute)
	mk(p2.ID, "running2", TaskRunning, 0, 4*time.Minute)
	mk(p1.ID, "archived", TaskArchived, 0, 5*time.Minute)

	queued, err := s.ListQueuedInOrder(ctx)
	if err != nil {
		t.Fatalf("ListQueuedInOrder: %v", err)
	}
	want := []int64{urgent.ID, older.ID, newer.ID}
	got := make([]int64, len(queued))
	for i, task := range queued {
		got[i] = task.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListQueuedInOrder ids = %v, want %v (priority desc, then FIFO)", got, want)
	}

	if n, err := s.CountRunning(ctx); err != nil || n != 2 {
		t.Errorf("CountRunning = %d, %v; want 2, nil", n, err)
	}
	if n, err := s.CountRunningByProject(ctx, p1.ID); err != nil || n != 1 {
		t.Errorf("CountRunningByProject(p1) = %d, %v; want 1, nil", n, err)
	}
	if n, err := s.CountNonArchivedTasks(ctx, p1.ID); err != nil || n != 3 {
		t.Errorf("CountNonArchivedTasks(p1) = %d, %v; want 3, nil", n, err)
	}
}

func TestTaskForeignKeyEnforced(t *testing.T) {
	s := openTest(t)
	if err := s.CreateTask(t.Context(), newTask(9999, "orphan", TaskQueued)); err == nil {
		t.Error("CreateTask with unknown project_id accepted; want FK violation")
	}
}

func TestTaskGetMissing(t *testing.T) {
	s := openTest(t)
	if _, err := s.GetTask(t.Context(), 42); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetTask(missing) = %v, want ErrNotFound", err)
	}
}
