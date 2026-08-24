package store

import (
	"testing"
	"time"
)

// blockedReason is what every task in this file is blocked with. Which reason
// it is does not matter to any assertion here — a repair carries whatever it
// found back to `blocked` — so one is fixed rather than varied.
const blockedReason = "check_failed"

// blockedTask is a task sitting in `blocked` with a reason, which is the only
// state a repair is asked for from (§6, task 025).
func blockedTask(t *testing.T, s *Store, projectID int64) *Task {
	t.Helper()
	reason := blockedReason
	task := newTask(projectID, "repairable", TaskQueued)
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskQueued, TaskRunning, TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	out, _, err := s.TransitionTask(t.Context(), task.ID, TaskRunning, TaskBlocked,
		TaskChange{BlockReason: &reason})
	if err != nil {
		t.Fatalf("block: %v", err)
	}
	return out
}

// TestPendingRepairRoundTrips: the request written with the blocked → queued
// transition is what the admitted actor reads back.
func TestPendingRepairRoundTrips(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p")
	task := blockedTask(t, s, p.ID)

	req := RepairRequest{
		Prompt: "fix the failing check", Agent: "claude", Model: "sonnet",
		Effort: "high", BlockReason: task.BlockReason,
	}
	queued, _, err := s.TransitionTask(t.Context(), task.ID, TaskBlocked, TaskQueued,
		TaskChange{PendingRepair: &req})
	if err != nil {
		t.Fatalf("repair transition: %v", err)
	}
	if queued.PendingRepair == nil || *queued.PendingRepair != req {
		t.Fatalf("pending repair = %+v, want %+v", queued.PendingRepair, req)
	}
	// It survives the transition off `blocked` that carried it, and the
	// admission that follows — those two moves are the whole reason the
	// column exists.
	running, _, err := s.TransitionTask(t.Context(), task.ID, TaskQueued, TaskRunning, TaskChange{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if running.PendingRepair == nil || *running.PendingRepair != req {
		t.Fatalf("pending repair after admission = %+v, want it intact", running.PendingRepair)
	}
	reloaded, err := s.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if reloaded.PendingRepair == nil || *reloaded.PendingRepair != req {
		t.Fatalf("pending repair did not survive a reload: %+v", reloaded.PendingRepair)
	}
}

// TestPendingRepairSurvivesAnInterrupt is why the request is drained by the
// re-block and not by the row insert (§12.4, task 025): recovery re-queues a
// running task, and a drained request would make the next admission a plain
// retry of the blocked step.
func TestPendingRepairSurvivesAnInterrupt(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p")
	task := blockedTask(t, s, p.ID)
	req := RepairRequest{Prompt: "fix it", BlockReason: "check_failed"}
	if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskBlocked, TaskQueued,
		TaskChange{PendingRepair: &req}); err != nil {
		t.Fatalf("repair transition: %v", err)
	}
	if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskQueued, TaskRunning, TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}

	interrupted, _, err := s.TransitionTask(t.Context(), task.ID, TaskRunning, TaskQueued, TaskChange{})
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if interrupted.PendingRepair == nil {
		t.Fatal("an interrupted repair lost its request; it would re-run as a retry")
	}
}

// TestReblockDrainsThePendingRepair: the transition that returns the task to
// `blocked` is the one that consumes the request.
func TestReblockDrainsThePendingRepair(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p")
	task := blockedTask(t, s, p.ID)
	req := RepairRequest{Prompt: "fix it", BlockReason: "check_failed"}
	if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskBlocked, TaskQueued,
		TaskChange{PendingRepair: &req}); err != nil {
		t.Fatalf("repair transition: %v", err)
	}
	if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskQueued, TaskRunning, TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}

	reason := req.BlockReason
	var drained RepairRequest
	out, _, err := s.TransitionTask(t.Context(), task.ID, TaskRunning, TaskBlocked,
		TaskChange{BlockReason: &reason, PendingRepair: &drained})
	if err != nil {
		t.Fatalf("re-block: %v", err)
	}
	if out.PendingRepair != nil {
		t.Errorf("pending repair = %+v, want it drained", out.PendingRepair)
	}
	if out.BlockReason != "check_failed" {
		t.Errorf("block_reason = %q, want the reason restored", out.BlockReason)
	}
}

// TestLeavingBlockedDropsAPendingRepair: a request describes *this* block, so
// every other way out of `blocked` drops it. Without this a `retry` would run
// a repair, and a `skip` would run one aimed at a step it had already moved
// past (task 025).
func TestLeavingBlockedDropsAPendingRepair(t *testing.T) {
	for _, to := range []TaskState{TaskQueued, TaskAborted} {
		t.Run(string(to), func(t *testing.T) {
			s := openTest(t)
			p := testProject(t, s, "p")
			task := blockedTask(t, s, p.ID)
			req := RepairRequest{Prompt: "fix it", BlockReason: "check_failed"}
			if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskBlocked, TaskQueued,
				TaskChange{PendingRepair: &req}); err != nil {
				t.Fatalf("repair transition: %v", err)
			}
			// Back to blocked without draining, the way a worktree failure
			// blocks a repair admission before it can run one.
			reason := "check_failed"
			if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskQueued, TaskRunning,
				TaskChange{}); err != nil {
				t.Fatalf("admit: %v", err)
			}
			blocked, _, err := s.TransitionTask(t.Context(), task.ID, TaskRunning, TaskBlocked,
				TaskChange{BlockReason: &reason})
			if err != nil {
				t.Fatalf("re-block: %v", err)
			}
			if blocked.PendingRepair == nil {
				t.Fatal("the fixture lost the request before the case under test")
			}

			out, _, err := s.TransitionTask(t.Context(), task.ID, TaskBlocked, to, TaskChange{})
			if err != nil {
				t.Fatalf("leave blocked → %s: %v", to, err)
			}
			if out.PendingRepair != nil {
				t.Errorf("%s carried the repair request out of blocked: %+v", to, out.PendingRepair)
			}
		})
	}
}

// TestCountStepAttemptsIgnoresRepairRows is the whole retry-budget argument in
// one assertion: the reserved step id puts a repair at a position of its own,
// so the blocked step's numbers are the same with repairs present as without
// (§7.2, task 025). No query changed to make this true.
func TestCountStepAttemptsIgnoresRepairRows(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p")
	task := newTask(p.ID, "budget", TaskBlocked)
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	ref := StepRef{TaskID: task.ID, StepIndex: 3, StepID: "build"}
	for attempt := 1; attempt <= 2; attempt++ {
		writeRun(t, s, task.ID, 3, "build", attempt, StepFailed)
	}
	before, err := s.CountStepAttempts(t.Context(), ref, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if before.Last != 2 || before.Failed != 2 {
		t.Fatalf("attempts = %+v, want 2 attempts, 2 failed", before)
	}

	// Two repairs at the same index, one of which failed.
	writeRun(t, s, task.ID, 3, repairStepID, 1, StepSucceeded)
	writeRun(t, s, task.ID, 3, repairStepID, 2, StepFailed)

	after, err := s.CountStepAttempts(t.Context(), ref, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if after != before {
		t.Errorf("the step's budget saw the repair rows: %+v → %+v", before, after)
	}
	// And the repair's own position numbers independently.
	repairs, err := s.CountStepAttempts(t.Context(),
		StepRef{TaskID: task.ID, StepIndex: 3, StepID: repairStepID}, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if repairs.Last != 2 {
		t.Errorf("repair attempts = %+v, want the second repair to be attempt 2", repairs)
	}
}

// repairStepID mirrors internal/taskrun's reserved id (§5.4, task 025). The
// store must not import the engine, and this is a fixture rather than a
// shared constant: what the store owes the id is that its composite key
// treats it like any other, which is exactly what the test above asserts.
const repairStepID = "__repair"

func writeRun(t *testing.T, s *Store, taskID int64, index int, stepID string, attempt int, state StepRunState) {
	t.Helper()
	run := &StepRun{
		TaskID: taskID, StepIndex: index, StepID: stepID, StepType: "agent",
		Attempt: attempt, State: state,
	}
	if err := s.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
}
