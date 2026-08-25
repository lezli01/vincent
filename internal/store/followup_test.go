package store

import "testing"

// followUpDoc is the compiled workflow a follow-up request carries. Its
// content is not this file's business — the store neither parses nor
// validates it — so one is fixed rather than varied.
const followUpDoc = "name: follow-up\nsteps:\n  - id: follow-up\n    type: command\n    run: git status\n"

// finishedTask is a task at `done` or `aborted`, which are the only two
// states a follow-up is asked for from (§6, task 027).
func finishedTask(t *testing.T, s *Store, projectID int64, end TaskState) *Task {
	t.Helper()
	task := newTask(projectID, "finished", TaskQueued)
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskQueued, TaskRunning, TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	out, _, err := s.TransitionTask(t.Context(), task.ID, TaskRunning, end, TaskChange{})
	if err != nil {
		t.Fatalf("finish as %s: %v", end, err)
	}
	return out
}

func followUpRequest(origin TaskState) FollowUpRequest {
	return FollowUpRequest{
		Form: FollowUpAgent, Prompt: "rebase onto main", Workflow: followUpDoc,
		Agent: "claude", Model: "sonnet", Effort: "high",
		Origin: origin, Round: 1,
	}
}

// TestPendingFollowUpRoundTrips: the request written with the done → queued
// transition is what the admitted actor reads back, and it survives a reload.
func TestPendingFollowUpRoundTrips(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p")
	task := finishedTask(t, s, p.ID, TaskDone)

	req := followUpRequest(TaskDone)
	queued, _, err := s.TransitionTask(t.Context(), task.ID, TaskDone, TaskQueued,
		TaskChange{PendingFollowUp: &req})
	if err != nil {
		t.Fatalf("follow-up transition: %v", err)
	}
	if queued.PendingFollowUp == nil || *queued.PendingFollowUp != req {
		t.Fatalf("pending follow-up = %+v, want %+v", queued.PendingFollowUp, req)
	}
	running, _, err := s.TransitionTask(t.Context(), task.ID, TaskQueued, TaskRunning, TaskChange{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if running.PendingFollowUp == nil || *running.PendingFollowUp != req {
		t.Fatalf("pending follow-up after admission = %+v, want it intact", running.PendingFollowUp)
	}
	reloaded, err := s.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if reloaded.PendingFollowUp == nil || *reloaded.PendingFollowUp != req {
		t.Fatalf("pending follow-up did not survive a reload: %+v", reloaded.PendingFollowUp)
	}
}

// TestPendingFollowUpSurvivesTheBlockAndTheRetry is the drain rule that is
// neither the override's nor the repair's (task 027 decision 6): the request
// is what makes a `retry` on a blocked follow-up re-run the follow-up rather
// than the workflow, so it must outlive both moves.
func TestPendingFollowUpSurvivesTheBlockAndTheRetry(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p")
	task := finishedTask(t, s, p.ID, TaskDone)
	req := followUpRequest(TaskDone)

	if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskDone, TaskQueued,
		TaskChange{PendingFollowUp: &req}); err != nil {
		t.Fatalf("follow-up: %v", err)
	}
	if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskQueued, TaskRunning, TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	reason := "nonzero_exit"
	blocked, _, err := s.TransitionTask(t.Context(), task.ID, TaskRunning, TaskBlocked,
		TaskChange{BlockReason: &reason})
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if blocked.PendingFollowUp == nil {
		t.Fatal("the request was dropped by the block; retry could not re-run the follow-up")
	}
	requeued, _, err := s.TransitionTask(t.Context(), task.ID, TaskBlocked, TaskQueued, TaskChange{})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if requeued.PendingFollowUp == nil {
		t.Fatal("the request was dropped by the retry; the re-admission would complete the task")
	}
}

// TestPendingFollowUpDrainsOnEverySettledEnd is the other half of decision 6.
// One rule covers all three ways a follow-up ends — the Complete that returns
// a done-origin run, the Restore that returns an aborted-origin one, and the
// cancel that abandons either — so no caller can leave a stale request on a
// finished task for the next follow-up to inherit.
func TestPendingFollowUpDrainsOnEverySettledEnd(t *testing.T) {
	for _, end := range []TaskState{TaskDone, TaskAborted} {
		t.Run(string(end), func(t *testing.T) {
			s := openTest(t)
			p := testProject(t, s, "p")
			task := finishedTask(t, s, p.ID, TaskDone)
			req := followUpRequest(TaskDone)

			if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskDone, TaskQueued,
				TaskChange{PendingFollowUp: &req}); err != nil {
				t.Fatalf("follow-up: %v", err)
			}
			if _, _, err := s.TransitionTask(t.Context(), task.ID,
				TaskQueued, TaskRunning, TaskChange{}); err != nil {
				t.Fatalf("admit: %v", err)
			}
			out, _, err := s.TransitionTask(t.Context(), task.ID, TaskRunning, end, TaskChange{})
			if err != nil {
				t.Fatalf("end as %s: %v", end, err)
			}
			if out.PendingFollowUp != nil {
				t.Errorf("reaching %s left the request behind: %+v", end, out.PendingFollowUp)
			}
		})
	}
}

// TestSetPendingFollowUpWritesTheCursor: the actor persists its own cursor as
// it advances, without a transition — the same separation SetTaskProgress
// makes for `current_step` (decision 4).
func TestSetPendingFollowUpWritesTheCursor(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p")
	task := finishedTask(t, s, p.ID, TaskAborted)
	req := followUpRequest(TaskAborted)

	if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskAborted, TaskQueued,
		TaskChange{PendingFollowUp: &req}); err != nil {
		t.Fatalf("follow-up: %v", err)
	}
	req.Cursor = 2
	if err := s.SetPendingFollowUp(t.Context(), task.ID, &req); err != nil {
		t.Fatalf("SetPendingFollowUp: %v", err)
	}
	got, err := s.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.PendingFollowUp == nil || got.PendingFollowUp.Cursor != 2 {
		t.Fatalf("cursor = %+v, want 2", got.PendingFollowUp)
	}
	if got.State != TaskQueued {
		t.Errorf("state = %s, want queued — writing the cursor is not a transition", got.State)
	}
	if err := s.SetPendingFollowUp(t.Context(), task.ID, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, err = s.GetTask(t.Context(), task.ID); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.PendingFollowUp != nil {
		t.Error("a nil request did not clear the column")
	}
}

// TestMaxStepIndex is what numbers a follow-up round (decision 2): the round
// is derived from the rows it will sit beside, never from a counter that
// could drift away from them.
func TestMaxStepIndex(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p")
	task := finishedTask(t, s, p.ID, TaskDone)

	if _, ok, err := s.MaxStepIndex(t.Context(), task.ID); err != nil || ok {
		t.Fatalf("MaxStepIndex on a task with no rows = %v, %v; want absent", ok, err)
	}
	for _, index := range []int{0, 1, 3} {
		run := &StepRun{
			TaskID: task.ID, StepIndex: index, StepID: "s", StepType: "command",
			Attempt: 1, State: StepSucceeded,
		}
		if err := s.CreateStepRun(t.Context(), run); err != nil {
			t.Fatalf("CreateStepRun: %v", err)
		}
	}
	got, ok, err := s.MaxStepIndex(t.Context(), task.ID)
	if err != nil || !ok || got != 3 {
		t.Fatalf("MaxStepIndex = %d, %v, %v; want 3", got, ok, err)
	}
}
