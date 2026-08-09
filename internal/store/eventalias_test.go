package store

import "testing"

// TestPublishedEventsDoNotAliasTheCallersTask pins the invariant that made a
// real race: an Event handed to the event hook goes on to the broker, where
// every SSE subscriber dereferences TaskID on its own goroutine. Building it
// as &t.ID instead of a copy publishes a pointer into a struct the caller is
// still writing — the runner rewrites its whole *Task on every transition,
// and the API assigns BranchName immediately after CreateTask returns.
//
// Mutating the caller's task after the fact is a deterministic stand-in for
// that concurrent write: if the event aliases it, the event changes too.
func TestPublishedEventsDoNotAliasTheCallersTask(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	var created *Event
	s.SetEventHook(func(e *Event) {
		if e.Type == EventTaskCreated {
			created = e
		}
	})
	task := newTask(p.ID, "work", TaskQueued)
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created == nil {
		t.Fatal("no task.created reached the event hook")
	}
	realID, realProject := task.ID, task.ProjectID

	var changed *Event
	s.SetEventHook(func(e *Event) {
		if e.Type == EventTaskStateChanged {
			changed = e
		}
	})
	if _, _, err := s.TransitionTask(ctx, task.ID, TaskQueued, TaskRunning, TaskChange{}); err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if changed == nil {
		t.Fatal("no task.state_changed reached the event hook")
	}

	// The caller keeps writing its own task. Nothing already published may
	// follow it.
	task.ID, task.ProjectID = 999, 998

	for name, ev := range map[string]*Event{"task.created": created, "task.state_changed": changed} {
		if ev.TaskID == nil || *ev.TaskID != realID {
			t.Errorf("%s: TaskID = %v, want %d — the event aliases the caller's task",
				name, ev.TaskID, realID)
		}
		if ev.ProjectID == nil || *ev.ProjectID != realProject {
			t.Errorf("%s: ProjectID = %v, want %d — the event aliases the caller's task",
				name, ev.ProjectID, realProject)
		}
	}
}
