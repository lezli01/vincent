package store

import (
	"reflect"
	"testing"
)

// The `workflow_origin_json` column (migration 0017, task 043). It records
// which definition a task's workflow name resolved to, so a project or global
// file shadowing a built-in is visible on the task forever afterwards. Like
// `github_issue_json` beside it, it is written once and never recomputed.

func projectOrigin() *WorkflowOrigin {
	return &WorkflowOrigin{
		Scope:  WorkflowScopeProject,
		File:   ".vincent/workflows/adhoc.yaml",
		Digest: "sha256:0f4a1c1e0f4a1c1e0f4a1c1e0f4a1c1e0f4a1c1e0f4a1c1e0f4a1c1e0f4a1c1e",
	}
}

func TestTaskWorkflowOriginRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "provenance")

	in := newTask(p.ID, "shadowed-adhoc", TaskQueued)
	in.WorkflowOrigin = projectOrigin()
	if err := s.CreateTask(ctx, in, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	out, err := s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if out.WorkflowOrigin == nil {
		t.Fatal("the stored task carries no workflow origin")
	}
	if !reflect.DeepEqual(*out.WorkflowOrigin, *projectOrigin()) {
		t.Errorf("origin round-tripped as\n %+v\nwant\n %+v", *out.WorkflowOrigin, *projectOrigin())
	}
}

// TestTaskDerivedWorkflowOriginRoundTrip: a fan-out lane carries `derived`
// naming its parent instead of a file and digest (decision 6), so the parent id
// has to survive the column too.
func TestTaskDerivedWorkflowOriginRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "lanes")

	parent := newTask(p.ID, "root", TaskQueued)
	if err := s.CreateTask(ctx, parent, nil); err != nil {
		t.Fatalf("CreateTask parent: %v", err)
	}
	in := newTask(p.ID, "lane", TaskQueued)
	in.WorkflowOrigin = &WorkflowOrigin{Scope: WorkflowScopeDerived, ParentTaskID: &parent.ID}
	if err := s.CreateTask(ctx, in, nil); err != nil {
		t.Fatalf("CreateTask lane: %v", err)
	}
	out, err := s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if out.WorkflowOrigin == nil || out.WorkflowOrigin.Scope != WorkflowScopeDerived {
		t.Fatalf("lane origin = %+v, want scope %q", out.WorkflowOrigin, WorkflowScopeDerived)
	}
	if out.WorkflowOrigin.ParentTaskID == nil || *out.WorkflowOrigin.ParentTaskID != parent.ID {
		t.Errorf("lane origin parent = %v, want %d", out.WorkflowOrigin.ParentTaskID, parent.ID)
	}
	if out.WorkflowOrigin.File != "" || out.WorkflowOrigin.Digest != "" {
		t.Errorf("lane origin claims a file or digest it does not have: %+v", out.WorkflowOrigin)
	}
}

// TestTaskWithoutAnOriginStoresNull: a row created before migration 0017 has
// no provenance, and reads back as *unrecorded* rather than as a zero-valued
// origin — "created before origin was recorded" and "created from an empty
// scope" are different claims and only one of them is true (decision 4).
func TestTaskWithoutAnOriginStoresNull(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "legacy")

	in := newTask(p.ID, "pre-0017", TaskQueued)
	if err := s.CreateTask(ctx, in, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	out, err := s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if out.WorkflowOrigin != nil {
		t.Errorf("a task with no recorded origin read back with one: %+v", out.WorkflowOrigin)
	}
	var raw any
	if err := s.db.QueryRowContext(ctx,
		`SELECT workflow_origin_json FROM tasks WHERE id = ?`, in.ID).Scan(&raw); err != nil {
		t.Fatalf("read column: %v", err)
	}
	if raw != nil {
		t.Errorf("workflow_origin_json = %v, want SQL NULL", raw)
	}
}

// TestTaskOriginSurvivesATransition: the origin is not part of any
// transition's write set, so walking the §6 lifecycle must not drop it.
func TestTaskOriginSurvivesATransition(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "transitions-origin")

	in := newTask(p.ID, "shadowed", TaskQueued)
	in.WorkflowOrigin = projectOrigin()
	if err := s.CreateTask(ctx, in, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	out, _, err := s.TransitionTask(ctx, in.ID, TaskQueued, TaskRunning, TaskChange{})
	if err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if out.WorkflowOrigin == nil || out.WorkflowOrigin.File != projectOrigin().File {
		t.Errorf("the origin did not survive a transition: %+v", out.WorkflowOrigin)
	}
}
