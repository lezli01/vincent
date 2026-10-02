package store

import (
	"context"
	"errors"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
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
		if err := s.CreateTask(ctx, tk, nil); err != nil {
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
	deleted := 0
	for _, e := range events {
		if e.ProjectID != nil && *e.ProjectID == doomed.ID {
			// The only trace the cascade leaves is the project.deleted event
			// it appends after the deletes (PR D decision).
			if e.Type != EventProjectDeleted {
				t.Errorf("event %d (%s) for the deleted project survived", e.ID, e.Type)
				continue
			}
			deleted++
		}
	}
	if deleted != 1 {
		t.Errorf("project.deleted events = %d, want exactly 1", deleted)
	}
	// Remaining: the kept project's project.created and two task.created
	// (CreateProject/CreateTask write their own), plus project.deleted.
	if len(events) != 4 {
		t.Errorf("events = %d, want 4", len(events))
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

// TestDeleteProjectCascadeTakesItsIssues (task 130): the issue tables go by
// ON DELETE CASCADE from the project row, after the explicit task delete, so
// a task linked to one of the project's issues is no obstacle and nothing of
// the issue set survives — while the other project's is untouched.
func TestDeleteProjectCascadeTakesItsIssues(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	keep := testProject(t, s, "keep")
	doomed := testProject(t, s, "doomed")

	seed := func(p *Project) *Issue {
		is, err := s.CreateIssue(ctx, NewIssue{
			ProjectID: p.ID, Title: "issue of " + p.Name, Labels: []string{"bug", "store"},
		}, issuestate.Human)
		if err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if _, err := s.AddIssueComment(ctx, is.ID, "me", "a comment", "", issuestate.Human); err != nil {
			t.Fatalf("AddIssueComment: %v", err)
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO issue_remotes (issue_id, project_id, provider, remote_key)
			VALUES (?, ?, 'github', ?)`, is.ID, p.ID, "node-"+p.Name); err != nil {
			t.Fatalf("insert remote: %v", err)
		}
		tk := newTask(p.ID, "from-"+p.Name, TaskQueued)
		tk.IssueID = &is.ID
		tk.Issue = &IssueSnapshot{ID: is.ID, Title: is.Title, State: "open"}
		if err := s.CreateTask(ctx, tk, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return is
	}
	kept := seed(keep)
	gone := seed(doomed)

	if err := s.DeleteProjectCascade(ctx, doomed.ID); err != nil {
		t.Fatalf("cascade: %v", err)
	}

	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("PRAGMA foreign_key_check reports %d violations after the cascade", n)
	}
	for _, tc := range []struct {
		name, q string
		arg     int64
	}{
		{"issues", `SELECT COUNT(*) FROM issues WHERE project_id = ?`, doomed.ID},
		{"issue_remotes", `SELECT COUNT(*) FROM issue_remotes WHERE project_id = ?`, doomed.ID},
		{"labels", `SELECT COUNT(*) FROM labels WHERE project_id = ?`, doomed.ID},
		{"issue_labels", `SELECT COUNT(*) FROM issue_labels WHERE issue_id = ?`, gone.ID},
		{"issue_comments", `SELECT COUNT(*) FROM issue_comments WHERE issue_id = ?`, gone.ID},
		{"tasks", `SELECT COUNT(*) FROM tasks WHERE project_id = ?`, doomed.ID},
	} {
		if n := count(tc.q, tc.arg); n != 0 {
			t.Errorf("%d %s rows of the deleted project survived", n, tc.name)
		}
	}
	for _, tc := range []struct {
		name, q string
		arg     int64
		want    int
	}{
		{"issues", `SELECT COUNT(*) FROM issues WHERE project_id = ?`, keep.ID, 1},
		{"issue_remotes", `SELECT COUNT(*) FROM issue_remotes WHERE project_id = ?`, keep.ID, 1},
		{"labels", `SELECT COUNT(*) FROM labels WHERE project_id = ?`, keep.ID, 2},
		{"issue_labels", `SELECT COUNT(*) FROM issue_labels WHERE issue_id = ?`, kept.ID, 2},
		{"issue_comments", `SELECT COUNT(*) FROM issue_comments WHERE issue_id = ?`, kept.ID, 1},
		{"linked tasks", `SELECT COUNT(*) FROM tasks WHERE issue_id = ?`, kept.ID, 1},
	} {
		if n := count(tc.q, tc.arg); n != tc.want {
			t.Errorf("kept project's %s = %d, want %d", tc.name, n, tc.want)
		}
	}
}
