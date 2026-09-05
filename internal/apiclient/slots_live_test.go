package apiclient_test

import (
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// TestSlotCountsRoundTrip wires the client to the real handlers and asserts
// the §11 slot figures survive the wire on both surfaces that carry them.
// It is the drift guard for issue #324's fix: the server DTOs are unexported,
// so nothing but a live round-trip catches `slots_used` renamed on one side —
// and a client that silently decoded zero would be back to undercounting.
func TestSlotCountsRoundTrip(t *testing.T) {
	h := newCreateHarness(t)
	ctx := t.Context()

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
		if err := h.store.CreateTask(ctx, task, nil); err != nil {
			t.Fatalf("CreateTask(%s): %v", title, err)
		}
		return task
	}
	// A parked fan-out parent, one lane running under it, and a root task
	// parked on a question: two slots held, neither of them a `running` root.
	parent := mk("parent", store.TaskAwaitingChildren, nil)
	mk("lane-running", store.TaskRunning, &parent.ID)
	mk("root-asking", store.TaskAwaitingInput, nil)

	info, err := h.client.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Slots.Used != 2 {
		t.Errorf("Slots.Used = %d, want 2 (a running lane and a task awaiting input)", info.Slots.Used)
	}
	if info.Slots.Lanes != 1 {
		t.Errorf("Slots.Lanes = %d, want 1", info.Slots.Lanes)
	}
	if info.Slots.AwaitingInput != 1 {
		t.Errorf("Slots.AwaitingInput = %d, want 1", info.Slots.AwaitingInput)
	}

	projects, err := h.client.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(projects))
	}
	if projects[0].SlotsUsed != 2 {
		t.Errorf("SlotsUsed = %d, want 2", projects[0].SlotsUsed)
	}
}
