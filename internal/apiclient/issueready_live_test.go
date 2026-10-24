package apiclient_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// finishedNotReady is an issue whose one main task finished done with a
// delivery that is not ready (task 137 decision 13), so it waits in
// in_progress.
func (h *harness) finishedNotReady(t *testing.T) (*store.Issue, *store.Task) {
	t.Helper()
	ctx := t.Context()
	iss, err := h.st.CreateIssue(ctx, store.NewIssue{ProjectID: h.projectID, Title: "explicit"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	task := &store.Task{
		ProjectID: h.projectID, Title: "run", WorkflowName: "w", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "vincent/run", State: store.TaskRunning,
		IssueID: &iss.ID, IssueWorktree: store.IssueWorktreeMain,
	}
	if err := h.st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	notReady := false
	d := &store.Delivery{Kind: store.DeliveryChange, Title: "x", Ready: &notReady, ReadyBy: store.ReadyByWorkflow, RecordedAt: time.Now().UTC()}
	done, _, err := h.st.TransitionTask(ctx, task.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: d})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	return iss, done
}

// TestIssueReadinessOverTheWire: MarkIssueReady and MarkIssueUnready move
// the lane and decode the issue; an unready outside hand_off is the 409
// IssueConflict names; a delivery's readiness round-trips.
func TestIssueReadinessOverTheWire(t *testing.T) {
	// A human's client: outside any step or chat, whatever runs the test.
	t.Setenv("VINCENT_TASK_ID", "")
	t.Setenv(apiclient.EnvChatID, "")
	h := newHarness(t)
	c := h.client()
	iss, task := h.finishedNotReady(t)

	got, err := c.MarkIssueReady(t.Context(), iss.ID)
	if err != nil {
		t.Fatalf("MarkIssueReady: %v", err)
	}
	if got.ID != iss.ID || got.Lane != string(issuestate.LaneHandOff) {
		t.Errorf("MarkIssueReady = %+v, want hand_off", got)
	}
	d, err := c.GetTaskDelivery(t.Context(), task.ID)
	if err != nil || d == nil || d.Ready == nil || !*d.Ready || d.ReadyBy != string(store.ReadyByHuman) {
		t.Fatalf("delivery after ready = %+v, %v", d, err)
	}
	if got, err = c.MarkIssueUnready(t.Context(), iss.ID); err != nil || got.Lane != string(issuestate.LaneInProgress) {
		t.Fatalf("MarkIssueUnready = %+v, %v", got, err)
	}
	_, err = c.MarkIssueUnready(t.Context(), iss.ID)
	if reason, _, ok := apiclient.IssueConflict(err); !ok || reason != apiclient.IssueReasonNotHandOff {
		t.Errorf("unready twice = %v, want 409 %s", err, apiclient.IssueReasonNotHandOff)
	}
	var apiErr *apiclient.Error
	if _, err := c.MarkIssueReady(t.Context(), 9999); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("ready of an unknown issue = %v, want 404", err)
	}

	// A human's deliver --ready on the done task marks it again.
	d, err = c.SetTaskDelivery(t.Context(), task.ID, apiclient.DeliveryInput{Title: "x", Ready: true})
	if err != nil || d.Ready == nil || !*d.Ready || d.ReadyBy != string(store.ReadyByHuman) {
		t.Fatalf("SetTaskDelivery ready = %+v, %v", d, err)
	}
}
