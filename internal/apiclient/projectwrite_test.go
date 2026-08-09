package apiclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

func TestCreateProjectDerivesNameAndBranchFromThePath(t *testing.T) {
	h := newCreateHarness(t)
	repo := testrepo.Init(t, "trunk")

	// Only the path: the whole point of the omitted fields is that the
	// daemon fills them in, so the form never has to guess a branch name.
	p, err := h.client.CreateProject(t.Context(), apiclient.CreateProjectRequest{Path: repo})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.ID == 0 {
		t.Errorf("ID = 0, want an assigned id")
	}
	if p.DefaultBranch != "trunk" {
		t.Errorf("DefaultBranch = %q, want trunk (detected from the repo)", p.DefaultBranch)
	}
	if p.Name == "" {
		t.Errorf("Name = %q, want a name derived from the directory", p.Name)
	}
	if p.MaxParallelTasks != nil {
		t.Errorf("MaxParallelTasks = %v, want nil (no project cap)", *p.MaxParallelTasks)
	}
}

func TestCreateProjectRejectsANonRepositoryPath(t *testing.T) {
	h := newCreateHarness(t)
	_, err := h.client.CreateProject(t.Context(),
		apiclient.CreateProjectRequest{Path: t.TempDir()})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *apiclient.Error", err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", apiErr.Status)
	}
	if apiErr.Message == "" {
		t.Error("message is empty; the form renders it on the path row")
	}
}

// The three PATCH states have to survive encoding, or "clear the default
// workflow" and "leave it alone" become the same request.
func TestPatchProjectDistinguishesAbsentNullAndSet(t *testing.T) {
	h := newCreateHarness(t)
	ctx := t.Context()

	cap3 := 3
	adhoc := "two-step"
	if _, err := h.client.PatchProject(ctx, h.projectID, apiclient.PatchProjectRequest{
		DefaultWorkflow:  apiclient.SetOpt(adhoc),
		MaxParallelTasks: apiclient.SetOpt(cap3),
	}); err != nil {
		t.Fatalf("PatchProject set: %v", err)
	}
	p := h.project(ctx, t)
	if p.DefaultWorkflow == nil || *p.DefaultWorkflow != adhoc {
		t.Fatalf("DefaultWorkflow = %v, want %q", p.DefaultWorkflow, adhoc)
	}
	if p.MaxParallelTasks == nil || *p.MaxParallelTasks != cap3 {
		t.Fatalf("MaxParallelTasks = %v, want %d", p.MaxParallelTasks, cap3)
	}

	// Absent: a patch that names only the workflow must not touch the cap.
	if _, err := h.client.PatchProject(ctx, h.projectID, apiclient.PatchProjectRequest{
		DefaultWorkflow: apiclient.NullOpt[string](),
	}); err != nil {
		t.Fatalf("PatchProject null: %v", err)
	}
	p = h.project(ctx, t)
	if p.DefaultWorkflow != nil {
		t.Errorf("DefaultWorkflow = %q, want nil after an explicit null", *p.DefaultWorkflow)
	}
	if p.MaxParallelTasks == nil || *p.MaxParallelTasks != cap3 {
		t.Errorf("MaxParallelTasks = %v, want %d untouched by an absent field", p.MaxParallelTasks, cap3)
	}

	if _, err := h.client.PatchProject(ctx, h.projectID, apiclient.PatchProjectRequest{
		MaxParallelTasks: apiclient.NullOpt[int](),
	}); err != nil {
		t.Fatalf("PatchProject clear cap: %v", err)
	}
	if p = h.project(ctx, t); p.MaxParallelTasks != nil {
		t.Errorf("MaxParallelTasks = %d, want nil (no project cap)", *p.MaxParallelTasks)
	}
}

// Encoding is asserted directly too: omitzero on an Opt is what makes an
// untouched field absent rather than a null that clears it.
func TestPatchProjectRequestOmitsUntouchedFields(t *testing.T) {
	body, err := json.Marshal(apiclient.PatchProjectRequest{
		Name: apiclient.SetOpt("renamed"),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(body), `{"name":"renamed"}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	body, err = json.Marshal(apiclient.PatchProjectRequest{
		MaxParallelTasks: apiclient.NullOpt[int](),
	})
	if err != nil {
		t.Fatalf("marshal null: %v", err)
	}
	if got, want := string(body), `{"max_parallel_tasks":null}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

// Zero is not "no cap" — null is. The daemon owns that rule and the form
// lets its message through rather than re-deriving it.
func TestPatchProjectRejectsAZeroCap(t *testing.T) {
	h := newCreateHarness(t)
	_, err := h.client.PatchProject(t.Context(), h.projectID, apiclient.PatchProjectRequest{
		MaxParallelTasks: apiclient.SetOpt(0),
	})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *apiclient.Error", err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", apiErr.Status)
	}
}

func TestDeleteProjectRemovesAnEmptyProject(t *testing.T) {
	h := newCreateHarness(t)
	if err := h.client.DeleteProject(t.Context(), h.projectID, false); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	projects, err := h.client.ListProjects(t.Context())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("projects = %d, want 0 after delete", len(projects))
	}
}

// The forceable 409: tasks exist, so the caller confirms and re-issues.
func TestDeleteProjectWithTasksNeedsForce(t *testing.T) {
	h := newCreateHarness(t)
	ctx := t.Context()
	h.newTask(ctx, t, store.TaskQueued)

	err := h.client.DeleteProject(ctx, h.projectID, false)
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *apiclient.Error", err)
	}
	if apiErr.Status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", apiErr.Status)
	}
	if err := h.client.DeleteProject(ctx, h.projectID, true); err != nil {
		t.Fatalf("forced DeleteProject: %v", err)
	}
}

// The 409 that force does not fix. Re-issuing with force would fail the
// same way, which is why the caller must not offer it as a confirmation.
func TestDeleteProjectWithARunningTaskRefusesForceToo(t *testing.T) {
	h := newCreateHarness(t)
	ctx := t.Context()
	task := h.newTask(ctx, t, store.TaskQueued)
	if _, _, err := h.store.TransitionTask(
		ctx, task.ID, store.TaskQueued, store.TaskRunning, store.TaskChange{},
	); err != nil {
		t.Fatalf("transition to running: %v", err)
	}

	for _, force := range []bool{false, true} {
		err := h.client.DeleteProject(ctx, h.projectID, force)
		var apiErr *apiclient.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("force=%v: err = %v, want *apiclient.Error", force, err)
		}
		if apiErr.Status != http.StatusConflict {
			t.Errorf("force=%v: status = %d, want 409", force, apiErr.Status)
		}
	}
}

func (h *createHarness) project(ctx context.Context, t *testing.T) apiclient.Project {
	t.Helper()
	projects, err := h.client.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range projects {
		if p.ID == h.projectID {
			return p
		}
	}
	t.Fatalf("project %d missing from %v", h.projectID, projects)
	return apiclient.Project{}
}

func (h *createHarness) newTask(ctx context.Context, t *testing.T, state store.TaskState) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID:    h.projectID,
		Title:        "a task",
		WorkflowName: "two-step",
		BaseBranch:   "main",
		State:        state,
	}
	if err := h.store.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}
