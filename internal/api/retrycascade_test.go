package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/worktree"
)

// parkedParent is a task in `awaiting_children` — what a `fan_out` step
// leaves behind while its lanes run — with `states` worth of lanes hanging
// off it. It builds the shape rather than running a fan-out, because what is
// under test here is the HTTP surface and not the engine.
func parkedParent(t *testing.T, h *taskHarness, states ...store.TaskState) (taskResponse, []*store.Task) {
	t.Helper()
	parent := queuedTask(t, h)
	setState(t, h, parent.ID, store.TaskAwaitingChildren)
	stored, err := h.store.GetTask(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	lanes := make([]*store.Task, 0, len(states))
	for i, state := range states {
		index := 0
		child := &store.Task{
			ProjectID:        h.projectID,
			Title:            fmt.Sprintf("lane %d", i),
			WorkflowName:     stored.WorkflowName,
			WorkflowSnapshot: stored.WorkflowSnapshot,
			BaseBranch:       stored.BranchName,
			State:            state,
			ParentTaskID:     &stored.ID,
			ParentStepIndex:  &index,
		}
		resolve := func(id int64) (string, error) { return worktree.BranchName(id, child.Title), nil }
		if err := h.store.CreateTask(t.Context(), child, resolve); err != nil {
			t.Fatalf("create lane: %v", err)
		}
		lanes = append(lanes, child)
	}
	// Re-read over the API: available_actions is derived per request, and the
	// lanes were attached after the create response was written.
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", parent.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get parent: %d %s", resp.StatusCode, body)
	}
	return decodeTask(t, body), lanes
}

// decodeRetry reads a retry response body, which is a task plus the cascade's
// count at the top level.
func decodeRetry(t *testing.T, body []byte) retryResponse {
	t.Helper()
	var out retryResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode retry: %v (%s)", err, body)
	}
	return out
}

// TestRetryFromParkedParentIs200 is task 090 at the HTTP seam: the action
// that used to be a 409 from `awaiting_children` now re-admits the blocked
// lanes holding the join open, and says how many it reached.
func TestRetryFromParkedParentIs200(t *testing.T) {
	h := newActionHarness(t)
	parent, lanes := parkedParent(t, h,
		store.TaskBlocked, store.TaskBlocked, store.TaskRunning)

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/retry", parent.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry from awaiting_children: %d %s, want 200", resp.StatusCode, body)
	}
	got := decodeRetry(t, body)
	if got.RetriedDescendants != 2 {
		t.Errorf("retried_descendants = %d, want 2 — the count the runner returned",
			got.RetriedDescendants)
	}
	// The parent's row is deliberately not written, so the body's task fields
	// are the parent as it stands.
	if got.ID != parent.ID {
		t.Errorf("id = %d, want the addressed task %d", got.ID, parent.ID)
	}
	if got.State != string(store.TaskAwaitingChildren) {
		t.Errorf("state = %s, want awaiting_children — the join is still open", got.State)
	}
	for _, lane := range lanes[:2] {
		if s := laneState(t, h, lane.ID); s != store.TaskQueued {
			t.Errorf("lane %d state = %s, want queued", lane.ID, s)
		}
	}
	if s := laneState(t, h, lanes[2].ID); s != store.TaskRunning {
		t.Errorf("running lane %d state = %s, want running — only blocked lanes cascade", lanes[2].ID, s)
	}
}

func laneState(t *testing.T, h *taskHarness, id int64) store.TaskState {
	t.Helper()
	task, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask %d: %v", id, err)
	}
	return task.State
}

// TestRetryReportsRetriedDescendantsAlways: the field is on every retry
// response, so a client never has to tell "no cascade" from a daemon that
// predates the field. An ordinary blocked retry with nothing under it is 0.
func TestRetryReportsRetriedDescendantsAlways(t *testing.T) {
	h := newActionHarness(t)
	task := blockedTask(t, h, taskrun.ReasonTimeout)

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/retry", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry: %d %s", resp.StatusCode, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if _, ok := raw["retried_descendants"]; !ok {
		t.Fatalf("body %s has no retried_descendants; it is always present", body)
	}
	// The task fields stay at the top level, flat, exactly as before.
	if _, ok := raw["state"]; !ok {
		t.Errorf("body %s nested the task; a retry response is still a task", body)
	}
	got := decodeRetry(t, body)
	if got.RetriedDescendants != 0 {
		t.Errorf("retried_descendants = %d, want 0 for a blocked task with no lanes",
			got.RetriedDescendants)
	}
	if got.State != string(store.TaskQueued) {
		t.Errorf("state = %s, want queued", got.State)
	}
}

// TestParkedParentOffersRetry: `available_actions` is derived from §6, so the
// endpoint and the bar a client renders cannot disagree about what is on
// offer while a join is open.
func TestParkedParentOffersRetry(t *testing.T) {
	h := newActionHarness(t)
	parent, _ := parkedParent(t, h, store.TaskBlocked)
	if !hasAction(parent.AvailableActions, "retry") {
		t.Errorf("awaiting_children actions = %v, must offer retry", parent.AvailableActions)
	}
}

// TestRetryFromParkedParentRefusesBranchOverride: renaming a parked parent's
// branch would move the name every live lane holds as its `base_branch`, so
// it is a 400 in front of the rename rather than a fan-out re-based onto a
// branch that no longer exists.
func TestRetryFromParkedParentRefusesBranchOverride(t *testing.T) {
	h := newActionHarness(t)
	parent, _ := parkedParent(t, h, store.TaskBlocked)

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/retry", parent.ID),
		map[string]any{"branch_override": "vincent/renamed"})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(body), "base_branch") {
		t.Errorf("message %s does not say what the rename would break", body)
	}
	stored, err := h.store.GetTask(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.BranchName != parent.BranchName {
		t.Errorf("branch_name = %q, want %q — a refused override renamed the branch anyway",
			stored.BranchName, parent.BranchName)
	}
}

// TestRetryFromParkedParentRefusesPromptOverride is the ParkedOverrideError
// mapping: a parked parent's cursor is on a `fan_out` step, which has neither
// a prompt nor a command for an override to rewrite.
func TestRetryFromParkedParentRefusesPromptOverride(t *testing.T) {
	for _, field := range []string{"prompt_override", "run_override"} {
		t.Run(field, func(t *testing.T) {
			h := newActionHarness(t)
			parent, lanes := parkedParent(t, h, store.TaskBlocked)

			resp, body := h.doJSON(t, http.MethodPost,
				fmt.Sprintf("/v1/tasks/%d/retry", parent.ID),
				map[string]any{field: "do it differently"})
			wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
			if s := laneState(t, h, lanes[0].ID); s != store.TaskBlocked {
				t.Errorf("lane %d state = %s, want blocked — a refused retry cascaded anyway",
					lanes[0].ID, s)
			}
		})
	}
}
