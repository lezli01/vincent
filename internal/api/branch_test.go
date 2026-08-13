package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// setProjectBranchTemplate points the harness project at a convention.
func (h *taskHarness) setProjectBranchTemplate(t *testing.T, tmpl string) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPatch, "/v1/projects/"+itoa(h.projectID),
		map[string]any{"branch_template": tmpl})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set branch_template: %d %s", resp.StatusCode, body)
	}
}

// createTaskRejected posts a task the daemon should refuse with a 400 and returns
// the message, which is the part a user acts on.
func (h *taskHarness) createTaskRejected(t *testing.T, req map[string]any) string {
	t.Helper()
	if _, ok := req["project_id"]; !ok {
		req["project_id"] = h.projectID
	}
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create task: status %d (want 400): %s", resp.StatusCode, body)
	}
	var e errorBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body: %v", err)
	}
	return e.Error.Message
}

// The default is unchanged by the whole feature: a task with nothing configured
// still gets vincent/{id}-{slug}. This is the regression guard for every existing
// user, and it is first for that reason.
func TestCreateTaskDefaultBranchUnchanged(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	got := h.createTask(t, map[string]any{"title": "Fix login"})
	if want := "vincent/" + itoa(got.ID) + "-fix-login"; got.BranchName != want {
		t.Fatalf("branch = %q, want %q", got.BranchName, want)
	}
}

func TestCreateTaskWithBranchName(t *testing.T) {
	h := newTaskHarness(t, 0, false)

	t.Run("a literal is used verbatim", func(t *testing.T) {
		got := h.createTask(t, map[string]any{
			"title": "Retry logic", "branch_name": "feat/OPS-123-retry",
		})
		if got.BranchName != "feat/OPS-123-retry" {
			t.Fatalf("branch = %q", got.BranchName)
		}
	})

	t.Run("an illegal name is a 400 naming the rules", func(t *testing.T) {
		msg := h.createTaskRejected(t, map[string]any{
			"title": "bad", "branch_name": "feat/../escape",
		})
		if !strings.Contains(msg, "not valid") {
			t.Fatalf("message = %q, want it to explain the name is invalid", msg)
		}
	})

	t.Run("a name an existing git branch holds is a 400", func(t *testing.T) {
		testrepo.Run(t, h.repo, "branch", "already/there")
		msg := h.createTaskRejected(t, map[string]any{
			"title": "clash", "branch_name": "already/there",
		})
		if !strings.Contains(msg, "already exists") {
			t.Fatalf("message = %q", msg)
		}
	})

	// The directory/file conflict: nothing is named `parent/x`, yet it cannot be
	// created because a branch exists beneath it. An exact-match pre-check reports
	// this name as free, which is why the probe is wider.
	t.Run("a directory/file conflict is a 400 that explains itself", func(t *testing.T) {
		testrepo.Run(t, h.repo, "branch", "parent/x/deeper")
		msg := h.createTaskRejected(t, map[string]any{
			"title": "dfclash", "branch_name": "parent/x",
		})
		if !strings.Contains(msg, "hierarchy") {
			t.Fatalf("message = %q, want it to explain the ref hierarchy", msg)
		}
	})

	// Neither branch exists yet, so git cannot object; the claim check inside the
	// insert transaction is what catches it.
	t.Run("a name another live task claims is a 400 naming that task", func(t *testing.T) {
		first := h.createTask(t, map[string]any{
			"title": "first", "branch_name": "feat/contended",
		})
		msg := h.createTaskRejected(t, map[string]any{
			"title": "second", "branch_name": "feat/contended",
		})
		if !strings.Contains(msg, "claimed by task "+itoa(first.ID)) {
			t.Fatalf("message = %q, want it to name task %d", msg, first.ID)
		}
	})
}

func TestCreateTaskWithProjectTemplate(t *testing.T) {
	h := newTaskHarness(t, 0, false)

	t.Run("an id-less template resolves before the insert", func(t *testing.T) {
		h.setProjectBranchTemplate(t, `feat/{{.Slug}}`)
		got := h.createTask(t, map[string]any{"title": "Add a health endpoint"})
		if got.BranchName != "feat/add-a-health-endpoint" {
			t.Fatalf("branch = %q", got.BranchName)
		}
	})

	t.Run("an id-bearing template resolves inside the insert", func(t *testing.T) {
		h.setProjectBranchTemplate(t, `t{{.ID}}/{{.Slug}}`)
		got := h.createTask(t, map[string]any{"title": "Second one"})
		if want := "t" + itoa(got.ID) + "/second-one"; got.BranchName != want {
			t.Fatalf("branch = %q, want %q", got.BranchName, want)
		}
	})

	t.Run("a literal still beats the template", func(t *testing.T) {
		h.setProjectBranchTemplate(t, `feat/{{.Slug}}`)
		got := h.createTask(t, map[string]any{
			"title": "Third", "branch_name": "release/hotfix",
		})
		if got.BranchName != "release/hotfix" {
			t.Fatalf("branch = %q", got.BranchName)
		}
	})

	// A template can reference a field, and a task without it must fail loudly
	// rather than render a hole: `feat/-slug` is a legal ref, so nothing further
	// down would catch it.
	t.Run("a template needing a missing field is a 400", func(t *testing.T) {
		h.setProjectBranchTemplate(t, `feat/{{.Fields.ticket}}-{{.Slug}}`)
		msg := h.createTaskRejected(t, map[string]any{"title": "no ticket"})
		if !strings.Contains(msg, "could not be resolved") {
			t.Fatalf("message = %q", msg)
		}
		// With the field supplied it goes through.
		got := h.createTask(t, map[string]any{
			"title": "has ticket", "fields": map[string]string{"ticket": "OPS-9"},
		})
		if got.BranchName != "feat/OPS-9-has-ticket" {
			t.Fatalf("branch = %q", got.BranchName)
		}
	})

	// An id-bearing template that renders an illegal name is rejected before the
	// row exists, using a placeholder id — legality does not depend on the digits.
	t.Run("an id-bearing template producing an illegal name is a 400", func(t *testing.T) {
		h.setProjectBranchTemplate(t, `feat/{{.ID}}.lock`)
		msg := h.createTaskRejected(t, map[string]any{"title": "x"})
		if !strings.Contains(msg, "not valid") {
			t.Fatalf("message = %q", msg)
		}
	})
}

func TestProjectBranchTemplateIsValidatedOnWrite(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	resp, body := h.doJSON(t, http.MethodPatch, "/v1/projects/"+itoa(h.projectID),
		map[string]any{"branch_template": `feat/{{.Slug`})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, body)
	}
	// Clearing it restores inheritance from config.yaml.
	h.setProjectBranchTemplate(t, "")
	got := h.createTask(t, map[string]any{"title": "back to default"})
	if want := "vincent/" + itoa(got.ID) + "-back-to-default"; got.BranchName != want {
		t.Fatalf("branch = %q, want %q", got.BranchName, want)
	}
}

// No committed task may carry an empty branch_name, and a rejected creation must
// leave nothing behind — the invariant that replaced the two-write window.
func TestCreateTaskBranchIsAtomic(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	ctx := t.Context()

	h.createTask(t, map[string]any{"title": "one", "branch_name": "feat/atomic"})
	h.createTaskRejected(t, map[string]any{
		"title": "rejected", "branch_name": "feat/atomic",
	})

	tasks, err := h.store.ListTasks(ctx, store.TaskFilter{Archived: store.ArchivedAll})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	for _, task := range tasks {
		if task.BranchName == "" {
			t.Errorf("task %d committed with an empty branch_name", task.ID)
		}
		if task.Title == "rejected" {
			t.Errorf("task %d was committed even though creation was rejected", task.ID)
		}
	}
}

func TestResolvePreviewsTheBranch(t *testing.T) {
	h := newTaskHarness(t, 0, false)

	preview := func(t *testing.T, req map[string]any) resolvedBranch {
		t.Helper()
		req["workflow"] = "adhoc"
		req["project_id"] = h.projectID
		resp, body := h.doJSON(t, http.MethodPost, "/v1/resolve", req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("resolve: %d %s", resp.StatusCode, body)
		}
		var out struct {
			Branch *resolvedBranch `json:"branch"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("resolve body: %v", err)
		}
		if out.Branch == nil {
			t.Fatal("resolve returned no branch preview")
		}
		return *out.Branch
	}

	t.Run("the default reports a placeholder, never a guessed id", func(t *testing.T) {
		got := preview(t, map[string]any{"title": "Fix login"})
		if got.Value != "vincent/<id>-fix-login" || !got.Placeholder {
			t.Fatalf("preview = %+v", got)
		}
		if got.Source != "default" {
			t.Fatalf("source = %q, want default", got.Source)
		}
	})

	t.Run("a project template is named as the source", func(t *testing.T) {
		h.setProjectBranchTemplate(t, `feat/{{.Slug}}`)
		got := preview(t, map[string]any{"title": "Fix login"})
		if got.Value != "feat/fix-login" || got.Placeholder {
			t.Fatalf("preview = %+v", got)
		}
		if got.Source != "project" {
			t.Fatalf("source = %q, want project", got.Source)
		}
	})

	t.Run("a typed literal wins and is reported as the task's own", func(t *testing.T) {
		got := preview(t, map[string]any{"title": "Fix login", "branch_name": "release/x"})
		if got.Value != "release/x" || got.Source != "task" {
			t.Fatalf("preview = %+v", got)
		}
	})

	t.Run("a template referencing a missing field is a 400, not a hole", func(t *testing.T) {
		h.setProjectBranchTemplate(t, `feat/{{.Fields.ticket}}`)
		resp, _ := h.doJSON(t, http.MethodPost, "/v1/resolve", map[string]any{
			"workflow": "adhoc", "project_id": h.projectID, "title": "no ticket",
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})
}

// The recovery path for the failure this whole feature makes reachable: a task
// blocked because its branch already exists. Nothing else in the API can change a
// branch name, so without branch_override such a task is permanently dead and its
// transcripts orphaned.
func TestRetryWithBranchOverride(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskBlocked)
	before := decodeTask(t, mustGetTask(t, h, task.ID))

	t.Run("a free name is adopted and the task re-admitted", func(t *testing.T) {
		resp, body := h.doJSON(t, http.MethodPost,
			"/v1/tasks/"+itoa(task.ID)+"/retry",
			map[string]any{"branch_override": "feat/second-attempt"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("retry: %d %s", resp.StatusCode, body)
		}
		got := decodeTask(t, body)
		if got.BranchName != "feat/second-attempt" {
			t.Fatalf("branch = %q, want feat/second-attempt", got.BranchName)
		}
		if got.State != string(store.TaskQueued) {
			t.Fatalf("state = %s, want queued", got.State)
		}
		// The same task, so its history and transcripts survive the rename.
		if got.ID != before.ID {
			t.Fatalf("id changed from %d to %d", before.ID, got.ID)
		}
	})

	t.Run("an illegal name is refused and the branch is left alone", func(t *testing.T) {
		setState(t, h, task.ID, store.TaskBlocked)
		resp, _ := h.doJSON(t, http.MethodPost,
			"/v1/tasks/"+itoa(task.ID)+"/retry",
			map[string]any{"branch_override": "feat/bad..name"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		got := decodeTask(t, mustGetTask(t, h, task.ID))
		if got.BranchName != "feat/second-attempt" {
			t.Fatalf("branch = %q, want it unchanged", got.BranchName)
		}
		if got.State != string(store.TaskBlocked) {
			t.Fatalf("state = %s, want it still blocked", got.State)
		}
	})

	t.Run("a name that still collides is refused, and the task stays blocked", func(t *testing.T) {
		setState(t, h, task.ID, store.TaskBlocked)
		testrepo.Run(t, h.repo, "branch", "feat/occupied")
		resp, body := h.doJSON(t, http.MethodPost,
			"/v1/tasks/"+itoa(task.ID)+"/retry",
			map[string]any{"branch_override": "feat/occupied"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", resp.StatusCode, body)
		}
		if got := decodeTask(t, mustGetTask(t, h, task.ID)); got.State != string(store.TaskBlocked) {
			t.Fatalf("state = %s, want it still blocked", got.State)
		}
	})

	// Renaming only makes sense for a task that has not started; a running task
	// already has a worktree checked out on the old name.
	t.Run("a non-blocked task is a 409, and nothing is renamed", func(t *testing.T) {
		setState(t, h, task.ID, store.TaskQueued)
		resp, body := h.doJSON(t, http.MethodPost,
			"/v1/tasks/"+itoa(task.ID)+"/retry",
			map[string]any{"branch_override": "feat/too-late"})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", resp.StatusCode, body)
		}
		if got := decodeTask(t, mustGetTask(t, h, task.ID)); got.BranchName == "feat/too-late" {
			t.Fatal("branch was renamed on a task that could not be retried")
		}
	})
}

func mustGetTask(t *testing.T, h *taskHarness, id int64) []byte {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, "/v1/tasks/"+itoa(id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.StatusCode, body)
	}
	return body
}
