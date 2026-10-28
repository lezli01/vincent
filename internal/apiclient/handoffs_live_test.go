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

// TestHandoffsOverTheWire pins the task 137.5 client against the real
// handlers: CreateHandoff with ids and with a selection, its replay under a
// key, ListHandoffs' filters, GetHandoff, and a refusal's reason.
func TestHandoffsOverTheWire(t *testing.T) {
	h := newCreateHarness(t)
	ctx := t.Context()
	handOff := func(title string, priority int) int64 {
		t.Helper()
		iss, err := h.store.CreateIssue(ctx, store.NewIssue{ProjectID: h.projectID, Title: title, Priority: priority}, issuestate.Human)
		if err != nil {
			t.Fatal(err)
		}
		task := &store.Task{
			ProjectID: h.projectID, Title: title, WorkflowName: "w", WorkflowSnapshot: "steps: []",
			BaseBranch: "main", BranchName: "b/" + title, State: store.TaskRunning,
			IssueID: &iss.ID, IssueWorktree: store.IssueWorktreeMain,
		}
		if err := h.store.CreateTask(ctx, task, nil); err != nil {
			t.Fatal(err)
		}
		d := &store.Delivery{Kind: store.DeliveryChange, Title: title, HeadSHA: "abc", RecordedAt: time.Now().UTC()}
		if _, _, err := h.store.TransitionTask(ctx, task.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: d}); err != nil {
			t.Fatal(err)
		}
		return iss.ID
	}
	low, urgent, other := handOff("low", 4), handOff("urgent", 1), handOff("other", 0)

	req := apiclient.CreateHandoffRequest{ProjectID: h.projectID, Workflow: "adhoc", IssueIDs: []int64{other}, Paused: true}
	first, err := h.client.CreateHandoff(ctx, req, "wire-1")
	if err != nil {
		t.Fatalf("CreateHandoff: %v", err)
	}
	if first.State != apiclient.HandoffLive || len(first.Items) != 1 ||
		first.Items[0].IssueID != other || first.Items[0].Delivery == nil || first.Task == nil || first.Task.ID != first.TaskID {
		t.Errorf("created = %+v", first)
	}
	again, err := h.client.CreateHandoff(ctx, req, "wire-1")
	if err != nil || again.ID != first.ID {
		t.Errorf("replay = %+v, %v, want handoff %d", again, err, first.ID)
	}

	sel, err := h.client.CreateHandoff(ctx, apiclient.CreateHandoffRequest{
		ProjectID: h.projectID, Workflow: "adhoc", Select: &apiclient.HandoffSelect{Delivery: []string{apiclient.DeliveryChange}, Max: 2},
	}, "")
	if err != nil {
		t.Fatalf("CreateHandoff select: %v", err)
	}
	if len(sel.Items) != 2 {
		t.Fatalf("selected %+v", sel.Items)
	}
	got := map[int64]bool{sel.Items[0].IssueID: true, sel.Items[1].IssueID: true}
	if !got[low] || !got[urgent] {
		t.Errorf("selected %+v, want low and urgent", sel.Items)
	}

	_, err = h.client.CreateHandoff(ctx, apiclient.CreateHandoffRequest{
		ProjectID: h.projectID, Workflow: "adhoc", Select: &apiclient.HandoffSelect{},
	}, "")
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Details["reason"] != "nothing_to_hand_off" {
		t.Errorf("empty selection = %v, want 409 nothing_to_hand_off", err)
	}

	rows, err := h.client.ListHandoffs(ctx, apiclient.HandoffListOptions{ProjectID: h.projectID, States: []string{apiclient.HandoffLive}, IssueID: urgent})
	if err != nil || len(rows) != 1 || rows[0].ID != sel.ID || len(rows[0].Items) != 0 {
		t.Errorf("ListHandoffs = %+v, %v", rows, err)
	}
	one, err := h.client.GetHandoff(ctx, first.ID)
	if err != nil || one.ID != first.ID || len(one.Items) != 1 {
		t.Errorf("GetHandoff = %+v, %v", one, err)
	}

	// The item verbs (task 137.6): one item, then every unresolved one.
	moved, err := h.client.ResolveHandoffItems(ctx, sel.ID, apiclient.HandoffWait, []int64{low},
		apiclient.HandoffItemRequest{Note: "upstream"})
	if err != nil {
		t.Fatalf("ResolveHandoffItems wait: %v", err)
	}
	for _, it := range moved.Items {
		if it.IssueID == low && (it.Outcome != "waiting" || it.Note != "upstream") {
			t.Errorf("waiting item = %+v", it)
		}
	}
	moved, err = h.client.ResolveHandoffItems(ctx, sel.ID, apiclient.HandoffDeliver, nil,
		apiclient.HandoffItemRequest{Close: apiclient.HandoffCloseNone})
	if err != nil {
		t.Fatalf("ResolveHandoffItems deliver --all: %v", err)
	}
	for _, it := range moved.Items {
		want := "delivered"
		if it.IssueID == low {
			want = "waiting"
		}
		if it.Outcome != want {
			t.Errorf("issue %d outcome = %s, want %s", it.IssueID, it.Outcome, want)
		}
	}
	_, err = h.client.ResolveHandoffItems(ctx, sel.ID, apiclient.HandoffRelease, []int64{urgent}, apiclient.HandoffItemRequest{})
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict ||
		apiErr.Details["reason"] != apiclient.HandoffReasonItemResolved {
		t.Errorf("release of a delivered item = %v, want 409 handoff_item_resolved", err)
	}
}

// The client's state names have to be the daemon's.
func TestHandoffStateNamesMatchTheDaemon(t *testing.T) {
	for _, k := range []struct{ client, daemon string }{
		{apiclient.HandoffLive, string(store.HandoffLive)},
		{apiclient.HandoffWaiting, string(store.HandoffWaitingState)},
		{apiclient.HandoffSettled, string(store.HandoffSettled)},
	} {
		if k.client != k.daemon {
			t.Errorf("client state %q != daemon's %q", k.client, k.daemon)
		}
	}
}
