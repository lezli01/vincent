package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// decodeDelete reads a permanent-delete response body.
func decodeDelete(t *testing.T, body []byte) deleteResponse {
	t.Helper()
	var out deleteResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode delete: %v (%s)", err, body)
	}
	return out
}

func TestDeleteTaskRemovesAnArchivedTask(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskDone)
	setState(t, h, task.ID, store.TaskArchived)

	resp, body := h.doJSON(t, http.MethodDelete, "/v1/tasks/"+itoa(task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: %d %s", resp.StatusCode, body)
	}
	if got := decodeDelete(t, body); !got.Deleted {
		t.Fatalf("the response does not report the delete: %s", body)
	}
	resp, _ = h.doJSON(t, http.MethodGet, "/v1/tasks/"+itoa(task.ID), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete: %d, want 404", resp.StatusCode)
	}
}

func TestDeleteTaskRefusesWithTheSnakeCaseEnvelope(t *testing.T) {
	h := newActionHarness(t)
	// Every refusal is a 409 whose details name what is holding on. The
	// not_archived one is the gate on every other; the two structural ones are
	// asserted in internal/store, where the rows can be built directly.
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskDone)

	resp, body := h.doJSON(t, http.MethodDelete, "/v1/tasks/"+itoa(task.ID), nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("DELETE on a done task: %d %s, want 409", resp.StatusCode, body)
	}
	e := decodeError(t, body)
	if e.Code != CodeInvalidState {
		t.Fatalf("code %q, want %q", e.Code, CodeInvalidState)
	}
	if e.Details["reason"] != store.DeleteRefusedNotArchived {
		t.Fatalf("details %v, want reason %q", e.Details, store.DeleteRefusedNotArchived)
	}
	if e.Details["action"] != "delete" {
		t.Fatalf("details %v, want action delete", e.Details)
	}
	resp, _ = h.doJSON(t, http.MethodGet, "/v1/tasks/"+itoa(task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal("the refused task was deleted anyway")
	}
}

func TestDeleteTaskIs404ForAnUnknownID(t *testing.T) {
	h := newActionHarness(t)
	resp, body := h.doJSON(t, http.MethodDelete, "/v1/tasks/999999", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE unknown: %d %s, want 404", resp.StatusCode, body)
	}
}

func TestDeleteTaskLeavesABranchItNeverCutAlone(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskDone)
	setState(t, h, task.ID, store.TaskArchived)
	// The task never ran a step, so no worktree was ever made and no branch
	// was ever cut for it — the empty step ledger is what says so after
	// archive has cleared `worktree_path`. The branch step must not run: a
	// task that blocked with `branch_exists` carries somebody else's branch
	// name. The response omits `branch` entirely, which is the shape a client
	// that predates the field sees.
	resp, body := h.doJSON(t, http.MethodDelete, "/v1/tasks/"+itoa(task.ID)+"?delete_branch=true", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: %d %s", resp.StatusCode, body)
	}
	if got := decodeDelete(t, body); got.Branch != nil {
		t.Fatalf("a task with no branch reported one: %+v", got.Branch)
	}
}

func TestDeleteTaskRejectsAnUnparseableDeleteBranch(t *testing.T) {
	h := newActionHarness(t)
	resp, body := h.doJSON(t, http.MethodDelete, "/v1/tasks/1?delete_branch=maybe", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete_branch=maybe: %d %s, want 400", resp.StatusCode, body)
	}
}

func TestTaskListRejectsAnUnparseableArchivedBound(t *testing.T) {
	h := newActionHarness(t)
	for _, q := range []string{"archived_before=nonsense", "archived_since=nonsense"} {
		resp, body := h.doJSON(t, http.MethodGet, "/v1/tasks?"+q, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d %s, want 400", q, resp.StatusCode, body)
		}
		if e := decodeError(t, body); e.Code != CodeValidationFailed {
			t.Fatalf("%s: code %q, want %q", q, e.Code, CodeValidationFailed)
		}
	}
}

func TestDeleteTaskAnnouncesItselfAsADurableEvent(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskDone)
	setState(t, h, task.ID, store.TaskArchived)

	resp, body := h.doJSON(t, http.MethodDelete, "/v1/tasks/"+itoa(task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: %d %s", resp.StatusCode, body)
	}
	events, err := h.store.ListEvents(t.Context(), store.EventFilter{Types: []string{store.EventTaskDeleted}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d task.deleted events, want 1", len(events))
	}
	var payload struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.ID != task.ID {
		t.Fatalf("the event names task %d, want %d", payload.ID, task.ID)
	}
}
