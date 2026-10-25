package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// Task 137.5: POST /v1/handoffs, its refusals and replay, and the reads.

// handOffIssue creates an issue in the hand_off lane: one main task that
// finished done with a change delivery.
func handOffIssue(t *testing.T, h *taskHarness, in store.NewIssue) *store.Issue {
	t.Helper()
	ctx := t.Context()
	in.ProjectID = h.projectID
	iss, err := h.store.CreateIssue(ctx, in, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	task := h.createTask(t, map[string]any{"project_id": h.projectID, "issue_id": iss.ID})
	setState(t, h, task.ID, store.TaskRunning)
	d := &store.Delivery{Kind: store.DeliveryChange, Title: in.Title, HeadSHA: "abc", RecordedAt: time.Now().UTC()}
	if _, _, err := h.store.TransitionTask(ctx, task.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: d}); err != nil {
		t.Fatalf("→ done: %v", err)
	}
	return iss
}

// wantStatus is decodeHandoff taking a request's (resp, body) pair whole.
func wantStatus(t *testing.T, want int) func(*http.Response, []byte) handoffBody {
	return func(resp *http.Response, body []byte) handoffBody {
		t.Helper()
		return decodeHandoff(t, resp, body, want)
	}
}

func decodeHandoff(t *testing.T, resp *http.Response, body []byte, want int) handoffBody {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status %d %s, want %d", resp.StatusCode, body, want)
	}
	var out handoffBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

func itemIDs(b handoffBody) []int64 {
	var out []int64
	for _, it := range b.Items {
		out = append(out, it.IssueID)
	}
	return out
}

func wantReason(t *testing.T, what string, resp *http.Response, body []byte, reason string) map[string]string {
	t.Helper()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("%s: %d %s, want 409", what, resp.StatusCode, body)
	}
	var env struct {
		Error struct {
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Details["reason"] != reason {
		t.Errorf("%s: details = %v, want reason %s", what, env.Error.Details, reason)
	}
	return env.Error.Details
}

// TestHandoffCreateExplicitIDs: the issues are claimed, the 201 carries the
// items and the task, and the task is a root task with no issue.
func TestHandoffCreateExplicitIDs(t *testing.T) {
	h := newActionHarness(t)
	a := handOffIssue(t, h, store.NewIssue{Title: "a"})
	b := handOffIssue(t, h, store.NewIssue{Title: "b"})
	resp, body := h.postMarked(t, "/v1/handoffs", map[string]any{
		"project_id": h.projectID, "workflow": "adhoc", "issue_ids": []int64{b.ID, a.ID},
		"grant": []string{"pull.merge", "issue.comment"}, "paused": true,
	})
	got := decodeHandoff(t, resp, body, http.StatusCreated)
	if got.State != "live" || got.CreatedBy != "human" || !slices.Equal(got.Grant, []string{"issue.comment", "pull.merge"}) {
		t.Errorf("handoff = %+v", got)
	}
	if !slices.Equal(itemIDs(got), []int64{a.ID, b.ID}) || got.Items[0].Delivery == nil || got.Items[0].DeliveryTaskID == nil {
		t.Errorf("items = %+v", got.Items)
	}
	if got.Task == nil || got.Task.ID != got.TaskID || got.Task.Title != "Hand off 2 issues" || got.Task.State != "paused" {
		t.Fatalf("task = %+v", got.Task)
	}
	task, err := h.store.GetTask(t.Context(), got.TaskID)
	if err != nil || task.IssueID != nil || task.BaseBranch != "main" {
		t.Errorf("handoff task = %+v, %v", task, err)
	}

	// Read back, one and listed.
	one := wantStatus(t, http.StatusOK)(h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/handoffs/%d", got.ID), nil))
	if one.ID != got.ID || len(one.Items) != 2 || one.Task != nil {
		t.Errorf("GET one = %+v", one)
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/handoffs/999999", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown handoff: %d %s", resp.StatusCode, body)
	}
}

// TestHandoffCreateSelect: a selection with max claims the expected issues
// in priority order, and a second finds what is left, then nothing.
func TestHandoffCreateSelect(t *testing.T) {
	h := newActionHarness(t)
	none := handOffIssue(t, h, store.NewIssue{Title: "none", Kind: "bug"})
	low := handOffIssue(t, h, store.NewIssue{Title: "low", Priority: 4, Kind: "bug"})
	urgent := handOffIssue(t, h, store.NewIssue{Title: "urgent", Priority: 1, Kind: "bug", Labels: []string{"Ship"}})
	feature := handOffIssue(t, h, store.NewIssue{Title: "feature", Priority: 1, Kind: "feature"})

	create := func(sel map[string]any) (*http.Response, []byte) {
		return h.postMarked(t, "/v1/handoffs", map[string]any{"project_id": h.projectID, "workflow": "adhoc", "select": sel})
	}
	got := wantStatus(t, http.StatusCreated)(create(map[string]any{"lane": "hand_off", "kind": "bug", "max": 2, "order": "priority"}))
	if !slices.Equal(itemIDs(got), []int64{low.ID, urgent.ID}) {
		t.Errorf("claimed %v, want urgent and low", itemIDs(got))
	}
	got = wantStatus(t, http.StatusCreated)(create(map[string]any{"delivery": []string{"change"}}))
	if !slices.Equal(itemIDs(got), []int64{none.ID, feature.ID}) || got.Task.Title != "Hand off 2 issues" {
		t.Errorf("second claimed %v (%s)", itemIDs(got), got.Task.Title)
	}
	resp, body := create(map[string]any{})
	wantReason(t, "empty selection", resp, body, reasonNothingToHandOff)

	// The list filters.
	var rows []handoffBody
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/handoffs?project_id=%d&state=live&issue_id=%d", h.projectID, urgent.ID), nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &rows) != nil || len(rows) != 1 || rows[0].Items != nil {
		t.Errorf("list by issue: %d %s", resp.StatusCode, body)
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/handoffs?state=settled", nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &rows) != nil || len(rows) != 0 {
		t.Errorf("list settled: %d %s", resp.StatusCode, body)
	}
	resp, _ = h.doJSON(t, http.MethodGet, "/v1/handoffs?state=bogus", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad state filter: %d", resp.StatusCode)
	}
}

// TestHandoffCreateRefusals: an ineligible id names itself and creates
// nothing; a claimed one names its handoff too; the body's shape errors are
// 400s.
func TestHandoffCreateRefusals(t *testing.T) {
	h := newActionHarness(t)
	ok := handOffIssue(t, h, store.NewIssue{Title: "ok"})
	open, err := h.store.CreateIssue(t.Context(), store.NewIssue{ProjectID: h.projectID, Title: "open"}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := h.store.ListTasks(t.Context(), store.TaskFilter{})
	resp, body := h.postMarked(t, "/v1/handoffs", map[string]any{
		"project_id": h.projectID, "workflow": "adhoc", "issue_ids": []int64{ok.ID, open.ID},
	})
	d := wantReason(t, "ineligible", resp, body, reasonIssueNotEligible)
	if d["issue_id"] != strconv.FormatInt(open.ID, 10) || d["handoff_id"] != "" {
		t.Errorf("details = %v", d)
	}
	after, _ := h.store.ListTasks(t.Context(), store.TaskFilter{})
	if len(after) != len(before) {
		t.Errorf("a refused create left a task: %d → %d", len(before), len(after))
	}
	if hs, _ := h.store.ListHandoffs(t.Context(), store.HandoffFilter{}); len(hs) != 0 {
		t.Errorf("a refused create left a handoff: %v", hs)
	}

	first := wantStatus(t, http.StatusCreated)(h.postMarked(t, "/v1/handoffs", map[string]any{
		"project_id": h.projectID, "workflow": "adhoc", "issue_ids": []int64{ok.ID},
	}))
	resp, body = h.postMarked(t, "/v1/handoffs", map[string]any{
		"project_id": h.projectID, "workflow": "adhoc", "issue_ids": []int64{ok.ID},
	})
	d = wantReason(t, "claimed", resp, body, reasonIssueNotEligible)
	if d["handoff_id"] != strconv.FormatInt(first.ID, 10) {
		t.Errorf("claimed details = %v", d)
	}

	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"project_id": h.projectID, "workflow": "adhoc", "issue_ids": []int64{ok.ID}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for name, req := range map[string]map[string]any{
		"both":            base(map[string]any{"select": map[string]any{}}),
		"neither":         {"project_id": h.projectID, "workflow": "adhoc"},
		"no workflow":     {"project_id": h.projectID, "issue_ids": []int64{ok.ID}},
		"duplicate ids":   base(map[string]any{"issue_ids": []int64{ok.ID, ok.ID}}),
		"bad lane":        {"project_id": h.projectID, "workflow": "adhoc", "select": map[string]any{"lane": "open"}},
		"bad order":       {"project_id": h.projectID, "workflow": "adhoc", "select": map[string]any{"order": "newest"}},
		"bad delivery":    {"project_id": h.projectID, "workflow": "adhoc", "select": map[string]any{"delivery": "patch"}},
		"zero max":        {"project_id": h.projectID, "workflow": "adhoc", "select": map[string]any{"max": 0}},
		"unknown grant":   base(map[string]any{"grant": []string{"repo.delete"}}),
		"issue_id option": base(map[string]any{"issue_id": ok.ID}),
		"branch_name":     base(map[string]any{"branch_name": "x"}),
	} {
		resp, body := h.postMarked(t, "/v1/handoffs", req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, resp.StatusCode, body)
		}
	}
}

// TestHandoffCreateAgentCallers: a grant from an agent caller — a request
// carrying a step's marker, or an MCP call — is refused grant_needs_human;
// an MCP create without one succeeds as created_by agent, and replays under
// its idempotency key.
func TestHandoffCreateAgentCallers(t *testing.T) {
	h := newActionHarness(t)
	a := handOffIssue(t, h, store.NewIssue{Title: "a"})
	req := map[string]any{"project_id": h.projectID, "workflow": "adhoc", "issue_ids": []int64{a.ID}, "grant": []string{"issue.close"}}
	resp, body := h.postMarked(t, "/v1/handoffs", req, headerTaskMarker, "42")
	wantReason(t, "marked", resp, body, reasonGrantNeedsHuman)
	resp, body = h.postMarked(t, "/v1/handoffs", req, headerChatMarker, "7")
	wantReason(t, "chat marked", resp, body, reasonGrantNeedsHuman)

	hc := &http.Client{Transport: bearerRoundTripper{base: http.DefaultTransport, token: testToken}}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: h.ts.URL + "/mcp", HTTPClient: hc}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	call := func(tool string, args map[string]any) (string, bool) {
		t.Helper()
		res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		return res.Content[0].(*sdk.TextContent).Text, res.IsError
	}
	text, isErr := call("handoff_start", map[string]any{"body": req})
	if !isErr || !strings.Contains(text, reasonGrantNeedsHuman) {
		t.Errorf("MCP grant: error %v %s", isErr, text)
	}
	delete(req, "grant")
	args := map[string]any{"body": req, "idempotency_key": "agent-1"}
	text, isErr = call("handoff_start", args)
	var got handoffBody
	if isErr || json.Unmarshal([]byte(text), &got) != nil || got.CreatedBy != "agent" || len(got.Items) != 1 {
		t.Fatalf("MCP create: error %v %s", isErr, text)
	}
	text, isErr = call("handoff_start", args)
	var again handoffBody
	if isErr || json.Unmarshal([]byte(text), &again) != nil || again.ID != got.ID {
		t.Errorf("MCP replay: error %v %s, want handoff %d", isErr, text, got.ID)
	}
	text, isErr = call("handoff_show", map[string]any{"id": got.ID})
	if isErr || !strings.Contains(text, `"items"`) {
		t.Errorf("handoff_show: %v %s", isErr, text)
	}
}

// TestHandoffCreateIdempotency: the same key and body replay the same
// handoff; the same key with another body is 409 idempotency_key_reused.
func TestHandoffCreateIdempotency(t *testing.T) {
	h := newActionHarness(t)
	a := handOffIssue(t, h, store.NewIssue{Title: "a"})
	b := handOffIssue(t, h, store.NewIssue{Title: "b"})
	req := map[string]any{"project_id": h.projectID, "workflow": "adhoc", "issue_ids": []int64{a.ID}}
	first := wantStatus(t, http.StatusCreated)(h.postMarked(t, "/v1/handoffs", req, idempotencyHeader, "k1"))
	again := wantStatus(t, http.StatusCreated)(h.postMarked(t, "/v1/handoffs", req, idempotencyHeader, "k1"))
	if again.ID != first.ID || again.TaskID != first.TaskID || len(again.Items) != 1 {
		t.Errorf("replay = %+v, want handoff %d", again, first.ID)
	}
	req["issue_ids"] = []int64{b.ID}
	resp, body := h.postMarked(t, "/v1/handoffs", req, idempotencyHeader, "k1")
	wantReason(t, "reused", resp, body, idempotencyReasonReused)
	// The task route's key space is separate: the same key there is new.
	resp, body = h.postMarked(t, "/v1/tasks", map[string]any{"project_id": h.projectID, "title": "x"}, idempotencyHeader, "k1")
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("task create under a handoff key: %d %s", resp.StatusCode, body)
	}
}
