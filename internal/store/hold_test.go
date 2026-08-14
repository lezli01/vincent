package store

import (
	"testing"
	"time"
)

// TestAdmissionHoldRoundTrips asserts the 0006 columns exist on a store the
// migrator opened and survive a write/read cycle. It is also the proof that
// the migration applied: `openTest` runs the full chain, and every query in
// this package selects taskColumns, which now names both columns.
func TestAdmissionHoldRoundTrips(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "held", TaskQueued)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// A fresh task carries no hold: this is what every existing row looks
	// like after the migration, and what every client keeps seeing.
	fresh, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if fresh.AdmitNotBefore != nil || fresh.QueuedReason != "" {
		t.Fatalf("new task carries a hold: %v / %q", fresh.AdmitNotBefore, fresh.QueuedReason)
	}

	until := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Second)
	task.AdmitNotBefore = &until
	task.QueuedReason = "usage_limit"
	if err := s.UpdateTask(ctx, task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.AdmitNotBefore == nil || !got.AdmitNotBefore.Equal(until) {
		t.Errorf("admit_not_before = %v, want %s", got.AdmitNotBefore, until)
	}
	if got.QueuedReason != "usage_limit" {
		t.Errorf("queued_reason = %q, want usage_limit", got.QueuedReason)
	}

	// ListAdmissible is the scheduler's own read path; the hold has to reach
	// it, since the walk — not SQL — is where it is evaluated.
	candidates, err := s.ListAdmissible(ctx)
	if err != nil {
		t.Fatalf("ListAdmissible: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}
	if candidates[0].Task.AdmitNotBefore == nil {
		t.Error("ListAdmissible dropped admit_not_before; the scheduler would admit at once")
	}
}

// TestTransitionWritesHoldIntoQueued covers the engine's write: the hold
// lands atomically with the running → queued transition.
func TestTransitionWritesHoldIntoQueued(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "work", TaskRunning)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	until := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Second)
	reason := "usage_limit"
	got, _, err := s.TransitionTask(ctx, task.ID, TaskRunning, TaskQueued,
		TaskChange{AdmitNotBefore: &until, QueuedReason: &reason})
	if err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if got.AdmitNotBefore == nil || !got.AdmitNotBefore.Equal(until) {
		t.Errorf("admit_not_before = %v, want %s", got.AdmitNotBefore, until)
	}
	if got.QueuedReason != reason {
		t.Errorf("queued_reason = %q, want %q", got.QueuedReason, reason)
	}
	// A hold is not a block reason: clients key off block_reason to mean
	// "stopped, needs a human", and this task is neither.
	if got.BlockReason != "" {
		t.Errorf("block_reason = %q, want empty for a held queued task", got.BlockReason)
	}
}

// TestLeavingQueuedClearsHold is the invariant that makes clearing not the
// caller's job: every way out of `queued` drops the hold, so it cannot
// outlive the queued period it describes.
func TestLeavingQueuedClearsHold(t *testing.T) {
	exits := map[string]TaskState{
		"admitted": TaskRunning,
		"parked":   TaskPaused,
		"canceled": TaskAborted,
	}
	for name, to := range exits {
		t.Run(name, func(t *testing.T) {
			s := openTest(t)
			ctx := t.Context()
			p := testProject(t, s, "p1")
			task := newTask(p.ID, "held", TaskRunning)
			if err := s.CreateTask(ctx, task, nil); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			until := time.Now().Add(15 * time.Minute).UTC()
			reason := "usage_limit"
			if _, _, err := s.TransitionTask(ctx, task.ID, TaskRunning, TaskQueued,
				TaskChange{AdmitNotBefore: &until, QueuedReason: &reason}); err != nil {
				t.Fatalf("re-queue with a hold: %v", err)
			}

			got, _, err := s.TransitionTask(ctx, task.ID, TaskQueued, to, TaskChange{})
			if err != nil {
				t.Fatalf("TransitionTask(%s): %v", to, err)
			}
			if got.AdmitNotBefore != nil || got.QueuedReason != "" {
				t.Fatalf("hold survived queued → %s: %v / %q", to, got.AdmitNotBefore, got.QueuedReason)
			}
			reloaded, err := s.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if reloaded.AdmitNotBefore != nil || reloaded.QueuedReason != "" {
				t.Errorf("hold still committed after queued → %s: %v / %q",
					to, reloaded.AdmitNotBefore, reloaded.QueuedReason)
			}
		})
	}
}
