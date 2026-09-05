package apiclient_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestActionAgainstRealHandlers: an action returns the daemon's own view of
// the task afterwards, which is what a caller renders instead of predicting
// the transition itself.
func TestActionAgainstRealHandlers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	got, err := h.client().Cancel(ctx, h.taskID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got.State != string(store.TaskAborted) {
		t.Errorf("state = %q, want aborted", got.State)
	}
	if len(got.AvailableActions) == 0 {
		t.Error("available_actions is empty; an aborted task can still be archived")
	}
}

// TestActionConflictCarriesState: a 409 arrives as *Error with §13.1's
// details intact, so a stale action bar can say what it found instead of
// re-printing the daemon's prose.
func TestActionConflictCarriesState(t *testing.T) {
	h := newHarness(t)

	_, err := h.client().Approve(context.Background(), h.taskID)
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("Approve on a queued task: err = %v, want *apiclient.Error", err)
	}
	if apiErr.Status != http.StatusConflict {
		t.Errorf("status = %d, want 409", apiErr.Status)
	}
	if got := apiErr.Details["state"]; got != string(store.TaskQueued) {
		t.Errorf("details.state = %q, want queued", got)
	}
}

// TestRetryOverrideReachesTheWire: edit+retry rewrites the snapshot, so the
// text comes back on the next fetch — the round trip the editor path rides.
func TestRetryOverrideReachesTheWire(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.snapshotTask(t)
	h.setState(t, id, store.TaskBlocked)

	const edited = "do it differently"
	if _, _, err := h.client().Retry(ctx, id, apiclient.Override{Prompt: edited}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	task, err := h.client().GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	step, ok := task.Step(0)
	if !ok {
		t.Fatal("workflow_steps has no step 0")
	}
	text, field, editable := step.EditableText()
	if !editable || field != "prompt" {
		t.Fatalf("EditableText field = %q editable = %v, want prompt/true", field, editable)
	}
	if text != edited {
		t.Errorf("prompt = %q, want %q", text, edited)
	}
}

// TestWorkflowStepsDecode covers the field mapping and the per-type editable
// text, including the manual step that has none.
func TestWorkflowStepsDecode(t *testing.T) {
	h := newHarness(t)
	id := h.snapshotTask(t)

	task, err := h.client().GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(task.WorkflowSteps) != 3 {
		t.Fatalf("workflow_steps = %d, want 3", len(task.WorkflowSteps))
	}
	for _, tc := range []struct {
		index int
		field string
		text  string
		ok    bool
	}{
		{0, "prompt", "write the thing", true},
		{1, "", "", false},
		{2, "run", "git push", true},
	} {
		step, found := task.Step(tc.index)
		if !found {
			t.Fatalf("step %d missing", tc.index)
		}
		text, field, ok := step.EditableText()
		if ok != tc.ok || field != tc.field || text != tc.text {
			t.Errorf("step %d: (%q, %q, %v), want (%q, %q, %v)",
				tc.index, text, field, ok, tc.text, tc.field, tc.ok)
		}
	}
	if got, _ := task.Step(1); got.Instructions != "look at the diff" {
		t.Errorf("manual instructions = %q, want the gate text", got.Instructions)
	}
}

// TestDiffReadsWorktree drives the text/plain endpoint against a real repo.
func TestDiffReadsWorktree(t *testing.T) {
	h := newHarness(t)
	repo := testrepo.Init(t, "main")
	testrepo.WriteFile(t, repo, "README.md", "changed by the agent\n")
	id := h.taskInWorktree(t, repo)

	diff, err := h.client().Diff(context.Background(), id)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "changed by the agent") {
		t.Errorf("diff does not carry the change:\n%s", diff)
	}
	if !strings.HasPrefix(diff, "diff --git") {
		t.Errorf("diff is not a unified diff:\n%s", diff)
	}
}

// TestDiffWithoutWorktree: the 409 stays an *Error rather than flattening to
// an empty diff — "not started yet" and "worktree removed" are different
// answers to "show me the changes".
func TestDiffWithoutWorktree(t *testing.T) {
	h := newHarness(t)

	_, err := h.client().Diff(context.Background(), h.taskID)
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("Diff on a task with no worktree: err = %v, want *apiclient.Error", err)
	}
	if apiErr.Status != http.StatusConflict {
		t.Errorf("status = %d, want 409", apiErr.Status)
	}
}

// TestRepairAgainstRealHandlers drives POST /v1/tasks/{id}/repair through the
// real handler: a blocked task re-queues, and the daemon's own view of it
// comes back (§6, task 025).
func TestRepairAgainstRealHandlers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.block(t, "check_failed")

	got, warnings, err := h.client().Repair(ctx, h.taskID,
		apiclient.RepairInput{Prompt: "fix the failing check"})
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if got.State != string(store.TaskQueued) {
		t.Errorf("state = %q, want queued", got.State)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none for a repair with no selection", warnings)
	}
}

// TestRepairRejectsAnEmptyPrompt: the daemon answers 400 and the client hands
// the §13.1 envelope back intact.
func TestRepairRejectsAnEmptyPrompt(t *testing.T) {
	h := newHarness(t)
	h.block(t, "check_failed")

	_, _, err := h.client().Repair(context.Background(), h.taskID, apiclient.RepairInput{})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("Repair with no prompt = %v, want a 400", err)
	}
}

// TestRepairOutsideBlockedCarriesState: a repair is valid from `blocked`
// alone, and the 409 says what it found.
func TestRepairOutsideBlockedCarriesState(t *testing.T) {
	h := newHarness(t)

	_, _, err := h.client().Repair(context.Background(), h.taskID,
		apiclient.RepairInput{Prompt: "fix it"})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("Repair from queued = %v, want a 409", err)
	}
	if apiErr.Details["state"] != string(store.TaskQueued) {
		t.Errorf("details.state = %q, want queued", apiErr.Details["state"])
	}
}

// TestRepairStepIDMatchesTheEngine pins the client's copy of the reserved
// step id (§5.4) to the engine's. The client owns its wire types, the same
// way it owns the action names above — and a copy that drifted would make
// every repair row render as an attempt of the step it sits beside.
func TestRepairStepIDMatchesTheEngine(t *testing.T) {
	if apiclient.RepairStepID != taskrun.RepairStepID {
		t.Errorf("apiclient.RepairStepID = %q, engine says %q",
			apiclient.RepairStepID, taskrun.RepairStepID)
	}
}

// TestRetryFromParkedParentCarriesTheCount: `retry` on a fan_out parent
// parked in `awaiting_children` is the cascade (task 088), and the client
// reads how many blocked lanes it re-admitted out of the same flat object the
// task comes from. Without it the only thing a human would see is a parent in
// the state it was already in.
func TestRetryFromParkedParentCarriesTheCount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	parent := h.parkedParent(t)
	h.lane(t, parent, store.TaskBlocked)
	h.lane(t, parent, store.TaskBlocked)

	got, retried, err := h.client().Retry(ctx, parent, apiclient.Override{})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if retried != 2 {
		t.Errorf("retried_descendants = %d, want 2", retried)
	}
	if got.State != string(store.TaskAwaitingChildren) {
		t.Errorf("state = %q, want awaiting_children — the join is still open", got.State)
	}
}

// TestRetryFromBlockedReportsNoCascade: the count is always on the wire, and
// on the ordinary path it is 0 — a client that read a missing field as "some"
// would announce lanes nobody re-admitted.
func TestRetryFromBlockedReportsNoCascade(t *testing.T) {
	h := newHarness(t)
	id := h.snapshotTask(t)
	h.setState(t, id, store.TaskRunning)
	h.setState(t, id, store.TaskBlocked)

	got, retried, err := h.client().Retry(context.Background(), id, apiclient.Override{})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if retried != 0 {
		t.Errorf("retried_descendants = %d on an ordinary retry, want 0", retried)
	}
	if got.State != string(store.TaskQueued) {
		t.Errorf("state = %q, want queued", got.State)
	}
}

// TestRetryOverrideFromParkedParentIsRefused: a parked parent's cursor is a
// `fan_out` step, which carries no text — so edit+retry is a 400 there, which
// is why the `E` key checks stepEditable before offering itself.
func TestRetryOverrideFromParkedParentIsRefused(t *testing.T) {
	h := newHarness(t)
	parent := h.parkedParent(t)

	_, _, err := h.client().Retry(context.Background(), parent,
		apiclient.Override{Prompt: "try harder"})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("edit+retry from awaiting_children = %v, want a 400", err)
	}
}

// parkedParent is a task parked on a fan_out join, which is what a `retry`
// cascades from.
func (h *harness) parkedParent(t *testing.T) int64 {
	t.Helper()
	id := h.snapshotTask(t)
	h.setState(t, id, store.TaskRunning)
	h.setState(t, id, store.TaskAwaitingChildren)
	return id
}

// lane is one descendant of a parked parent, in whatever state the fan_out
// left it — what the cascade walks.
func (h *harness) lane(t *testing.T, parent int64, state store.TaskState) {
	t.Helper()
	index := 0
	child := &store.Task{
		ProjectID: h.projectID, Title: "lane", WorkflowName: "three",
		WorkflowSnapshot: snapshotWorkflow, BaseBranch: "main",
		State: state, ParentTaskID: &parent, ParentStepIndex: &index,
	}
	if err := h.st.CreateTask(t.Context(), child,
		func(id int64) (string, error) { return fmt.Sprintf("vincent/%d-lane", id), nil }); err != nil {
		t.Fatalf("create lane: %v", err)
	}
}
