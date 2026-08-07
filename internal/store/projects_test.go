package store

import (
	"errors"
	"testing"
)

func intPtr(v int) *int { return &v }

func TestProjectRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	in := &Project{
		Name:             "vincent",
		Path:             `C:\repos\vincent`,
		DefaultBranch:    "master",
		DefaultWorkflow:  "feature-pr",
		MaxParallelTasks: intPtr(2),
	}
	if err := s.CreateProject(ctx, in); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if in.ID == 0 {
		t.Fatal("CreateProject did not assign an ID")
	}

	got, err := s.GetProject(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Name != in.Name || got.Path != in.Path || got.DefaultBranch != in.DefaultBranch ||
		got.DefaultWorkflow != in.DefaultWorkflow {
		t.Errorf("got %+v, want %+v", got, in)
	}
	if got.MaxParallelTasks == nil || *got.MaxParallelTasks != 2 {
		t.Errorf("MaxParallelTasks = %v, want 2", got.MaxParallelTasks)
	}
	if !got.CreatedAt.Equal(in.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, in.CreatedAt)
	}
}

func TestProjectNullableFields(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	in := &Project{Name: "bare", Path: "/repo", DefaultBranch: "main"}
	if err := s.CreateProject(ctx, in); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	got, err := s.GetProject(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.DefaultWorkflow != "" {
		t.Errorf("DefaultWorkflow = %q, want empty", got.DefaultWorkflow)
	}
	if got.MaxParallelTasks != nil {
		t.Errorf("MaxParallelTasks = %v, want nil (unlimited)", *got.MaxParallelTasks)
	}
}

func TestProjectUpdate(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	p := &Project{Name: "old", Path: "/old", DefaultBranch: "main"}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	p.Name = "new"
	p.Path = "/new"
	p.DefaultBranch = "develop"
	p.DefaultWorkflow = "wf"
	p.MaxParallelTasks = intPtr(4)
	if err := s.UpdateProject(ctx, p); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}

	got, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Name != "new" || got.Path != "/new" || got.DefaultBranch != "develop" ||
		got.DefaultWorkflow != "wf" || got.MaxParallelTasks == nil || *got.MaxParallelTasks != 4 {
		t.Errorf("update not persisted: %+v", got)
	}
	if got.UpdatedAt.Before(got.CreatedAt) {
		t.Errorf("UpdatedAt %v precedes CreatedAt %v", got.UpdatedAt, got.CreatedAt)
	}

	if err := s.UpdateProject(ctx, &Project{ID: 9999, Name: "x", Path: "/x", DefaultBranch: "m"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateProject(missing) = %v, want ErrNotFound", err)
	}
}

func TestProjectListAndDelete(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	a := &Project{Name: "a", Path: "/a", DefaultBranch: "main"}
	b := &Project{Name: "b", Path: "/b", DefaultBranch: "main"}
	for _, p := range []*Project{a, b} {
		if err := s.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
	}

	list, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Errorf("ListProjects = %+v, want [a b] by id", list)
	}

	if err := s.DeleteProject(ctx, a.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := s.GetProject(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetProject(deleted) = %v, want ErrNotFound", err)
	}
	if err := s.DeleteProject(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteProject(again) = %v, want ErrNotFound", err)
	}
}

func TestProjectDuplicateNameRejected(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	if err := s.CreateProject(ctx, &Project{Name: "dup", Path: "/1", DefaultBranch: "main"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := s.CreateProject(ctx, &Project{Name: "dup", Path: "/2", DefaultBranch: "main"}); err == nil {
		t.Error("duplicate project name accepted; want UNIQUE violation")
	}
}
