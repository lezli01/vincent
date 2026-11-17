package store

import (
	"testing"
	"time"
)

// TestGlobalPauseRoundTrip walks the daemon-wide pause (task 149) through the
// store: taking it marks only the running tasks a human has not paused, a
// repeat changes nothing, and lifting it withdraws the pending requests it
// made and hands back exactly the tasks it parked.
func TestGlobalPauseRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	mk := func(title string, state TaskState) *Task {
		t.Helper()
		task := newTask(p.ID, title, state)
		if err := s.CreateTask(ctx, task, nil); err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		return task
	}
	running := mk("running", TaskRunning)
	humanPending := mk("human pending", TaskRunning)
	if _, err := s.RequestPause(ctx, humanPending.ID); err != nil {
		t.Fatalf("RequestPause: %v", err)
	}
	queued := mk("queued", TaskQueued)
	humanPaused := mk("human paused", TaskPaused)
	parked := mk("parked by the pause", TaskPaused)

	if p0, err := s.GetGlobalPause(ctx); err != nil || p0 != nil {
		t.Fatalf("fresh store: pause = %v, %v; want active", p0, err)
	}

	at := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	got, changed, err := s.SetGlobalPause(ctx, "upgrading", at)
	if err != nil || !changed {
		t.Fatalf("SetGlobalPause = %v, %v, %v", got, changed, err)
	}
	again, changed, err := s.SetGlobalPause(ctx, "other", at.Add(time.Hour))
	if err != nil || changed || !again.PausedAt.Equal(at) || again.Reason != "upgrading" {
		t.Fatalf("repeat SetGlobalPause = %+v, %v, %v; want the first pause kept", again, changed, err)
	}

	check := func(task *Task, wantReq, wantMark bool) {
		t.Helper()
		g, err := s.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if g.PauseRequested != wantReq || g.GlobalPause != wantMark {
			t.Errorf("%s: pause_requested=%v global_pause=%v, want %v/%v",
				task.Title, g.PauseRequested, g.GlobalPause, wantReq, wantMark)
		}
	}
	check(running, true, true)
	check(humanPending, true, false)
	check(queued, false, false)

	// The engine's park keeps the mark; simulate it on one task.
	mark := true
	if _, _, err := s.TransitionTask(ctx, parked.ID, TaskPaused, TaskPaused, TaskChange{GlobalPause: &mark}); err != nil {
		t.Fatalf("mark parked: %v", err)
	}

	tally, err := s.TallyGlobalPause(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tally.Running != 2 || tally.Queued != 1 || tally.Paused != 1 {
		t.Errorf("tally = %+v, want 2 running, 1 queued, 1 paused by the pause", tally)
	}

	ids, changed, err := s.ClearGlobalPause(ctx)
	if err != nil || !changed {
		t.Fatalf("ClearGlobalPause = %v, %v, %v", ids, changed, err)
	}
	if len(ids) != 1 || ids[0] != parked.ID {
		t.Errorf("ClearGlobalPause ids = %v, want only [%d]; the human's paused task %d stays", ids, parked.ID, humanPaused.ID)
	}
	check(running, false, false)
	check(humanPending, true, false)
	if p1, err := s.GetGlobalPause(ctx); err != nil || p1 != nil {
		t.Fatalf("after clear: pause = %v, %v; want active", p1, err)
	}
	if ids, changed, err := s.ClearGlobalPause(ctx); err != nil || changed || len(ids) != 0 {
		t.Fatalf("second ClearGlobalPause = %v, %v, %v; want a no-op", ids, changed, err)
	}
}
