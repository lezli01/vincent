package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The forge-write guard on the pull-request write routes (task 137.7):
// a marked caller is an agent, refused before any `gh` call unless it is a
// live handoff's task granted the route's capability on a task in scope.

// markedPullWrite is pullWrite with a marker header.
func (h *githubHarness) markedPullWrite(t *testing.T, id int64, action string, body any, header, value string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost,
		h.ts.URL+"/v1/tasks/"+strconv.FormatInt(id, 10)+"/github/pull/"+action, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(header, value)
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, out
}

// grantedHandoff starts a live handoff over one handed-off local issue,
// granted grant, and links its task to #412.
func (h *githubHarness) grantedHandoff(t *testing.T, grant ...string) (*store.Handoff, *store.Task) {
	t.Helper()
	ctx := t.Context()
	iss, err := h.store.CreateIssue(ctx, store.NewIssue{ProjectID: h.projectID, Title: "delivered"}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	main := &store.Task{
		ProjectID: h.projectID, Title: "main", WorkflowName: "adhoc", BaseBranch: "main",
		BranchName: "vincent/main-task", State: store.TaskRunning, IssueID: &iss.ID, IssueWorktree: store.IssueWorktreeMain,
	}
	if err := h.store.CreateTask(ctx, main, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.TransitionTask(ctx, main.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: &store.Delivery{
		Kind: store.DeliveryChange, Title: "t", Body: "b", HeadSHA: "abc", Branch: main.BranchName, Base: "main",
		RecordedAt: time.Now().UTC().Truncate(time.Second),
	}}); err != nil {
		t.Fatal(err)
	}
	gj, err := store.GrantJSON(grant)
	if err != nil {
		t.Fatal(err)
	}
	hf := &store.Handoff{CreatedBy: store.HandoffByHuman, GrantJSON: gj}
	task := &store.Task{
		ProjectID: h.projectID, Title: "handoff", WorkflowName: "adhoc", BaseBranch: "main",
		BranchName: "vincent/handoff", State: store.TaskRunning,
	}
	if err := h.store.CreateHandoff(ctx, hf, []int64{iss.ID}, task, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SetTaskGitHubPull(ctx, task.ID,
		store.LinkPull("octo/repo", 412, github.SourceHuman, task.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	return hf, task
}

func TestPullWritesRefuseAnUngrantedAgent(t *testing.T) {
	t.Setenv("FAKEGH_STATE_FILE", filepath.Join(t.TempDir(), "state.json"))
	h := newGitHubHarness(t, nil, ghOrigin)
	task := h.linkedTask(t, "vincent/1-a-task")
	actions := append(slices.Clone(writeActions), struct {
		action string
		body   any
	}{"create", map[string]any{"title": "t"}})
	for _, marker := range [][2]string{
		{headerTaskMarker, strconv.FormatInt(task.ID, 10)},
		{headerChatMarker, "3"},
	} {
		for _, a := range actions {
			resp, raw := h.markedPullWrite(t, task.ID, a.action, a.body, marker[0], marker[1])
			if resp.StatusCode != http.StatusConflict || errorReason(t, raw) != issueReasonForgeWrite {
				t.Errorf("%s with %s = %d %s, want 409 %s", a.action, marker[0], resp.StatusCode, raw, issueReasonForgeWrite)
			}
		}
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Errorf("a refused write reached gh:\n%s", calls)
	}
	// A human's write is unchanged.
	if resp, raw := h.pullWrite(t, task.ID, "comment", map[string]any{"body": "hi"}); resp.StatusCode != http.StatusOK {
		t.Errorf("human comment = %d %s", resp.StatusCode, raw)
	}
}

func TestPullWritesAllowAGrantedHandoffInScope(t *testing.T) {
	t.Setenv("FAKEGH_STATE_FILE", filepath.Join(t.TempDir(), "state.json"))
	h := newGitHubHarness(t, nil, ghOrigin)
	_, ht := h.grantedHandoff(t, "pull.comment")
	marker := strconv.FormatInt(ht.ID, 10)
	other := h.linkedTask(t, "vincent/9-other")

	if resp, raw := h.markedPullWrite(t, ht.ID, "comment", map[string]any{"body": "hi"}, headerTaskMarker, marker); resp.StatusCode != http.StatusOK {
		t.Fatalf("granted comment = %d %s", resp.StatusCode, raw)
	}
	for _, tc := range []struct {
		name   string
		task   int64
		action string
		body   any
	}{
		{"a capability not granted", ht.ID, "merge", map[string]any{"method": "merge", "head_sha": fakeGHHead}},
		{"a task outside the handoff", other.ID, "comment", map[string]any{"body": "hi"}},
	} {
		resp, raw := h.markedPullWrite(t, tc.task, tc.action, tc.body, headerTaskMarker, marker)
		if resp.StatusCode != http.StatusConflict || errorReason(t, raw) != issueReasonForgeWrite {
			t.Errorf("%s = %d %s, want 409 %s", tc.name, resp.StatusCode, raw, issueReasonForgeWrite)
		}
	}

	// A settled handoff grants nothing.
	if _, _, err := h.store.TransitionTask(t.Context(), ht.ID, store.TaskRunning, store.TaskDone, store.TaskChange{}); err != nil {
		t.Fatal(err)
	}
	resp, raw := h.markedPullWrite(t, ht.ID, "comment", map[string]any{"body": "hi"}, headerTaskMarker, marker)
	if resp.StatusCode != http.StatusConflict || errorReason(t, raw) != issueReasonForgeWrite {
		t.Errorf("settled handoff comment = %d %s, want 409 %s", resp.StatusCode, raw, issueReasonForgeWrite)
	}
}
