package store

import (
	"testing"
	"time"
)

// A step run's commit range round-trips (migration 0068, task 168.5):
// head_start rides the insert, head_end the finalizing update, and an update
// from a struct that never read either — the actor's PID journal, a stale
// copy — erases neither end.
func TestStepRunHeadRangeRoundTrips(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	r := &StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "implement", StepType: "command",
		Attempt: 1, State: StepRunning, HeadStart: ptr("aaaa"),
	}
	if err := s.CreateStepRun(ctx, r); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	got, err := s.GetStepRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.HeadStart == nil || *got.HeadStart != "aaaa" || got.HeadEnd != nil {
		t.Fatalf("after insert: head_start=%v head_end=%v, want aaaa and nil", got.HeadStart, got.HeadEnd)
	}

	// The PID journal's update carries no head_end: it must not null the start.
	pid := 42
	stale := *got
	stale.HeadStart = nil
	stale.PID = &pid
	if err := s.UpdateStepRun(ctx, &stale); err != nil {
		t.Fatalf("UpdateStepRun (pid): %v", err)
	}

	now := time.Now()
	got.State, got.PID, got.FinishedAt, got.HeadEnd = StepSucceeded, nil, &now, ptr("bbbb")
	if err := s.UpdateStepRun(ctx, got); err != nil {
		t.Fatalf("UpdateStepRun (finish): %v", err)
	}
	// A later write from a struct without the end keeps it.
	again := *got
	again.HeadEnd = nil
	if err := s.UpdateStepRun(ctx, &again); err != nil {
		t.Fatalf("UpdateStepRun (stale): %v", err)
	}

	final, err := s.GetStepRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if final.HeadStart == nil || *final.HeadStart != "aaaa" {
		t.Errorf("head_start = %v, want aaaa", final.HeadStart)
	}
	if final.HeadEnd == nil || *final.HeadEnd != "bbbb" {
		t.Errorf("head_end = %v, want bbbb", final.HeadEnd)
	}
}

// Recovery's terminalize finalizes an open run without inventing an end
// (§12.4): it did not see the attempt end.
func TestTerminalizeLeavesHeadEndUnrecorded(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)
	r := &StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "implement", StepType: "agent",
		Attempt: 1, State: StepRunning, HeadStart: ptr("aaaa"),
	}
	if err := s.CreateStepRun(ctx, r); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	if _, err := s.TerminalizeOpenStepRuns(ctx, task.ID, StepInterrupted, "daemon_restart"); err != nil {
		t.Fatalf("TerminalizeOpenStepRuns: %v", err)
	}
	got, err := s.GetStepRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.State != StepInterrupted || got.HeadEnd != nil || got.HeadStart == nil {
		t.Errorf("state=%s head_start=%v head_end=%v, want interrupted, aaaa, nil",
			got.State, got.HeadStart, got.HeadEnd)
	}
}
