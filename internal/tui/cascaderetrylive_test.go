package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// fanOutWorkflow parks its parent on a join. The lane's own work is one
// command, because what this file is about is the parent — a `fan_out` step
// is structure and carries no text, which is the whole reason edit+retry has
// nothing to offer on it.
const fanOutWorkflow = `name: fanned
steps:
  - id: build
    type: fan_out
    lanes:
      - id: api
        steps:
          - {id: api_impl, type: command, run: git --version}
`

// laneWorkflow is what a re-admitted lane runs: one command that succeeds
// wherever the suite runs.
const laneWorkflow = `name: lane
steps:
  - id: work
    type: command
    run: git --version
`

// parkedParent creates a task sitting in `awaiting_children` on a fan_out
// step, without running one — the scheduler only ever admits `queued`, so a
// task created in this state is left alone.
func (h *actionLiveHarness) parkedParent(t *testing.T, title string) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: h.projectID, Title: title, WorkflowName: "fanned",
		WorkflowSnapshot: fanOutWorkflow,
		BaseBranch:       "main", BranchName: "vincent/live-" + title,
		State: store.TaskAwaitingChildren,
	}
	if err := h.st.CreateTask(context.Background(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// blockedLane is one descendant holding the join open.
func (h *actionLiveHarness) blockedLane(t *testing.T, parent *store.Task, n int) *store.Task {
	t.Helper()
	index := 0
	reason := "nonzero_exit"
	task := &store.Task{
		ProjectID: h.projectID, Title: fmt.Sprintf("lane-%d", n), WorkflowName: "lane",
		WorkflowSnapshot: laneWorkflow,
		BaseBranch:       "main", BranchName: fmt.Sprintf("vincent/live-lane-%d", n),
		State:           store.TaskBlocked,
		BlockReason:     reason,
		ParentTaskID:    &parent.ID,
		ParentStepIndex: &index,
	}
	if err := h.st.CreateTask(context.Background(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// TestDetailRetriesParkedParentLive is the client half of task 088: the bar
// offers `r` on a parent parked in `awaiting_children`, the one call
// re-admits every blocked lane holding the join open, and the status line
// says how many — because the parent's own row comes back in the state it
// went in, and would otherwise report an action that looks like it did
// nothing.
//
// `E` is *not* offered there. Its cursor is the `fan_out` step, which carries
// no text, and the daemon answers an override from this state with a 400 —
// so the key checks the same thing its hint always did.
func TestDetailRetriesParkedParentLive(t *testing.T) {
	h := newActionLiveHarness(t)
	parent := h.parkedParent(t, "fanned-out")
	first := h.blockedLane(t, parent, 1)
	second := h.blockedLane(t, parent, 2)

	_, cmd := h.m.Update(selectTaskMsg{id: parent.ID})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the detail view to open the parked parent", func() bool {
		return detailOf(h.m).taskID == parent.ID && detailOf(h.m).loaded
	})

	d := detailOf(h.m)
	if !d.target().has(apiclient.ActionRetry) {
		t.Fatalf("available actions = %v, want retry on a parked parent", d.target().actions)
	}
	if d.stepEditable() {
		t.Error("a fan_out step reports editable text")
	}
	if hintsContain(d.detailHints(), "edit+retry") {
		t.Error("the bar advertises edit+retry on a parked parent")
	}

	// The key itself, not only the hint: pressing it must not reach the
	// editor, and must not leave the "a gate has no prompt" complaint behind.
	h.press(t, "E")
	if status := detailOf(h.m).actions.status; strings.Contains(status, "edit+retry") {
		t.Errorf("E acted on a parked parent: status = %q", status)
	}

	h.press(t, "r")
	h.p.until(30*time.Second, "both lanes to be re-admitted", func() bool {
		return h.state(t, first.ID) != store.TaskBlocked &&
			h.state(t, second.ID) != store.TaskBlocked
	})
	// The parent is where it was — the join is still open — so the count is
	// the only account of what the key did.
	h.p.until(30*time.Second, "the bar to report the cascade", func() bool {
		return strings.Contains(detailOf(h.m).actions.status, "2 lanes re-admitted")
	})
	if got := h.state(t, parent.ID); got != store.TaskAwaitingChildren &&
		got != store.TaskQueued && got != store.TaskRunning && got != store.TaskDone {
		t.Errorf("parent is %s after the cascade, want it parked or converged", got)
	}
}
