package api

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// TestParseSnapshotCarriesStepText: every step type contributes the text a
// human can edit before retrying it (§6 edit+retry), and a manual step
// contributes its instructions.
func TestParseSnapshotCarriesStepText(t *testing.T) {
	const src = `name: x
steps:
  - id: implement
    type: agent
    prompt: write the thing
  - id: review
    type: manual
    instructions: look at the diff
  - id: publish
    type: command
    run: git push
`
	got := parseSnapshot(src, 10).steps
	want := []stepDefinition{
		{index: 0, id: "implement", stepType: "agent", prompt: "write the thing"},
		{index: 1, id: "review", stepType: "manual", instructions: "look at the diff"},
		{index: 2, id: "publish", stepType: "command", run: "git push"},
	}
	if len(got) != len(want) {
		t.Fatalf("steps = %d, want %d", len(got), len(want))
	}
	// reflect.DeepEqual rather than ==: stepDefinition carries the §7.9
	// provenance chain, and a struct holding a slice is not comparable.
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("step %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestWorkflowStepsOnTaskDetail: the detail response carries the snapshot as
// typed steps, so a client never parses workflow YAML to prefill an editor.
func TestWorkflowStepsOnTaskDetail(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)

	got := h.getTask(t, task.ID)
	if len(got.WorkflowSteps) != 1 {
		t.Fatalf("workflow_steps = %d, want 1 (adhoc is one agent step)", len(got.WorkflowSteps))
	}
	step := got.WorkflowSteps[0]
	if step.Type != "agent" || step.Prompt == "" {
		t.Errorf("step = %+v, want an agent step carrying its prompt", step)
	}
	if step.Run != "" || step.Instructions != "" {
		t.Errorf("step = %+v, want run/instructions empty on an agent step", step)
	}
	if len(got.Steps) != 0 {
		t.Errorf("steps = %d, want none: the task has never run", len(got.Steps))
	}
}

// TestEditRetryRefreshesWorkflowSteps: edit+retry rewrites this task's
// snapshot (§6), which is the one write that can make a cached parse wrong.
// Without the retry handler forgetting the entry, the second read serves the
// pre-edit prompt forever.
func TestEditRetryRefreshesWorkflowSteps(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)

	// Prime the cache: this is the read a detail view does before the human
	// decides to edit anything.
	before := h.getTask(t, task.ID).WorkflowSteps[0].Prompt
	setState(t, h, task.ID, store.TaskBlocked)

	const edited = "try it this way instead"
	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/retry", task.ID),
		map[string]any{"prompt_override": edited})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry: %d %s", resp.StatusCode, body)
	}
	if got := decodeTask(t, body).WorkflowSteps; len(got) != 0 {
		t.Errorf("workflow_steps on an action response = %d, want none: detail view only", len(got))
	}

	after := h.getTask(t, task.ID).WorkflowSteps[0].Prompt
	if after == before {
		t.Fatalf("prompt = %q after edit+retry, want the edited text %q", after, edited)
	}
	if after != edited {
		t.Errorf("prompt = %q, want %q", after, edited)
	}
}

// getTask reads GET /v1/tasks/{id}.
func (h *taskHarness) getTask(t *testing.T, id int64) taskResponse {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.StatusCode, body)
	}
	return decodeTask(t, body)
}
