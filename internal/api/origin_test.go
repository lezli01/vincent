package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
)

// Workflow origin over the API (task 043, issue #145). §5.2's built-in
// shadowing is unchanged: a project or global `adhoc.yaml` still wins. What
// these assert is that the substitution is *visible* on the task afterwards.

type originBody struct {
	Scope        string `json:"scope"`
	File         string `json:"file"`
	Digest       string `json:"digest"`
	ParentTaskID *int64 `json:"parent_task_id"`
}

// originOf creates a task and returns its id and the origin the API reports.
func originOf(t *testing.T, h *workflowHarness, req map[string]any) (int64, originBody) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created struct {
		ID     int64      `json:"id"`
		Origin originBody `json:"workflow_origin"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID, created.Origin
}

// fetchOrigin re-reads a task and returns the origin on the detail response.
func fetchOrigin(t *testing.T, h *workflowHarness, id int64) originBody {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, "/v1/tasks/"+itoa(id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.StatusCode, body)
	}
	var got struct {
		Origin originBody `json:"workflow_origin"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Origin
}

// TestTaskOriginSeesAShadowedAdhoc is the issue's actual complaint. Two
// projects, one versioning `.vincent/workflows/adhoc.yaml` and one not, each
// creating a task with `workflow` omitted. Both run — resolution is unchanged
// — and the task row says which definition each one got.
func TestTaskOriginSeesAShadowedAdhoc(t *testing.T) {
	h := newWorkflowHarness(t)
	shadowingRepo := testrepo.Init(t, "main")
	shadowSrc := manualYAML(workflow.AdhocName, "the project's own adhoc")
	writeWorkflowFile(t, filepath.Join(shadowingRepo, workflow.ProjectDirName),
		workflow.AdhocName, shadowSrc)

	shadowing := h.mustCreate(t, map[string]any{"path": shadowingRepo})
	plain := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})

	_, shadowed := originOf(t, h, map[string]any{
		"project_id": int64(shadowing["id"].(float64)), "title": "implicit adhoc",
	})
	if shadowed.Scope != store.WorkflowScopeProject {
		t.Errorf("scope = %q, want %q", shadowed.Scope, store.WorkflowScopeProject)
	}
	if want := ".vincent/workflows/adhoc.yaml"; shadowed.File != want {
		t.Errorf("file = %q, want %q", shadowed.File, want)
	}
	if want := workflow.SourceDigest(shadowSrc); shadowed.Digest != want {
		t.Errorf("digest = %q, want %q — the bytes the registry loaded", shadowed.Digest, want)
	}

	_, builtin := originOf(t, h, map[string]any{
		"project_id": int64(plain["id"].(float64)), "title": "implicit adhoc",
	})
	if builtin.Scope != store.WorkflowScopeBuiltin {
		t.Errorf("scope = %q, want %q", builtin.Scope, store.WorkflowScopeBuiltin)
	}
	if builtin.File != "" {
		t.Errorf("file = %q, want none: a built-in has no file", builtin.File)
	}
	if builtin.Digest == shadowed.Digest {
		t.Error("both tasks report the same digest; the shadowed adhoc is still invisible")
	}
}

// TestTaskOriginIsFrozenAtCreation: editing the workflow file afterwards must
// not rewrite an existing task's provenance, and neither must a rewrite of the
// snapshot — the digest names the file version the task was created from, not
// the bytes the engine is executing (decision 3).
func TestTaskOriginIsFrozenAtCreation(t *testing.T) {
	h := newWorkflowHarness(t)
	repo := testrepo.Init(t, "main")
	dir := filepath.Join(repo, workflow.ProjectDirName)
	writeWorkflowFile(t, dir, "release", manualYAML("release", "before"))
	p := h.mustCreate(t, map[string]any{"path": repo})

	id, at := originOf(t, h, map[string]any{
		"project_id": int64(p["id"].(float64)), "title": "frozen", "workflow": "release",
	})

	writeWorkflowFile(t, dir, "release", manualYAML("release", "after"))
	h.reg.Reload()
	if got := fetchOrigin(t, h, id); got != at {
		t.Errorf("an edit to the file moved an existing task's origin:\n %+v\nwas\n %+v", got, at)
	}

	// The snapshot's own bytes move under `edit + retry`; the origin does not.
	task, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	task.WorkflowSnapshot = manualYAML("release", "rewritten by edit+retry")
	if err := h.store.UpdateTask(t.Context(), task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if got := fetchOrigin(t, h, id); got != at {
		t.Errorf("a rewritten snapshot moved the origin:\n %+v\nwas\n %+v", got, at)
	}
}

// TestTaskOriginDigestsTheFileNotTheSnapshot: include expansion re-marshals the
// snapshot at creation (§7.9), so the two genuinely differ. The digest is of
// the caller's own source, which is what makes it identify a registry file.
func TestTaskOriginDigestsTheFileNotTheSnapshot(t *testing.T) {
	h := newWorkflowHarness(t)
	rootSrc := "name: root\nsteps:\n" +
		"  - {id: work, type: manual, instructions: do the thing}\n" +
		"  - {id: verify, type: include, workflow: checks}\n"
	writeWorkflowFile(t, h.globalDir, "checks",
		"name: checks\nsteps:\n  - {id: gate, type: manual, instructions: check it}\n")
	writeWorkflowFile(t, h.globalDir, "root", rootSrc)
	h.reg.ReloadGlobal()
	p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})

	id, got := originOf(t, h, map[string]any{
		"project_id": int64(p["id"].(float64)), "title": "included", "workflow": "root",
	})
	if want := workflow.SourceDigest(rootSrc); got.Digest != want {
		t.Errorf("digest = %q, want %q — the caller's own source bytes", got.Digest, want)
	}
	task, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.WorkflowSnapshot == rootSrc {
		t.Fatal("the snapshot was not expanded; this test proves nothing")
	}
	if strings.Contains(task.WorkflowSnapshot, "type: include") {
		t.Error("the snapshot still carries an include step")
	}
	if got.Digest == workflow.SourceDigest(task.WorkflowSnapshot) {
		t.Error("the digest tracks the snapshot rather than the file it came from")
	}
}
