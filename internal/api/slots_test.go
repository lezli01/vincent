package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// seedSlotFixture builds the shape issue #324 was reported against: a fan-out
// parent parked on its lanes, one lane actually running, a root task parked on
// a question, and a queued root holding nothing. Two tasks hold a slot that a
// client counting `state == "running"` over root rows sees as zero.
func seedSlotFixture(t *testing.T, h *taskHarness) {
	t.Helper()
	mk := func(title string, state store.TaskState, parent *int64) *store.Task {
		task := &store.Task{
			ProjectID:        h.projectID,
			Title:            title,
			WorkflowName:     "adhoc",
			WorkflowSnapshot: "steps: []",
			BaseBranch:       "main",
			BranchName:       "vincent/" + title,
			State:            state,
			ParentTaskID:     parent,
		}
		if parent != nil {
			idx := 0
			task.ParentStepIndex = &idx
			task.LaneID = title
		}
		if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
			t.Fatalf("CreateTask(%s): %v", title, err)
		}
		return task
	}
	parent := mk("parent", store.TaskAwaitingChildren, nil)
	mk("lane-running", store.TaskRunning, &parent.ID)
	mk("root-asking", store.TaskAwaitingInput, nil)
	mk("root-queued", store.TaskQueued, nil)
}

// TestInfoReportsSlotsUsed is the daemon's half of issue #324: /v1/info serves
// the §11 count, so no client has to define a slot for itself. A lane running
// under a parked parent counts once — for the lane. The parent holds nothing,
// which is what keeps §7.6's deadlock-freedom argument true.
func TestInfoReportsSlotsUsed(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	seedSlotFixture(t, h)

	body, raw := infoBody(t, h.ts)
	slots, ok := body["slots"].(map[string]any)
	if !ok {
		t.Fatalf("info has no slots object: %s", raw)
	}
	for key, want := range map[string]float64{"used": 2, "lanes": 1, "awaiting_input": 1} {
		got, present := slots[key].(float64)
		if !present {
			t.Errorf("slots.%s is missing: %s", key, raw)
			continue
		}
		if got != want {
			t.Errorf("slots.%s = %v, want %v", key, got, want)
		}
	}
	// The count is only meaningful against the cap it is measured on, so
	// both must ride the same payload.
	if _, ok := body["max_parallel_tasks"]; !ok {
		t.Errorf("max_parallel_tasks is missing beside slots: %s", raw)
	}
}

// TestProjectResponsesCarrySlotsUsed holds the "every response, always"
// half: a client that saw the field on a list and not on a get would have to
// keep its own count for the difference, which is the bug again.
func TestProjectResponsesCarrySlotsUsed(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	seedSlotFixture(t, h)

	slotsUsed := func(body []byte) int {
		t.Helper()
		var out struct {
			SlotsUsed *int `json:"slots_used"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode project: %v (%s)", err, body)
		}
		if out.SlotsUsed == nil {
			t.Fatalf("slots_used is absent or null: %s", body)
		}
		return *out.SlotsUsed
	}

	resp, body := h.doJSON(t, http.MethodGet, "/v1/projects", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list projects: %d %s", resp.StatusCode, body)
	}
	var list []json.RawMessage
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d projects, want 1", len(list))
	}
	if n := slotsUsed(list[0]); n != 2 {
		t.Errorf("list slots_used = %d, want 2", n)
	}

	resp, body = h.doJSON(t, http.MethodGet, "/v1/projects/"+itoa(h.projectID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get project: %d %s", resp.StatusCode, body)
	}
	if n := slotsUsed(body); n != 2 {
		t.Errorf("get slots_used = %d, want 2", n)
	}

	resp, body = h.doJSON(t, http.MethodPatch, "/v1/projects/"+itoa(h.projectID),
		map[string]any{"name": "renamed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch project: %d %s", resp.StatusCode, body)
	}
	if n := slotsUsed(body); n != 2 {
		t.Errorf("patch slots_used = %d, want 2", n)
	}
}
