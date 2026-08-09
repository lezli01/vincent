package apiclient_test

import (
	"context"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// TestListTasksAgainstRealHandlers is the drift guard: the client's wire
// struct is decoded from the response the real API produces, so a server-side
// field rename fails here rather than silently blanking a board column.
func TestListTasksAgainstRealHandlers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	got, err := h.client().ListTasks(ctx, apiclient.ListTasksOptions{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d tasks, want 1", len(got))
	}
	task := got[0]
	if task.ID != h.taskID {
		t.Errorf("id = %d, want %d", task.ID, h.taskID)
	}
	if task.ProjectName != "client" {
		t.Errorf("project_name = %q, want %q", task.ProjectName, "client")
	}
	if task.State != string(store.TaskQueued) {
		t.Errorf("state = %q, want queued", task.State)
	}
	if task.CreatedAt.IsZero() {
		t.Error("created_at did not decode; RFC3339 timestamps are wire-format")
	}
	if task.CostUSD != nil {
		t.Errorf("cost_usd = %v, want null with no attempts", *task.CostUSD)
	}
	if len(task.AvailableActions) == 0 {
		t.Error("available_actions is empty; the board would render no action hints")
	}
}

// TestListTasksRollupAndSteps drives the columns through the real aggregate.
func TestListTasksRollupAndSteps(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for i, c := range []float64{0.20, 0.05} {
		cost := c
		in, out := int64(50), int64(5)
		run := &store.StepRun{
			TaskID: h.taskID, StepIndex: 0, StepID: "run", StepType: "agent",
			Attempt: i + 1, State: store.StepFailed,
			CostUSD: &cost, InputTokens: &in, OutputTokens: &out,
		}
		if err := h.st.CreateStepRun(ctx, run); err != nil {
			t.Fatalf("CreateStepRun: %v", err)
		}
	}

	got, err := h.client().ListTasks(ctx, apiclient.ListTasksOptions{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if got[0].CostUSD == nil {
		t.Fatal("cost_usd is null though two attempts reported a cost")
	}
	if diff := *got[0].CostUSD - 0.25; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost_usd = %v, want 0.25 (both attempts)", *got[0].CostUSD)
	}
	if got[0].InputTokens != 100 {
		t.Errorf("input_tokens = %d, want 100", got[0].InputTokens)
	}
	// The harness snapshot is deliberately not a workflow, so the step
	// columns degrade rather than failing the request.
	if got[0].StepTotal != 0 {
		t.Errorf("step_total = %d, want 0 for an unparsable snapshot", got[0].StepTotal)
	}
}

func TestListTasksArchivedScope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, _, err := h.st.TransitionTask(
		ctx, h.taskID, store.TaskQueued, store.TaskArchived, store.TaskChange{},
	); err != nil {
		t.Fatalf("archive task: %v", err)
	}

	c := h.client()
	live, err := c.ListTasks(ctx, apiclient.ListTasksOptions{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("default listing returned %d archived tasks, want 0", len(live))
	}
	only, err := c.ListTasks(ctx, apiclient.ListTasksOptions{Archived: apiclient.ArchivedOnly})
	if err != nil {
		t.Fatalf("ListTasks(archived): %v", err)
	}
	if len(only) != 1 {
		t.Errorf("ArchivedOnly returned %d tasks, want 1", len(only))
	}
	all, err := c.ListTasks(ctx, apiclient.ListTasksOptions{Archived: apiclient.ArchivedAll})
	if err != nil {
		t.Fatalf("ListTasks(all): %v", err)
	}
	if len(all) != 1 {
		t.Errorf("ArchivedAll returned %d tasks, want 1", len(all))
	}
}

func TestInfoAgainstRealHandlers(t *testing.T) {
	h := newHarness(t)
	got, err := h.client().Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if got.MaxParallelTasks <= 0 {
		t.Errorf("max_parallel_tasks = %d, want the configured cap", got.MaxParallelTasks)
	}
	if got.PID == 0 {
		t.Error("pid did not decode")
	}
}

func TestStepDisplay(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cur, total int
		wantK      int
		wantOK     bool
	}{
		{"first step", 0, 6, 1, true},
		{"mid run", 2, 6, 3, true},
		{"cursor past the end after completion", 6, 6, 6, true},
		{"unparsable snapshot", 0, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := apiclient.Task{CurrentStep: tc.cur, StepTotal: tc.total}
			k, n, ok := task.StepDisplay()
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if k != tc.wantK || n != tc.total {
				t.Errorf("got %d/%d, want %d/%d", k, n, tc.wantK, tc.total)
			}
		})
	}
}

// TestElapsedIsWallClock pins the §15 decision: a task idle on a human still
// reports the full time it has been alive.
func TestElapsedIsWallClock(t *testing.T) {
	start := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	now := start.Add(40 * time.Minute)

	running := apiclient.Task{StartedAt: &start}
	d, ok := running.Elapsed(now)
	if !ok || d != 40*time.Minute {
		t.Errorf("running elapsed = %v (ok=%v), want 40m", d, ok)
	}

	end := start.Add(5 * time.Minute)
	done := apiclient.Task{StartedAt: &start, FinishedAt: &end}
	if d, _ := done.Elapsed(now); d != 5*time.Minute {
		t.Errorf("finished elapsed = %v, want 5m (frozen at finished_at)", d)
	}

	if _, ok := (apiclient.Task{}).Elapsed(now); ok {
		t.Error("a task that never started should report no elapsed time")
	}

	// Clock skew between daemon and client must not render as a negative.
	future := now.Add(time.Minute)
	if d, _ := (apiclient.Task{StartedAt: &future}).Elapsed(now); d != 0 {
		t.Errorf("skewed elapsed = %v, want 0", d)
	}
}
