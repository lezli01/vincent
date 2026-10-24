package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// Task 137.4: new work on an issue a handoff claims is a 409
// `issue_in_handoff`, except from the handoff's own task.

// postMarked is a POST through doJSON's path with extra header pairs.
func (h *projectHarness) postMarked(t *testing.T, path string, body any, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	enc, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, h.ts.URL+path, bytes.NewReader(enc))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, out
}

// claimIssue creates an issue with one finished main task and a handoff
// claiming it, and returns the issue, its task's id and the handoff.
func claimIssue(t *testing.T, st *store.Store, projectID int64, create func(issueID int64) int64) (*store.Issue, int64, *store.Handoff, *store.Task) {
	t.Helper()
	ctx := t.Context()
	iss, err := st.CreateIssue(ctx, store.NewIssue{ProjectID: projectID, Title: "Claimed"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	taskID := create(iss.ID)
	for _, to := range []store.TaskState{store.TaskRunning, store.TaskDone} {
		cur, err := st.GetTask(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.TransitionTask(ctx, taskID, cur.State, to, store.TaskChange{}); err != nil {
			t.Fatalf("→ %s: %v", to, err)
		}
	}
	h := &store.Handoff{CreatedBy: store.HandoffByHuman}
	ht := &store.Task{
		ProjectID: projectID, Title: "integrate", WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/integrate", State: store.TaskQueued,
	}
	if err := st.CreateHandoff(ctx, h, []int64{iss.ID}, ht, nil); err != nil {
		t.Fatalf("CreateHandoff: %v", err)
	}
	return iss, taskID, h, ht
}

func wantIssueInHandoff(t *testing.T, what string, resp *http.Response, body []byte, handoffID int64) {
	t.Helper()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("%s: %d %s, want 409", what, resp.StatusCode, body)
	}
	var env struct {
		Error struct {
			Code    string            `json:"code"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != CodeInvalidState || env.Error.Details["reason"] != reasonIssueInHandoff ||
		env.Error.Details["handoff_id"] != strconv.FormatInt(handoffID, 10) {
		t.Errorf("%s: error = %+v", what, env.Error)
	}
}

// TestTaskCreateAndFollowUpOnClaimedIssue: POST /v1/tasks and follow_up
// refuse; the same requests marked as the handoff's own task pass.
func TestTaskCreateAndFollowUpOnClaimedIssue(t *testing.T) {
	h := newActionHarness(t)
	iss, mainID, ho, ht := claimIssue(t, h.store, h.projectID, func(issueID int64) int64 {
		return h.createTask(t, map[string]any{"issue_id": issueID}).ID
	})
	marker := strconv.FormatInt(ht.ID, 10)

	create := map[string]any{"project_id": h.projectID, "issue_id": iss.ID, "title": "more"}
	resp, body := h.postMarked(t, "/v1/tasks", create)
	wantIssueInHandoff(t, "create", resp, body, ho.ID)
	resp, body = h.postMarked(t, "/v1/tasks", create, headerTaskMarker, "999999")
	wantIssueInHandoff(t, "create marked as another task", resp, body, ho.ID)

	path := fmt.Sprintf("/v1/tasks/%d/follow_up", mainID)
	resp, body = h.postMarked(t, path, map[string]any{"prompt": "again"})
	wantIssueInHandoff(t, "follow_up", resp, body, ho.ID)
	resp, body = h.postMarked(t, path, map[string]any{"prompt": "again"}, headerTaskMarker, marker)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("follow_up as the handoff's task: %d %s", resp.StatusCode, body)
	}

	resp, body = h.postMarked(t, "/v1/tasks", create, headerTaskMarker, marker)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create as the handoff's task: %d %s", resp.StatusCode, body)
	}
}

// TestChatHandoffOnClaimedIssue: handing a chat off onto a claimed issue is
// the same 409, and the chat stays idle.
func TestChatHandoffOnClaimedIssue(t *testing.T) {
	h := newChatHarness(t)
	iss, _, ho, _ := claimIssue(t, h.store, h.projectID, func(issueID int64) int64 {
		task := &store.Task{
			ProjectID: h.projectID, Title: "first", WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
			BaseBranch: "main", BranchName: "vincent/first", State: store.TaskQueued,
			IssueID: &issueID, IssueWorktree: store.IssueWorktreeMain,
		}
		if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return task.ID
	})
	chatID, _ := handoffFixture(t, h)
	resp, body := h.postMarked(t, "/v1/chats/"+strconv.FormatInt(chatID, 10)+"/handoff",
		map[string]any{"issue_id": iss.ID})
	wantIssueInHandoff(t, "chat handoff", resp, body, ho.ID)
	c, err := h.store.GetChat(t.Context(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if c.HandoffTaskID != nil {
		t.Errorf("a refused handoff linked task %d", *c.HandoffTaskID)
	}
}
