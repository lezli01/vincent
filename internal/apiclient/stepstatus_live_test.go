package apiclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// runningStep gives the harness task a `running` step run to speak for, and
// puts the task itself in `running` — which is the only shape the endpoint
// can be reached in.
func runningStep(t *testing.T, h *harness, taskID int64, stepID string) *store.StepRun {
	t.Helper()
	h.setState(t, taskID, store.TaskRunning)
	run := &store.StepRun{
		TaskID: taskID, StepIndex: 0, StepID: stepID, StepType: "agent",
		Attempt: 1, State: store.StepRunning,
	}
	if err := h.st.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	return run
}

// The wire round trip, against the real handlers: a step sets its status, and
// it comes back on the step-run DTO and on the list row a board reads. This
// is where client and server types would drift apart if either side grew the
// field alone.
func TestStepStatusOverTheWire(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	run := runningStep(t, h, h.taskID, "implement")

	stored, err := c.SetStepStatus(t.Context(), h.taskID, "implement", "3 tests red in internal/store")
	if err != nil {
		t.Fatalf("SetStepStatus: %v", err)
	}
	if stored != "3 tests red in internal/store" {
		t.Errorf("stored message = %q", stored)
	}

	detail, err := c.GetTask(t.Context(), h.taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].ID != run.ID {
		t.Fatalf("steps = %+v, want the one row", detail.Steps)
	}
	if got := detail.Steps[0].StatusMessage; got == nil || *got != "3 tests red in internal/store" {
		t.Errorf("step DTO status = %v, want the message", got)
	}

	tasks, err := c.ListTasks(t.Context(), apiclient.ListTasksOptions{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	var row *apiclient.Task
	for i := range tasks {
		if tasks[i].ID == h.taskID {
			row = &tasks[i]
		}
	}
	if row == nil {
		t.Fatalf("task %d missing from the list", h.taskID)
	}
	if row.StatusMessage == nil || *row.StatusMessage != "3 tests red in internal/store" {
		t.Errorf("list row status = %v, want the message denormalized onto it", row.StatusMessage)
	}

	// The bounds are the daemon's, not the caller's: a multi-line message
	// over the cap comes back flattened and truncated rather than refused.
	long := "first line\nsecond line " + strings.Repeat("x", 400)
	stored, err = c.SetStepStatus(t.Context(), h.taskID, "implement", long)
	if err != nil {
		t.Fatalf("SetStepStatus (long): %v", err)
	}
	if len(stored) != 256 || strings.ContainsAny(stored, "\n\r") {
		t.Errorf("stored long message = %d bytes %q, want 256 on one line", len(stored), stored)
	}
}

// A write against a step that is no longer running is refused with 409, not
// applied silently and not answered 404: the task is there, the step is past
// the point where it can speak.
func TestStepStatusRefusedWhenNotRunning(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	run := runningStep(t, h, h.taskID, "implement")
	run.State = store.StepSucceeded
	if err := h.st.UpdateStepRun(t.Context(), run); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}

	_, err := c.SetStepStatus(t.Context(), h.taskID, "implement", "too late")
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("SetStepStatus on a finished step = %v, want a 409", err)
	}

	if _, err = c.SetStepStatus(t.Context(), 9999, "implement", "nobody"); !errors.As(err, &apiErr) ||
		apiErr.Status != http.StatusNotFound {
		t.Errorf("SetStepStatus on an unknown task = %v, want a 404", err)
	}
}

// The whole reason the status is on §13.3's durable side: a client that
// blinks recovers it from the events table rather than losing it. A live
// output chunk could not do this.
func TestStepStatusReplaysWithLastEventID(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	runningStep(t, h, h.taskID, "implement")

	// The cursor a subscriber would have held before the status was set.
	before := h.append(t, "task.state_changed")
	if _, err := c.SetStepStatus(t.Context(), h.taskID, "implement", "packing the artifact"); err != nil {
		t.Fatalf("SetStepStatus: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notes := c.StreamTask(ctx, h.taskID, apiclient.StreamOptions{
		LastEventID:    before.ID,
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	})
	if _, ok := nextNote(t, notes).(apiclient.ConnectedNote); !ok {
		t.Fatal("first note was not ConnectedNote")
	}
	ev, ok := nextNote(t, notes).(apiclient.EventNote)
	if !ok {
		t.Fatalf("replay note = %T, want an EventNote", ev)
	}
	if ev.Event.Type != apiclient.EventTaskStatusChanged {
		t.Fatalf("replayed event type = %q, want %q", ev.Event.Type, apiclient.EventTaskStatusChanged)
	}
	var payload struct {
		TaskID  int64  `json:"task_id"`
		StepID  string `json:"step_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(ev.Event.Payload, &payload); err != nil {
		t.Fatalf("payload: %v (%s)", err, ev.Event.Payload)
	}
	if payload.TaskID != h.taskID || payload.StepID != "implement" ||
		payload.Message != "packing the artifact" {
		t.Errorf("replayed payload = %+v, want the task, step and message", payload)
	}
}

// The client's copy of the event name has to be the daemon's; nothing else
// pins two string literals in two packages together.
func TestStatusEventNameMatchesTheDaemon(t *testing.T) {
	if apiclient.EventTaskStatusChanged != store.EventTaskStatusChanged {
		t.Errorf("client event name %q != daemon's %q",
			apiclient.EventTaskStatusChanged, store.EventTaskStatusChanged)
	}
}
