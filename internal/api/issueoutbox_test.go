package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The forge-write guard and the sync block (task 130 decision 10, task
// 130.10).

func (h *issueHarness) imported(t *testing.T, key string, number int) *store.Issue {
	t.Helper()
	iss, _, err := h.st.UpsertRemoteIssue(t.Context(), store.RemoteIssue{
		ProjectID: h.pid, Provider: "github", RemoteKey: key, Repo: "o/r", Number: number,
		URL: fmt.Sprintf("https://github.com/o/r/issues/%d", number), Title: key, State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	return iss
}

func TestAgentMarkedCloseOfAnImportedIssueIsRefused(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	iss := h.imported(t, "I_1", 1)
	path := fmt.Sprintf("/v1/issues/%d/close", iss.ID)
	for _, hdr := range [][]string{{headerTaskMarker, "12"}, {headerChatMarker, "3"}} {
		resp, out := h.do(t, http.MethodPost, path, nil, hdr...)
		if d := conflict(t, resp, out); detailString(t, d, "reason") != issueReasonForgeWrite {
			t.Errorf("close with %s = %s", hdr[0], out)
		}
	}
	if n := h.pendingWrites(t); n != 0 {
		t.Fatalf("a refused close enqueued %d writes", n)
	}
	// A local issue is the agent's to close, marker or not.
	local := h.create(t, map[string]any{"title": "local"})
	resp, out := h.do(t, http.MethodPost, fmt.Sprintf("/v1/issues/%d/close", local.ID), nil, headerTaskMarker, "12")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("marked close of a local issue = %d: %s", resp.StatusCode, out)
	}

	// A human's plain close writes back: the sync block says pending.
	got := h.must(t, http.StatusOK, http.MethodPost, path, nil)
	if got.Sync == nil || got.Sync.State != issueSyncPending {
		t.Errorf("sync after a human close = %+v, want pending", got.Sync)
	}
	if n := h.pendingWrites(t); n != 1 {
		t.Errorf("a human close enqueued %d writes, want 1", n)
	}
	resp, out = h.do(t, http.MethodPost, fmt.Sprintf("/v1/issues/%d/reopen", iss.ID), nil, headerTaskMarker, "12")
	if d := conflict(t, resp, out); detailString(t, d, "reason") != issueReasonForgeWrite {
		t.Errorf("marked reopen = %s", out)
	}
	if local.Sync != nil {
		t.Errorf("a local issue carries a sync block: %+v", local.Sync)
	}
}

func (h *issueHarness) pendingWrites(t *testing.T) int {
	t.Helper()
	rows, err := h.st.PendingIssueWrites(t.Context(), timeZero)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestIssueSyncBlockAndCounts(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	ctx := t.Context()
	iss := h.imported(t, "I_1", 1)
	got := h.must(t, http.StatusOK, http.MethodGet, fmt.Sprintf("/v1/issues/%d", iss.ID), nil)
	if got.Sync == nil || got.Sync.State != issueSyncSynced || got.Sync.LastSyncedAt == nil {
		t.Errorf("fresh import sync = %+v, want synced", got.Sync)
	}
	h.must(t, http.StatusOK, http.MethodPost, fmt.Sprintf("/v1/issues/%d/close", iss.ID), nil)
	rows, _ := h.st.PendingIssueWrites(ctx, timeZero)
	if _, err := h.st.SettleIssueWrite(ctx, rows[0].ID, store.OutboxFailed, "no_write_scope", nil); err != nil {
		t.Fatal(err)
	}
	got = h.must(t, http.StatusOK, http.MethodGet, fmt.Sprintf("/v1/issues/%d", iss.ID), nil)
	if got.State != "closed" || got.Sync == nil || got.Sync.State != issueSyncFailed || got.Sync.Reason != "no_write_scope" {
		t.Errorf("after a failed write: state %s sync %+v", got.State, got.Sync)
	}
	st := h.syncStatus(t, http.MethodGet, h.pid, http.StatusOK)
	if st.WritesFailed != 1 || st.WritesPending != 0 {
		t.Errorf("counts = pending %d failed %d", st.WritesPending, st.WritesFailed)
	}
	moved := h.imported(t, "I_2", 2)
	if err := h.st.SetIssueRemoteStatus(ctx, h.pid, "github", "I_2", store.RemoteStatusMissing, ""); err != nil {
		t.Fatal(err)
	}
	got = h.must(t, http.StatusOK, http.MethodGet, fmt.Sprintf("/v1/issues/%d", moved.ID), nil)
	if got.Sync == nil || got.Sync.State != issueSyncFailed || got.Sync.Reason != "gone" {
		t.Errorf("missing remote sync = %+v, want failed/gone", got.Sync)
	}
}

func TestIssueCloseOverMCPOfAnImportedIssueIsRefused(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	iss := h.imported(t, "I_1", 1)
	creator := &store.Task{
		ProjectID: h.pid, Title: "creator", WorkflowName: "w", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "b", State: store.TaskRunning,
	}
	if err := h.st.CreateTask(t.Context(), creator, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	sess, err := h.srv.MCP().OpenStep(1, creator.ID, "plan")
	if err != nil {
		t.Fatalf("OpenStep: %v", err)
	}
	call := func(endpoint, token string, id int64) (string, bool) {
		t.Helper()
		hc := &http.Client{Transport: bearerRoundTripper{base: http.DefaultTransport, token: token}}
		client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
		cs, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: h.ts.URL + endpoint, HTTPClient: hc}, nil)
		if err != nil {
			t.Fatalf("connect %s: %v", endpoint, err)
		}
		defer func() { _ = cs.Close() }()
		res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "issue_close", Arguments: map[string]any{"id": id}})
		if err != nil {
			t.Fatalf("issue_close: %v", err)
		}
		return res.Content[0].(*sdk.TextContent).Text, res.IsError
	}
	for name, ep := range map[string][2]string{"/mcp": {"/mcp", testToken}, "step": {sess.URLPath(), sess.Secret}} {
		text, isErr := call(ep[0], ep[1], iss.ID)
		if !isErr || !strings.Contains(text, issueReasonForgeWrite) {
			t.Errorf("%s issue_close of an imported issue = %v %s, want refused", name, isErr, text)
		}
	}
	local := h.create(t, map[string]any{"title": "local"})
	text, isErr := call(sess.URLPath(), sess.Secret, local.ID)
	var out map[string]any
	if isErr || json.Unmarshal([]byte(text), &out) != nil || out["state"] != "closed" {
		t.Errorf("step issue_close of a local issue = %v %s", isErr, text)
	}
}

var timeZero time.Time
