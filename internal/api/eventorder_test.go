package api

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// TestEventsStreamDeliversEveryFanOutLane is issue #963 seen from a client
// (review F3): a fan-out round committed while a live `GET /v1/events`
// stream is open must reach that stream whole — every lane's task.created
// and every children_changed the daemon's hook appends for their ancestor.
//
// The hook here is the daemon's (internal/daemon/daemon.go): publish to the
// broker, then append children_changed for a task.created or
// task.state_changed. Before the store published in id order, that relay's
// event overtook the batch's later lanes, and the stream's `<= lastID`
// dedupe dropped them for good.
func TestEventsStreamDeliversEveryFanOutLane(t *testing.T) {
	h := newSSEHarness(t)
	ctx := context.Background()
	h.st.SetEventHook(func(e *store.Event) {
		h.broker.Publish(e)
		if e.TaskID == nil || (e.Type != store.EventTaskCreated && e.Type != store.EventTaskStateChanged) {
			return
		}
		if err := h.st.EmitChildrenChanged(ctx, *e.TaskID, store.TaskQueued); err != nil {
			t.Errorf("EmitChildrenChanged: %v", err)
		}
	})

	root := &store.Task{
		ProjectID: h.projectID, Title: "root", WorkflowName: "adhoc", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "root", State: store.TaskAwaitingChildren,
	}
	if err := h.st.CreateTask(ctx, root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	before, err := h.st.MaxEventID(ctx)
	if err != nil {
		t.Fatalf("MaxEventID: %v", err)
	}

	c := openSSE(t, h.ts.URL+"/v1/events", "")
	var lanes []*store.Task
	for i, id := range []string{"a", "b", "c"} {
		idx := 0
		lanes = append(lanes, &store.Task{
			ProjectID: h.projectID, Title: "lane " + id, WorkflowName: "adhoc", WorkflowSnapshot: "x",
			BaseBranch: "main", BranchName: "lane-" + id, State: store.TaskQueued,
			ParentTaskID: &root.ID, ParentStepIndex: &idx, LaneID: id, LaneOrder: i,
		})
	}
	if err := h.st.CreateTasks(ctx, lanes, nil); err != nil {
		t.Fatalf("CreateTasks: %v", err)
	}

	committed, err := h.st.ListEvents(ctx, store.EventFilter{AfterID: before})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var want, got []string
	created := 0
	for _, e := range committed {
		want = append(want, fmt.Sprint(e.ID))
		if e.Type == store.EventTaskCreated {
			created++
		}
	}
	if created != len(lanes) {
		t.Fatalf("committed %d task.created, want one per lane (%d)", created, len(lanes))
	}
	deadline := time.After(10 * time.Second)
	for len(got) < len(want) {
		select {
		case f, ok := <-c.frames:
			if !ok {
				t.Fatalf("stream ended after %v, want %v", got, want)
			}
			got = append(got, f.id)
		case <-deadline:
			t.Fatalf("live stream delivered %v of the committed %v: the rest were dropped by its id dedupe", got, want)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("live stream delivered %v, want every committed event in id order %v", got, want)
	}
	c.expectNone(t, 200*time.Millisecond)
}
