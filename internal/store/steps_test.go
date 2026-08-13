package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }

func testTask(t *testing.T, s *Store) *Task {
	t.Helper()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "t", TaskRunning)
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func TestStepRunRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	proc := time.Now().Add(-time.Minute)
	finished := time.Now()
	in := &StepRun{
		TaskID:         task.ID,
		StepIndex:      0,
		StepID:         "implement",
		StepType:       "agent",
		Attempt:        2,
		State:          StepSucceeded,
		Agent:          "claude",
		Model:          "sonnet",
		Effort:         "high",
		PID:            intPtr(4321),
		ProcStartedAt:  &proc,
		ExitCode:       intPtr(0),
		CheckExitCode:  intPtr(0),
		FailureReason:  "",
		ResultSummary:  "Implemented the thing.",
		TranscriptPath: "/data/transcripts/1/0-2.jsonl",
		InputTokens:    int64Ptr(1200),
		OutputTokens:   int64Ptr(3400),
		CostUSD:        float64Ptr(0.42),
		FinishedAt:     &finished,
	}
	if err := s.CreateStepRun(ctx, in); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	if in.ID == 0 {
		t.Fatal("CreateStepRun did not assign an ID")
	}

	got, err := s.GetStepRun(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.TaskID != in.TaskID || got.StepID != in.StepID || got.StepType != in.StepType ||
		got.Attempt != in.Attempt || got.State != in.State || got.Agent != in.Agent ||
		got.Model != in.Model || got.Effort != in.Effort ||
		got.ResultSummary != in.ResultSummary || got.TranscriptPath != in.TranscriptPath {
		t.Errorf("got %+v, want %+v", got, in)
	}
	if got.PID == nil || *got.PID != 4321 || got.ExitCode == nil || *got.ExitCode != 0 ||
		got.CheckExitCode == nil || *got.CheckExitCode != 0 {
		t.Errorf("process fields mismatched: %+v", got)
	}
	if got.InputTokens == nil || *got.InputTokens != 1200 ||
		got.OutputTokens == nil || *got.OutputTokens != 3400 ||
		got.CostUSD == nil || *got.CostUSD != 0.42 {
		t.Errorf("usage fields mismatched: %+v", got)
	}
	if got.ProcStartedAt == nil || !got.ProcStartedAt.Equal(proc) {
		t.Errorf("ProcStartedAt = %v, want %v", got.ProcStartedAt, proc)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(finished) {
		t.Errorf("FinishedAt = %v, want %v", got.FinishedAt, finished)
	}
}

func TestStepRunNullableFields(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	in := &StepRun{TaskID: task.ID, StepIndex: 0, StepID: "gate", StepType: "manual", Attempt: 1, State: StepRunning}
	if err := s.CreateStepRun(ctx, in); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	got, err := s.GetStepRun(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.Agent != "" || got.FailureReason != "" || got.ResultSummary != "" || got.TranscriptPath != "" {
		t.Errorf("nullable strings not empty: %+v", got)
	}
	if got.PID != nil || got.ExitCode != nil || got.CheckExitCode != nil ||
		got.InputTokens != nil || got.OutputTokens != nil || got.CostUSD != nil ||
		got.ProcStartedAt != nil || got.FinishedAt != nil {
		t.Errorf("nullable pointers not nil: %+v", got)
	}
	if got.StartedAt.IsZero() {
		t.Error("StartedAt was not defaulted")
	}
}

func TestStepRunUpdate(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	r := &StepRun{TaskID: task.ID, StepIndex: 0, StepID: "impl", StepType: "agent", Attempt: 1, State: StepRunning, PID: intPtr(99)}
	if err := s.CreateStepRun(ctx, r); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	finished := time.Now()
	r.State = StepFailed
	r.PID = nil // cleared once the process exits
	r.ExitCode = intPtr(1)
	r.FailureReason = "agent exited nonzero"
	r.FinishedAt = &finished
	if err := s.UpdateStepRun(ctx, r); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}

	got, err := s.GetStepRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.State != StepFailed || got.PID != nil || got.ExitCode == nil || *got.ExitCode != 1 ||
		got.FailureReason != r.FailureReason || got.FinishedAt == nil {
		t.Errorf("update not persisted: %+v", got)
	}

	if err := s.UpdateStepRun(ctx, &StepRun{ID: 9999, State: StepFailed}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateStepRun(missing) = %v, want ErrNotFound", err)
	}
}

func TestStepRunListOrder(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	// Insert out of order; expect step_index then attempt ordering.
	for _, r := range []*StepRun{
		{TaskID: task.ID, StepIndex: 1, StepID: "review", StepType: "agent", Attempt: 1, State: StepRunning},
		{TaskID: task.ID, StepIndex: 0, StepID: "impl", StepType: "agent", Attempt: 2, State: StepSucceeded},
		{TaskID: task.ID, StepIndex: 0, StepID: "impl", StepType: "agent", Attempt: 1, State: StepFailed},
	} {
		if err := s.CreateStepRun(ctx, r); err != nil {
			t.Fatalf("CreateStepRun: %v", err)
		}
	}

	got, err := s.ListStepRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	order := make([][2]int, len(got))
	for i, run := range got {
		order[i] = [2]int{run.StepIndex, run.Attempt}
	}
	want := [][2]int{{0, 1}, {0, 2}, {1, 1}}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestStepRunGetMissing(t *testing.T) {
	s := openTest(t)
	if _, err := s.GetStepRun(t.Context(), 42); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetStepRun(missing) = %v, want ErrNotFound", err)
	}
}
