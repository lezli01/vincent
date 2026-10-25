package cli

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// `vincent issue ready`/`unready` and `vincent task deliver --ready` (task
// 137 decision 13) against the real API handlers.

// readinessIssue is an issue with one main task of its own, running; finish
// takes it to done with a delivery that is not ready.
func readinessIssue(t *testing.T, h *liveHarness) (issueID int64, task *store.Task, finish func()) {
	t.Helper()
	ctx := t.Context()
	iss, err := h.st.CreateIssue(ctx, store.NewIssue{ProjectID: h.projectID, Title: "explicit"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	task = &store.Task{
		ProjectID: h.projectID, Title: "run", WorkflowName: "w", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "vincent/run", State: store.TaskRunning,
		IssueID: &iss.ID, IssueWorktree: store.IssueWorktreeMain,
	}
	if err := h.st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	finish = func() {
		t.Helper()
		notReady := false
		d := &store.Delivery{Kind: store.DeliveryChange, Title: "x", Ready: &notReady, ReadyBy: store.ReadyByWorkflow, RecordedAt: time.Now().UTC()}
		if _, _, err := h.st.TransitionTask(ctx, task.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: d}); err != nil {
			t.Fatalf("finish: %v", err)
		}
	}
	return iss.ID, task, finish
}

// TestIssueReadyCommands: ready moves the issue to hand_off and says so;
// unready takes it back; each refusal is the daemon's message and a line
// saying what to do next.
func TestIssueReadyCommands(t *testing.T) {
	// A human at a terminal, whatever runs the test.
	t.Setenv(envTaskID, "")
	t.Setenv(apiclient.EnvChatID, "")
	h := newLiveHarness(t)
	issueID, _, finish := readinessIssue(t, h)
	id := strconv.FormatInt(issueID, 10)

	_, errOut, code := runCLI(t, "issue", "ready", id)
	if code != 1 || !strings.Contains(errOut, "running task to settle") {
		t.Errorf("ready while running: exit %d, %q", code, errOut)
	}
	finish()

	_, errOut, code = runCLI(t, "issue", "unready", id)
	if code != 1 || !strings.Contains(errOut, "in the in_progress lane") {
		t.Errorf("unready outside hand_off: exit %d, %q", code, errOut)
	}
	out, errOut, code := runCLI(t, "issue", "ready", id)
	if code != 0 || !strings.Contains(out, "issue "+id+" marked ready: explicit") {
		t.Errorf("ready: exit %d, %q %q", code, out, errOut)
	}
	if iss := issueJSON(t, "issue", "show", id); iss.Lane != string(issuestate.LaneHandOff) {
		t.Errorf("lane after ready = %s, want hand_off", iss.Lane)
	}
	if iss := issueJSON(t, "issue", "unready", id); iss.Lane != string(issuestate.LaneInProgress) {
		t.Errorf("unready --json lane = %s, want in_progress", iss.Lane)
	}

	empty := issueJSON(t, "issue", "add", "--project", h.project(), "--title", "nothing yet")
	_, errOut, code = runCLI(t, "issue", "ready", strconv.FormatInt(empty.ID, 10))
	if code != 1 || !strings.Contains(errOut, "no finished task") {
		t.Errorf("ready with no finished work: exit %d, %q", code, errOut)
	}
}

// TestTaskDeliverReady: --ready on a running task is the task's mark, and a
// later deliver without it clears it; on a done task it is a human's, and
// moves the issue to hand_off.
func TestTaskDeliverReady(t *testing.T) {
	t.Setenv(apiclient.EnvChatID, "")
	h := newLiveHarness(t)
	issueID, task, finish := readinessIssue(t, h)
	taskID := strconv.FormatInt(task.ID, 10)

	// From inside the step: the task comes from the environment.
	t.Setenv(envTaskID, taskID)
	if _, errOut, code := runCLI(t, "task", "deliver", "--title", "x", "--ready"); code != 0 {
		t.Fatalf("deliver --ready: exit %d (%s)", code, errOut)
	}
	d := h.storedDelivery(t, task.ID)
	if d.Ready == nil || !*d.Ready || d.ReadyBy != store.ReadyByTask {
		t.Errorf("running deliver --ready = %v by %q, want true by task", d.Ready, d.ReadyBy)
	}
	if _, errOut, code := runCLI(t, "task", "deliver", "--title", "x"); code != 0 {
		t.Fatalf("deliver: exit %d (%s)", code, errOut)
	}
	if d := h.storedDelivery(t, task.ID); d.Ready != nil {
		t.Errorf("a later deliver without --ready kept readiness %v", *d.Ready)
	}

	finish()
	t.Setenv(envTaskID, "")
	out, errOut, code := runCLI(t, "task", "deliver", "--task", taskID, "--title", "x", "--ready")
	if code != 0 || !strings.Contains(out, "ready     yes (by human)") {
		t.Errorf("done deliver --ready: exit %d, %q %q", code, out, errOut)
	}
	if iss := issueJSON(t, "issue", "show", strconv.FormatInt(issueID, 10)); iss.Lane != string(issuestate.LaneHandOff) {
		t.Errorf("lane after a human's deliver --ready = %s, want hand_off", iss.Lane)
	}
}

func (h *liveHarness) storedDelivery(t *testing.T, taskID int64) *store.Delivery {
	t.Helper()
	task, err := h.st.GetTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Delivery == nil {
		t.Fatalf("task %d has no delivery", taskID)
	}
	return task.Delivery
}
