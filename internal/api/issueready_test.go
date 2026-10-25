package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// Task 137 decision 13: a person's — or an agent's — say on whether an
// issue's finished work is ready to hand off, over POST /v1/issues/{id}/ready
// and /unready and the `ready` key of a delivery.

// issueTask creates a main-line root task of issueID in state.
func (h *issueHarness) issueTask(t *testing.T, issueID int64, title string, state store.TaskState) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: h.pid, Title: title, WorkflowName: "w", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/" + title, State: state,
		IssueID: &issueID, IssueWorktree: store.IssueWorktreeMain,
	}
	if err := h.st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// finishedIssue is an issue whose one main task finished done with a
// delivery of the given readiness: nil is a legacy delivery, which reads
// ready.
func (h *issueHarness) finishedIssue(t *testing.T, title string, ready *bool) (*store.Issue, *store.Task) {
	t.Helper()
	iss, err := h.st.CreateIssue(t.Context(), store.NewIssue{ProjectID: h.pid, Title: title}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	task := h.issueTask(t, iss.ID, title, store.TaskRunning)
	d := &store.Delivery{Kind: store.DeliveryChange, Title: title, Ready: ready, RecordedAt: time.Now().UTC()}
	if ready != nil {
		d.ReadyBy = store.ReadyByWorkflow
	}
	done, _, err := h.st.TransitionTask(t.Context(), task.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: d})
	if err != nil {
		t.Fatalf("finish %s: %v", title, err)
	}
	return iss, done
}

// laneMoves is the issue.lane_changed payloads recorded after mark.
func (h *issueHarness) laneMoves(t *testing.T, mark int64) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range h.issueEvents(t, mark) {
		if e.Type != store.EventIssueLaneChanged {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("decode lane_changed: %v", err)
		}
		out = append(out, p)
	}
	return out
}

func wantLaneMove(t *testing.T, moves []map[string]any, from, to issuestate.Lane, by issuestate.Actor) {
	t.Helper()
	if len(moves) != 1 || moves[0]["from"] != string(from) || moves[0]["to"] != string(to) || moves[0]["by"] != string(by) {
		t.Errorf("lane moves = %v, want one %s → %s by %s", moves, from, to, by)
	}
}

func (h *issueHarness) delivery(t *testing.T, taskID int64) *store.Delivery {
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

func wantStoredReady(t *testing.T, d *store.Delivery, ready bool, by store.ReadyBy) {
	t.Helper()
	if d.Ready == nil || *d.Ready != ready || d.ReadyBy != by {
		t.Errorf("stored readiness = %v by %q, want %v by %q", d.Ready, d.ReadyBy, ready, by)
	}
}

// TestIssueReadyAndUnready: a not-ready issue waits in in_progress; ready
// moves it to hand_off by a human, unready from a step's CLI takes it back
// by an agent, and marking an issue already ready changes nothing.
func TestIssueReadyAndUnready(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	notReady := false
	iss, task := h.finishedIssue(t, "explicit", &notReady)
	if got := h.must(t, http.StatusOK, http.MethodGet, fmt.Sprintf("/v1/issues/%d", iss.ID), nil); got.Lane != issuestate.LaneInProgress {
		t.Fatalf("lane before ready = %s, want in_progress", got.Lane)
	}

	mark := h.maxEvent(t)
	got := h.must(t, http.StatusOK, http.MethodPost, fmt.Sprintf("/v1/issues/%d/ready", iss.ID), nil)
	if got.Lane != issuestate.LaneHandOff {
		t.Errorf("ready answered lane %s, want hand_off", got.Lane)
	}
	wantLaneMove(t, h.laneMoves(t, mark), issuestate.LaneInProgress, issuestate.LaneHandOff, issuestate.Human)
	wantStoredReady(t, h.delivery(t, task.ID), true, store.ReadyByHuman)

	mark = h.maxEvent(t)
	h.must(t, http.StatusOK, http.MethodPost, fmt.Sprintf("/v1/issues/%d/ready", iss.ID), "{}")
	if moves := h.laneMoves(t, mark); len(moves) != 0 {
		t.Errorf("ready on a ready issue moved the lane: %v", moves)
	}

	mark = h.maxEvent(t)
	resp, out := h.do(t, http.MethodPost, fmt.Sprintf("/v1/issues/%d/unready", iss.ID), nil, headerTaskMarker, strconv.FormatInt(task.ID, 10))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unready = %d: %s", resp.StatusCode, out)
	}
	var back issueBody
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.Lane != issuestate.LaneInProgress {
		t.Errorf("unready answered lane %s, want in_progress", back.Lane)
	}
	wantLaneMove(t, h.laneMoves(t, mark), issuestate.LaneHandOff, issuestate.LaneInProgress, issuestate.Agent)
	wantStoredReady(t, h.delivery(t, task.ID), false, store.ReadyByAgent)
}

// readinessRefusal asserts a 409 invalid_state with details.reason, and
// returns the details.
func readinessRefusal(t *testing.T, h *issueHarness, path, reason string) map[string]string {
	t.Helper()
	resp, out := h.do(t, http.MethodPost, path, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST %s = %d, want 409: %s", path, resp.StatusCode, out)
	}
	var env errorBody
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("decode 409: %v", err)
	}
	if env.Error.Code != CodeInvalidState || env.Error.Details["reason"] != reason {
		t.Errorf("POST %s = %+v, want reason %q", path, env.Error, reason)
	}
	return env.Error.Details
}

// TestIssueReadinessRefusals: every refusal of ready and unready, each with
// the reason a client branches on.
func TestIssueReadinessRefusals(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	both := func(issueID int64) []string {
		return []string{fmt.Sprintf("/v1/issues/%d/ready", issueID), fmt.Sprintf("/v1/issues/%d/unready", issueID)}
	}

	for _, path := range both(9999) {
		if resp, out := h.do(t, http.MethodPost, path, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404: %s", path, resp.StatusCode, out)
		}
	}
	if resp, _ := h.do(t, http.MethodPost, "/v1/issues/x/ready", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("non-integer id = %d, want 400", resp.StatusCode)
	}

	closed, _ := h.finishedIssue(t, "closed", nil)
	h.must(t, http.StatusOK, http.MethodPost, fmt.Sprintf("/v1/issues/%d/close", closed.ID), nil)
	for _, path := range both(closed.ID) {
		resp, out := h.do(t, http.MethodPost, path, nil)
		var env errorBody
		_ = json.Unmarshal(out, &env)
		if resp.StatusCode != http.StatusConflict || env.Error.Details["state"] != "closed" {
			t.Errorf("POST %s on a closed issue = %d %s, want 409 state closed", path, resp.StatusCode, out)
		}
	}

	running, _ := h.finishedIssue(t, "running", nil)
	h.issueTask(t, running.ID, "running-again", store.TaskRunning)
	for _, path := range both(running.ID) {
		readinessRefusal(t, h, path, issueReasonWorkRunning)
	}

	empty := h.create(t, map[string]any{"title": "empty"})
	for _, path := range both(empty.ID) {
		readinessRefusal(t, h, path, issueReasonNoFinishedWork)
	}

	notReady := false
	waiting, _ := h.finishedIssue(t, "waiting", &notReady)
	if d := readinessRefusal(t, h, fmt.Sprintf("/v1/issues/%d/unready", waiting.ID), issueReasonNotHandOff); d["lane"] != string(issuestate.LaneInProgress) {
		t.Errorf("not hand_off details = %v, want lane in_progress", d)
	}

	claimed, _ := h.finishedIssue(t, "claimed", nil)
	ho := &store.Handoff{CreatedBy: store.HandoffByHuman}
	ht := &store.Task{
		ProjectID: h.pid, Title: "integrate", WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/integrate", State: store.TaskQueued,
	}
	if err := h.st.CreateHandoff(t.Context(), ho, []int64{claimed.ID}, ht, nil); err != nil {
		t.Fatalf("CreateHandoff: %v", err)
	}
	for _, path := range both(claimed.ID) {
		resp, out := h.do(t, http.MethodPost, path, nil)
		wantIssueInHandoff(t, path, resp, out, ho.ID)
	}
}

// TestDeliverReady: `ready` on a running task is stored as the task's
// whoever called, and a later deliver without it clears it; on a done task
// it is the caller's mark — a human's from the CLI, an agent's from a step —
// and moves the lane by that actor.
func TestDeliverReady(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	iss, err := h.st.CreateIssue(t.Context(), store.NewIssue{ProjectID: h.pid, Title: "i"}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	task := h.issueTask(t, iss.ID, "run", store.TaskRunning)
	path := fmt.Sprintf("/v1/tasks/%d/delivery", task.ID)
	deliver := func(body any, hdr ...string) *deliveryResponse {
		t.Helper()
		resp, out := h.do(t, http.MethodPost, path, body, hdr...)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("deliver = %d: %s", resp.StatusCode, out)
		}
		var got taskDeliveryResponse
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		return got.Delivery
	}

	if got := deliver(map[string]any{"title": "x", "ready": true}); got.Ready == nil || !*got.Ready || got.ReadyBy != string(store.ReadyByTask) {
		t.Errorf("running deliver ready = %v by %q, want true by task", got.Ready, got.ReadyBy)
	}
	if got := deliver(map[string]any{"title": "x"}); got.Ready != nil || got.ReadyBy != "" {
		t.Errorf("running deliver without ready = %v by %q, want it cleared", got.Ready, got.ReadyBy)
	}

	// Finished not ready: the issue waits in in_progress.
	notReady := false
	d := &store.Delivery{Kind: store.DeliveryChange, Title: "x", Ready: &notReady, ReadyBy: store.ReadyByWorkflow, RecordedAt: time.Now().UTC()}
	if _, _, err := h.st.TransitionTask(t.Context(), task.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: d}); err != nil {
		t.Fatal(err)
	}
	// A correction without ready keeps the stored readiness.
	if got := deliver(map[string]any{"title": "corrected"}); got.Ready == nil || *got.Ready || got.ReadyBy != string(store.ReadyByWorkflow) {
		t.Errorf("done correction = %v by %q, want false by workflow kept", got.Ready, got.ReadyBy)
	}

	mark := h.maxEvent(t)
	if got := deliver(map[string]any{"title": "corrected", "ready": true}, headerTaskMarker, "1"); got.Ready == nil || !*got.Ready || got.ReadyBy != string(store.ReadyByAgent) {
		t.Errorf("done deliver ready from a step = %v by %q, want true by agent", got.Ready, got.ReadyBy)
	}
	wantLaneMove(t, h.laneMoves(t, mark), issuestate.LaneInProgress, issuestate.LaneHandOff, issuestate.Agent)

	// Back out, then a human's mark through the delivery route.
	h.must(t, http.StatusOK, http.MethodPost, fmt.Sprintf("/v1/issues/%d/unready", iss.ID), nil)
	mark = h.maxEvent(t)
	if got := deliver(map[string]any{"title": "corrected", "ready": true}); got.ReadyBy != string(store.ReadyByHuman) {
		t.Errorf("done deliver ready by a human = by %q, want human", got.ReadyBy)
	}
	wantLaneMove(t, h.laneMoves(t, mark), issuestate.LaneInProgress, issuestate.LaneHandOff, issuestate.Human)

	resp, out := h.do(t, http.MethodGet, path, nil)
	var full taskDeliveryResponse
	if err := json.Unmarshal(out, &full); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET delivery = %d %v: %s", resp.StatusCode, err, out)
	}
	if full.Delivery.Ready == nil || !*full.Delivery.Ready || full.Delivery.ReadyBy != string(store.ReadyByHuman) {
		t.Errorf("GET delivery readiness = %v by %q", full.Delivery.Ready, full.Delivery.ReadyBy)
	}
}

// TestIssueReadinessToolsOverMCP: issue_ready and issue_unready replay the
// routes as an agent, and task_deliver carries `ready`.
func TestIssueReadinessToolsOverMCP(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	notReady := false
	iss, task := h.finishedIssue(t, "explicit", &notReady)
	call := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		hc := &http.Client{Transport: bearerRoundTripper{base: http.DefaultTransport, token: testToken}}
		client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
		cs, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: h.ts.URL + "/mcp", HTTPClient: hc}, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer func() { _ = cs.Close() }()
		res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		text := res.Content[0].(*sdk.TextContent).Text
		if res.IsError {
			t.Fatalf("%s failed: %s", tool, text)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("decode %s: %v: %s", tool, err, text)
		}
		return out
	}

	mark := h.maxEvent(t)
	if got := call("issue_ready", map[string]any{"id": iss.ID}); got["lane"] != string(issuestate.LaneHandOff) {
		t.Errorf("issue_ready lane = %v", got["lane"])
	}
	wantLaneMove(t, h.laneMoves(t, mark), issuestate.LaneInProgress, issuestate.LaneHandOff, issuestate.Agent)
	wantStoredReady(t, h.delivery(t, task.ID), true, store.ReadyByAgent)

	if got := call("issue_unready", map[string]any{"id": iss.ID}); got["lane"] != string(issuestate.LaneInProgress) {
		t.Errorf("issue_unready lane = %v", got["lane"])
	}
	wantStoredReady(t, h.delivery(t, task.ID), false, store.ReadyByAgent)

	got := call("task_deliver", map[string]any{"id": task.ID, "body": map[string]any{"title": "again", "ready": true}})
	d, _ := got["delivery"].(map[string]any)
	if d["ready"] != true || d["ready_by"] != string(store.ReadyByAgent) {
		t.Errorf("task_deliver ready = %v", d)
	}
}
