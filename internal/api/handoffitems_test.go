package api

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// Task 137.6: POST /v1/handoffs/{id}/items/{issue_id}/{verb}.

func startHandoff(t *testing.T, h *taskHarness, ids ...int64) handoffBody {
	t.Helper()
	return wantStatus(t, http.StatusCreated)(h.postMarked(t, "/v1/handoffs", map[string]any{
		"project_id": h.projectID, "workflow": handoffWorkflowName, "issue_ids": ids, "paused": true,
	}))
}

func itemVerbPath(handoffID int64, sel, verb string) string {
	return fmt.Sprintf("/v1/handoffs/%d/items/%s/%s", handoffID, sel, verb)
}

func outcomeOf(t *testing.T, b handoffBody, issueID int64) handoffItemBody {
	t.Helper()
	for _, it := range b.Items {
		if it.IssueID == issueID {
			return it
		}
	}
	t.Fatalf("no item for issue %d in %+v", issueID, b.Items)
	return handoffItemBody{}
}

// TestHandoffItemVerbs: deliver, release and return over the route, with
// outcome_by read from the caller, and the refusals.
func TestHandoffItemVerbs(t *testing.T) {
	h := newActionHarness(t)
	ctx := t.Context()
	a := handOffIssue(t, h, store.NewIssue{Title: "a"})
	b := handOffIssue(t, h, store.NewIssue{Title: "b"})
	c := handOffIssue(t, h, store.NewIssue{Title: "c"})
	d := handOffIssue(t, h, store.NewIssue{Title: "d"})
	ho := startHandoff(t, h, a.ID, b.ID, c.ID, d.ID)

	got := wantStatus(t, http.StatusOK)(h.postMarked(t, itemVerbPath(ho.ID, strconv.FormatInt(a.ID, 10), "deliver"),
		map[string]any{"close": "none", "note": "in main"}))
	if it := outcomeOf(t, got, a.ID); it.Outcome != "delivered" || it.OutcomeBy != "human" || it.Note != "in main" {
		t.Errorf("delivered item = %+v", it)
	}
	if iss, _ := h.store.GetIssue(ctx, a.ID); iss.State == issuestate.Closed {
		t.Error("close none closed the issue")
	}

	// The handoff's own task, by its marker.
	got = wantStatus(t, http.StatusOK)(h.postMarked(t, itemVerbPath(ho.ID, strconv.FormatInt(b.ID, 10), "release"),
		nil, headerTaskMarker, strconv.FormatInt(ho.TaskID, 10)))
	if it := outcomeOf(t, got, b.ID); it.Outcome != "released" || it.OutcomeBy != "handoff" {
		t.Errorf("released item = %+v", it)
	}

	// Return with a follow-up lands on the issue's latest main task.
	main, err := h.store.LatestIssueMainTask(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	got = wantStatus(t, http.StatusOK)(h.postMarked(t, itemVerbPath(ho.ID, strconv.FormatInt(c.ID, 10), "return"),
		map[string]any{"follow_up": "fix the tests", "paused": true}, headerTaskMarker, "4242"))
	if it := outcomeOf(t, got, c.ID); it.Outcome != "returned" || it.OutcomeBy != "agent" {
		t.Errorf("returned item = %+v", it)
	}
	if task, _ := h.store.GetTask(ctx, main.ID); task.State != store.TaskPaused || task.PendingFollowUp == nil ||
		task.PendingFollowUp.Prompt != "fix the tests" {
		t.Errorf("main task after return = %s %+v", task.State, task.PendingFollowUp)
	}

	// Refusals.
	resp, body := h.postMarked(t, itemVerbPath(ho.ID, strconv.FormatInt(a.ID, 10), "release"), nil)
	if det := wantReason(t, "resolved", resp, body, reasonHandoffItemResolved); det["outcome"] != "delivered" {
		t.Errorf("resolved details = %v", det)
	}
	resp, body = h.postMarked(t, itemVerbPath(ho.ID, strconv.FormatInt(d.ID, 10), "release"), map[string]any{"close": "completed"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("close on release: %d %s", resp.StatusCode, body)
	}
	resp, body = h.postMarked(t, itemVerbPath(ho.ID, strconv.FormatInt(d.ID, 10), "return"), map[string]any{"side": true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("side without follow_up: %d %s", resp.StatusCode, body)
	}
	resp, body = h.postMarked(t, itemVerbPath(ho.ID, "999999", "wait"), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("not an item: %d %s", resp.StatusCode, body)
	}
	resp, body = h.postMarked(t, itemVerbPath(999999, strconv.FormatInt(d.ID, 10), "wait"), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown handoff: %d %s", resp.StatusCode, body)
	}

	// --all moves only what is unresolved: d.
	got = wantStatus(t, http.StatusOK)(h.postMarked(t, itemVerbPath(ho.ID, "all", "wait"), nil))
	if it := outcomeOf(t, got, d.ID); it.Outcome != "waiting" {
		t.Errorf("--all left d %s", it.Outcome)
	}
	if it := outcomeOf(t, got, a.ID); it.Outcome != "delivered" {
		t.Errorf("--all moved a to %s", it.Outcome)
	}
}

// TestHandoffReturnSide: a side return creates a merge_back task on the
// issue running the latest main task's workflow.
func TestHandoffReturnSide(t *testing.T) {
	h := newActionHarness(t)
	ctx := t.Context()
	a := handOffIssue(t, h, store.NewIssue{Title: "a"})
	// The side task is cut from the issue's main branch, which the main
	// task's admission would have created.
	main, err := h.store.LatestIssueMainTask(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, h.repo, "branch", "-q", main.BranchName)
	ho := startHandoff(t, h, a.ID)
	wantStatus(t, http.StatusOK)(h.postMarked(t, itemVerbPath(ho.ID, strconv.FormatInt(a.ID, 10), "return"),
		map[string]any{"follow_up": "try another way", "side": true, "paused": true}))
	tasks, err := h.store.ListTasks(ctx, store.TaskFilter{ProjectID: h.projectID})
	if err != nil {
		t.Fatal(err)
	}
	var side *store.Task
	for i := range tasks {
		if task := &tasks[i]; task.IssueID != nil && *task.IssueID == a.ID && task.IssueWorktree == store.IssueWorktreeSide {
			side = task
		}
	}
	if side == nil {
		t.Fatal("no side task was created")
	}
	if side.Description != "try another way" || side.MergeOnConflict != store.MergeOnConflictBlock ||
		side.WorkflowName != "adhoc" || side.State != store.TaskPaused {
		t.Errorf("side task = %+v", side)
	}
}

// TestHandoffDeliverImportedByAgent: a step's deliver that would close an
// imported issue is refused forge_write_needs_human and records nothing; a
// human's is accepted and queues the write-back.
func TestHandoffDeliverImportedByAgent(t *testing.T) {
	h := newActionHarness(t)
	ctx := t.Context()
	iss, _, err := h.store.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: h.projectID, Provider: "github", RemoteKey: "gh-1", Repo: "o/r", Number: 1,
		Title: "imported", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatal(err)
	}
	task := h.createTask(t, map[string]any{"project_id": h.projectID, "issue_id": iss.ID})
	setState(t, h, task.ID, store.TaskRunning)
	dl := &store.Delivery{Kind: store.DeliveryChange, Title: "x", HeadSHA: "abc", RecordedAt: time.Now().UTC()}
	if _, _, err := h.store.TransitionTask(ctx, task.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: dl}); err != nil {
		t.Fatal(err)
	}
	ho := startHandoff(t, h, iss.ID)
	path := itemVerbPath(ho.ID, strconv.FormatInt(iss.ID, 10), "deliver")
	resp, body := h.postMarked(t, path, nil, headerTaskMarker, "4242")
	wantReason(t, "agent deliver", resp, body, issueReasonForgeWrite)
	one := wantStatus(t, http.StatusOK)(h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/handoffs/%d", ho.ID), nil))
	if it := outcomeOf(t, one, iss.ID); it.Outcome != "" {
		t.Errorf("a refused deliver recorded %s", it.Outcome)
	}
	// The same step may deliver with close none: nothing is written upstream.
	got := wantStatus(t, http.StatusOK)(h.postMarked(t, path, map[string]any{"close": "none"}, headerTaskMarker, "4242"))
	if it := outcomeOf(t, got, iss.ID); it.Outcome != "delivered" || it.OutcomeBy != "agent" {
		t.Errorf("item = %+v", it)
	}
}

// TestHandoffDeliverImportedUnderAGrant (review F1): the handoff's own task,
// granted issue.close and issue.comment, delivers an imported item with a
// comment and a close; both are made as `handoff` and enqueued for GitHub,
// as `vincent issue close` and `issue comment` would make them. An agent
// that is not the handoff's task stays refused.
func TestHandoffDeliverImportedUnderAGrant(t *testing.T) {
	h := newActionHarness(t)
	ctx := t.Context()
	iss, _, err := h.store.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: h.projectID, Provider: "github", RemoteKey: "gh-1", Repo: "o/r", Number: 1,
		Title: "imported", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatal(err)
	}
	task := h.createTask(t, map[string]any{"project_id": h.projectID, "issue_id": iss.ID})
	setState(t, h, task.ID, store.TaskRunning)
	dl := &store.Delivery{Kind: store.DeliveryChange, Title: "x", HeadSHA: "abc", RecordedAt: time.Now().UTC()}
	if _, _, err := h.store.TransitionTask(ctx, task.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: dl}); err != nil {
		t.Fatal(err)
	}
	ho := wantStatus(t, http.StatusCreated)(h.postMarked(t, "/v1/handoffs", map[string]any{
		"project_id": h.projectID, "workflow": handoffWorkflowName, "issue_ids": []int64{iss.ID}, "paused": true,
		"grant": []string{"issue.close", "issue.comment"},
	}))
	path := itemVerbPath(ho.ID, strconv.FormatInt(iss.ID, 10), "deliver")
	body := map[string]any{"comment": "shipped"}

	resp, raw := h.postMarked(t, path, body, headerTaskMarker, strconv.FormatInt(task.ID, 10))
	wantReason(t, "another task's deliver", resp, raw, issueReasonForgeWrite)

	got := wantStatus(t, http.StatusOK)(h.postMarked(t, path, body, headerTaskMarker, strconv.FormatInt(ho.TaskID, 10)))
	if it := outcomeOf(t, got, iss.ID); it.Outcome != "delivered" || it.OutcomeBy != "handoff" {
		t.Errorf("item = %+v", it)
	}
	closed, err := h.store.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issuestate.Normalize(closed.State) != issuestate.Closed {
		t.Errorf("issue state = %s, want closed", closed.State)
	}
	writes, err := h.store.IssueWrites(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]issuestate.Actor{}
	for _, w := range writes {
		ops[w.Op] = w.Origin
	}
	if len(writes) != 2 || ops["set_state"] != issuestate.Handoff || ops["add_comment"] != issuestate.Handoff {
		t.Errorf("outbox = %+v, want set_state and add_comment by handoff", ops)
	}
}
