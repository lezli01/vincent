package store

import (
	"context"
	"errors"
	"testing"
)

func TestDeleteProjectCascade(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	keep := &Project{Name: "keep", Path: "/keep", DefaultBranch: "main"}
	doomed := &Project{Name: "doomed", Path: "/doomed", DefaultBranch: "main"}
	for _, p := range []*Project{keep, doomed} {
		if err := s.CreateProject(ctx, p); err != nil {
			t.Fatalf("create project: %v", err)
		}
	}
	mkTask := func(pid int64, state TaskState) *Task {
		tk := &Task{
			ProjectID: pid, Title: "t", WorkflowName: "adhoc", WorkflowSnapshot: "x",
			BaseBranch: "main", BranchName: "b", State: state,
		}
		if err := s.CreateTask(ctx, tk); err != nil {
			t.Fatalf("create task: %v", err)
		}
		return tk
	}
	doomedTask := mkTask(doomed.ID, TaskArchived)
	keepTask := mkTask(keep.ID, TaskQueued)
	run := &StepRun{
		TaskID: doomedTask.ID, StepIndex: 0, StepID: "s", StepType: "agent",
		Attempt: 1, State: StepSucceeded,
	}
	if err := s.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("create step run: %v", err)
	}
	for _, e := range []*Event{
		{Type: "task.created", TaskID: &doomedTask.ID, ProjectID: &doomed.ID},
		{Type: "project.updated", ProjectID: &doomed.ID},
		{Type: "task.created", TaskID: &keepTask.ID, ProjectID: &keep.ID},
	} {
		if err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}

	if err := s.DeleteProjectCascade(ctx, doomed.ID); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if _, err := s.GetProject(ctx, doomed.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("doomed project still present: %v", err)
	}
	if _, err := s.GetTask(ctx, doomedTask.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("doomed task still present: %v", err)
	}
	if runs, err := s.ListStepRuns(ctx, doomedTask.ID); err != nil || len(runs) != 0 {
		t.Errorf("step runs = %d, %v; want none", len(runs), err)
	}
	events, err := s.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, e := range events {
		if e.ProjectID != nil && *e.ProjectID == doomed.ID {
			t.Errorf("event %d for the deleted project survived", e.ID)
		}
	}
	if len(events) != 1 {
		t.Errorf("events = %d, want 1 (the kept project's)", len(events))
	}
	// The other project is untouched.
	if _, err := s.GetProject(ctx, keep.ID); err != nil {
		t.Errorf("kept project: %v", err)
	}
	if _, err := s.GetTask(ctx, keepTask.ID); err != nil {
		t.Errorf("kept task: %v", err)
	}

	if err := s.DeleteProjectCascade(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing project: err = %v, want ErrNotFound", err)
	}
}
