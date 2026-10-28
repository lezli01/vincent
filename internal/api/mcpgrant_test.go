package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/mcp"
	"github.com/lezli01/vincent/internal/store"
)

// The granted handoff's step endpoint (task 137.7): the pull-request writes
// are tools there, filtered to the grant, and nowhere else.

// handedOff gives iss a done main task with a change delivery, which puts
// it in the hand_off lane, and returns that task.
func (h *issueHarness) handedOff(t *testing.T, issueID int64, branch string) *store.Task {
	t.Helper()
	ctx := t.Context()
	task := &store.Task{
		ProjectID: h.pid, Title: branch, WorkflowName: "w", WorkflowSnapshot: "x", BaseBranch: "main",
		BranchName: branch, State: store.TaskRunning, IssueID: &issueID, IssueWorktree: store.IssueWorktreeMain,
	}
	if err := h.st.CreateTask(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.TransitionTask(ctx, task.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: &store.Delivery{
		Kind: store.DeliveryChange, Title: "t", Body: "b", HeadSHA: "abc", Branch: branch, Base: "main",
		RecordedAt: time.Now().UTC().Truncate(time.Second),
	}}); err != nil {
		t.Fatal(err)
	}
	return task
}

// handoffOver starts a live handoff over issueID with grant.
func (h *issueHarness) handoffOver(t *testing.T, issueID int64, branch string, grant ...string) *store.Task {
	t.Helper()
	gj, err := store.GrantJSON(grant)
	if err != nil {
		t.Fatal(err)
	}
	task := &store.Task{
		ProjectID: h.pid, Title: branch, WorkflowName: "w", WorkflowSnapshot: "x", BaseBranch: "main",
		BranchName: branch, State: store.TaskRunning,
	}
	if err := h.st.CreateHandoff(t.Context(), &store.Handoff{CreatedBy: store.HandoffByHuman, GrantJSON: gj},
		[]int64{issueID}, task, nil); err != nil {
		t.Fatal(err)
	}
	return task
}

func (h *issueHarness) mcpSession(t *testing.T, endpoint, token string) *sdk.ClientSession {
	t.Helper()
	hc := &http.Client{Transport: bearerRoundTripper{base: http.DefaultTransport, token: token}}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: h.ts.URL + endpoint, HTTPClient: hc}, nil)
	if err != nil {
		t.Fatalf("connect %s: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (h *issueHarness) pullTools(t *testing.T, endpoint, token string) []string {
	t.Helper()
	res, err := h.mcpSession(t, endpoint, token).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var out []string
	for _, tool := range res.Tools {
		if slices.ContainsFunc(mcp.Granted, func(g mcp.GrantedRoute) bool { return g.Tool == tool.Name }) {
			out = append(out, tool.Name)
		}
	}
	slices.Sort(out)
	return out
}

func TestMCPPullWriteToolsOnlyOnAGrantedHandoffsStep(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	granted := h.imported(t, "I_1", 1)
	bare := h.imported(t, "I_2", 2)
	plain := h.handedOff(t, granted.ID, "vincent/main-1")
	h.handedOff(t, bare.ID, "vincent/main-2")
	grantedTask := h.handoffOver(t, granted.ID, "vincent/handoff-1", "issue.close", "pull.merge", "pull.close")
	bareTask := h.handoffOver(t, bare.ID, "vincent/handoff-2")

	open := func(run int64, task *store.Task) mcp.StepSession {
		t.Helper()
		sess, err := h.srv.MCP().OpenStep(run, task.ID, "integrate")
		if err != nil {
			t.Fatal(err)
		}
		return sess
	}
	if got := h.pullTools(t, "/mcp", testToken); len(got) != 0 {
		t.Errorf("/mcp lists %v", got)
	}
	for name, task := range map[string]*store.Task{"a non-handoff step": plain, "a grant-less handoff": bareTask} {
		sess := open(task.ID*10, task)
		if got := h.pullTools(t, sess.URLPath(), sess.Secret); len(got) != 0 {
			t.Errorf("%s lists %v", name, got)
		}
	}
	sess := open(1, grantedTask)
	want := []string{"task_github_pull_close", "task_github_pull_merge", "task_github_pull_reopen"}
	if got := h.pullTools(t, sess.URLPath(), sess.Secret); !slices.Equal(got, want) {
		t.Errorf("granted step lists %v, want %v", got, want)
	}

	// Over the same endpoint, the granted close of the item is honoured as
	// the handoff's; the other handoff's issue is refused.
	cs := h.mcpSession(t, sess.URLPath(), sess.Secret)
	call := func(id int64) (string, bool) {
		t.Helper()
		res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "issue_close", Arguments: map[string]any{"id": id}})
		if err != nil {
			t.Fatalf("issue_close: %v", err)
		}
		return res.Content[0].(*sdk.TextContent).Text, res.IsError
	}
	if text, isErr := call(bare.ID); !isErr || !strings.Contains(text, issueReasonForgeWrite) {
		t.Errorf("close outside the handoff = %v %s, want refused", isErr, text)
	}
	if text, isErr := call(granted.ID); isErr {
		t.Fatalf("granted close = %s", text)
	}
	writes, err := h.st.IssueWrites(t.Context(), granted.ID)
	if err != nil || len(writes) != 1 || writes[0].Op != store.OutboxOpSetState || string(writes[0].Origin) != "handoff" {
		t.Errorf("writes = %+v, %v; want one set_state by handoff", writes, err)
	}
}
