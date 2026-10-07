package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// The CLI half of task 134.15: `task add --separate-worktree/--merge`, the
// queued-behind note, `task show`'s role rows, and `chat handoff --merge`,
// against the real API handlers.

// issueWorktreeFixture is a project on a real repository with one issue, the
// shape a side task needs: the daemon checks the issue's main branch in git.
type issueWorktreeFixture struct {
	h       *liveHarness
	repo    string
	project string
	issue   string
}

func newIssueWorktreeFixture(t *testing.T) issueWorktreeFixture {
	t.Helper()
	// A create on an issue resolves its main worktree through the manager,
	// which the harness leaves nil by default.
	h := newLiveHarness(t, func(o *liveOptions) { o.worktrees = true })
	ctx := t.Context()
	repo := testrepo.Init(t, "main")
	p := &store.Project{Name: "shared", Path: repo, DefaultBranch: "main"}
	if err := h.st.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	iss, err := h.st.CreateIssue(ctx, store.NewIssue{ProjectID: p.ID, Title: "Share a branch"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return issueWorktreeFixture{
		h: h, repo: repo, project: strconv.FormatInt(p.ID, 10), issue: strconv.FormatInt(iss.ID, 10),
	}
}

// occupyMainWorktree creates the issue's main task and admits it by hand, the
// way the scheduler would, then cuts its branch: the runner is never started
// here, and a side task is cut from that branch.
func (f issueWorktreeFixture) occupyMainWorktree(t *testing.T) apiclient.TaskDetail {
	t.Helper()
	main := taskAddJSON(t, "task", "add", "--project", f.project, "--issue", f.issue)
	if main.IssueWorktree == nil || *main.IssueWorktree != "main" || main.MergeBack != nil {
		t.Fatalf("main task = %v / %+v", main.IssueWorktree, main.MergeBack)
	}
	if _, _, err := f.h.st.TransitionTask(t.Context(), main.ID, store.TaskQueued, store.TaskRunning, store.TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	testrepo.Run(t, f.repo, "branch", main.BranchName)
	return main
}

// taskAddJSON runs a create with --json and decodes the task it answers with.
func taskAddJSON(t *testing.T, args ...string) apiclient.TaskDetail {
	t.Helper()
	out, errOut, code := runCLI(t, append(args, "--json")...)
	if code != 0 {
		t.Fatalf("%v: exit %d (%s)", args, code, errOut)
	}
	if errOut != "" {
		t.Errorf("%v --json wrote to stderr: %q", args, errOut)
	}
	var task apiclient.TaskDetail
	if err := json.Unmarshal([]byte(out), &task); err != nil {
		t.Fatalf("%v --json = %q: %v", args, out, err)
	}
	return task
}

func TestTaskAddSeparateWorktreeMakesASideTask(t *testing.T) {
	f := newIssueWorktreeFixture(t)
	main := f.occupyMainWorktree(t)

	side := taskAddJSON(t, "task", "add", "--project", f.project, "--issue", f.issue,
		"--title", "side", "--separate-worktree", "--merge", "agent")
	if side.IssueWorktree == nil || *side.IssueWorktree != "side" ||
		side.MergeBack == nil || side.MergeBack.OnConflict != "agent" {
		t.Fatalf("side task = %v / %+v", side.IssueWorktree, side.MergeBack)
	}
	if side.BaseBranch != main.BranchName {
		t.Errorf("side base = %q, want the main branch %q", side.BaseBranch, main.BranchName)
	}
	// The API's own view, not only the create response.
	got := taskShowJSON(t, side.ID)
	if got.IssueWorktree == nil || *got.IssueWorktree != "side" || got.MergeBack == nil || got.MergeBack.OnConflict != "agent" {
		t.Errorf("GET side task = %v / %+v", got.IssueWorktree, got.MergeBack)
	}

	// --merge defaults to block, and no queued note: a side task never waits
	// for the main worktree.
	out, errOut, code := runCLI(t, "task", "add", "--project", f.project, "--issue", f.issue,
		"--title", "side two", "--separate-worktree")
	if code != 0 {
		t.Fatalf("side two: exit %d (%s)", code, errOut)
	}
	if strings.Contains(errOut, "queued behind") {
		t.Errorf("a side task printed the queued note: %q", errOut)
	}
	id, _, _ := strings.Cut(strings.TrimPrefix(out, "task "), " ")
	got = taskShowJSON(t, mustInt(t, id))
	if got.MergeBack == nil || got.MergeBack.OnConflict != "block" {
		t.Errorf("side two merge_back = %+v, want block", got.MergeBack)
	}
}

func TestTaskAddQueuedBehindNote(t *testing.T) {
	f := newIssueWorktreeFixture(t)
	main := f.occupyMainWorktree(t)

	_, errOut, code := runCLI(t, "task", "add", "--project", f.project, "--issue", f.issue, "--title", "next")
	if code != 0 {
		t.Fatalf("next: exit %d (%s)", code, errOut)
	}
	want := "queued behind #" + strconv.FormatInt(main.ID, 10) +
		" (main worktree busy); pass --separate-worktree to run now\n"
	if !strings.Contains(errOut, want) {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}

	// Never under --json: taskAddJSON fails on any stderr, and the hint is
	// still in the body for a script that wants it.
	next := taskAddJSON(t, "task", "add", "--project", f.project, "--issue", f.issue, "--title", "next again")
	if next.MainWorktreeOccupantTaskID == nil || *next.MainWorktreeOccupantTaskID != main.ID {
		t.Errorf("--json hint = %v, want %d", next.MainWorktreeOccupantTaskID, main.ID)
	}
}

func TestTaskShowIssueWorktreeRows(t *testing.T) {
	f := newIssueWorktreeFixture(t)
	main := f.occupyMainWorktree(t)
	agentSide := taskAddJSON(t, "task", "add", "--project", f.project, "--issue", f.issue,
		"--title", "agent side", "--separate-worktree", "--merge", "agent")
	blockSide := taskAddJSON(t, "task", "add", "--project", f.project, "--issue", f.issue,
		"--title", "block side", "--separate-worktree", "--merge", "block")

	for _, tc := range []struct {
		id      int64
		want    []string
		notWant []string
	}{
		{main.ID, []string{"worktree  main\n"}, []string{"merge "}},
		{agentSide.ID, []string{"worktree  side\n", "merge     agent\n"}, nil},
		{blockSide.ID, []string{"worktree  side\n", "merge     manual\n"}, nil},
		// The harness's own task has no issue, so no role at all.
		{f.h.taskID, nil, []string{"worktree ", "merge "}},
	} {
		out, errOut, code := runCLI(t, "task", "show", strconv.FormatInt(tc.id, 10))
		if code != 0 {
			t.Fatalf("show %d: exit %d (%s)", tc.id, code, errOut)
		}
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("show %d lacks %q:\n%s", tc.id, w, out)
			}
		}
		for _, w := range tc.notWant {
			if strings.Contains(out, w) {
				t.Errorf("show %d carries %q:\n%s", tc.id, w, out)
			}
		}
	}
}

// TestTaskAddSeparateWorktreeRefusals proves every bad combination is refused
// before a request reaches the daemon.
func TestTaskAddSeparateWorktreeRefusals(t *testing.T) {
	var creates atomic.Int64
	h := newLiveHarness(t, withHandlerWrap(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/v1/tasks" {
				creates.Add(1)
			}
			next.ServeHTTP(w, r)
		})
	}))
	p := h.project()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no issue", []string{"--title", "x", "--separate-worktree"}, "--separate-worktree needs --issue"},
		{"merge without separate", []string{"--issue", "1", "--merge", "agent"}, "--merge needs --separate-worktree"},
		{"merge block without separate", []string{"--issue", "1", "--merge", "block"}, "--merge needs --separate-worktree"},
		{"bad merge", []string{"--issue", "1", "--separate-worktree", "--merge", "rebase"}, `--merge must be block or agent, got "rebase"`},
		{"existing branch", []string{"--issue", "1", "--separate-worktree", "--existing-branch"}, "existing-branch"},
		{"github pull", []string{"--separate-worktree", "--github-pull", "7"}, "github-pull"},
		{"branch", []string{"--issue", "1", "--separate-worktree", "--branch", "b"}, "branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := creates.Load()
			_, errOut, code := runCLI(t, append([]string{"task", "add", "--project", p}, tc.args...)...)
			if code == 0 {
				t.Fatalf("exit 0, want a refusal")
			}
			if !strings.Contains(errOut, tc.want) {
				t.Errorf("stderr = %q, want %q", errOut, tc.want)
			}
			if n := creates.Load() - before; n != 0 {
				t.Errorf("%d POST /v1/tasks sent, want none", n)
			}
		})
	}
}

// TestChatHandoffMergeSendsMergeBack proves the request body: the daemon's
// acceptance of merge_back on a handoff is not this unit's, so what is
// asserted is what the CLI sends, captured on the wire.
func TestChatHandoffMergeSendsMergeBack(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []map[string]json.RawMessage
	)
	newLiveHarness(t, withHandlerWrap(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/handoff") {
				data, _ := io.ReadAll(r.Body)
				var body map[string]json.RawMessage
				_ = json.Unmarshal(data, &body)
				mu.Lock()
				bodies = append(bodies, body)
				mu.Unlock()
				r.Body = io.NopCloser(bytes.NewReader(data))
			}
			next.ServeHTTP(w, r)
		})
	}))
	sent := func() []map[string]json.RawMessage {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]json.RawMessage(nil), bodies...)
	}

	// Chat 999 does not exist, so every request that is sent fails; the body
	// is the claim under test, not the outcome.
	for _, mode := range []string{"agent", "block"} {
		before := len(sent())
		runCLI(t, "chat", "handoff", "999", "--title", "x", "--issue", "7", "--merge", mode)
		got := sent()
		if len(got) != before+1 {
			t.Fatalf("--merge %s sent %d requests, want 1", mode, len(got)-before)
		}
		if string(got[before]["issue_id"]) != "7" {
			t.Errorf("--merge %s: issue_id = %s, want 7", mode, got[before]["issue_id"])
		}
		var mb apiclient.MergeBack
		if err := json.Unmarshal(got[before]["merge_back"], &mb); err != nil || mb.OnConflict != mode {
			t.Errorf("--merge %s: merge_back = %s (%v)", mode, got[before]["merge_back"], err)
		}
	}

	// No --merge, no merge_back: a plain handoff's body is unchanged.
	before := len(sent())
	runCLI(t, "chat", "handoff", "999", "--title", "x")
	got := sent()
	if len(got) != before+1 {
		t.Fatalf("plain handoff sent %d requests, want 1", len(got)-before)
	}
	if raw, ok := got[before]["merge_back"]; ok {
		t.Errorf("plain handoff sent merge_back %s", raw)
	}
	if raw, ok := got[before]["issue_id"]; ok {
		t.Errorf("plain handoff sent issue_id %s", raw)
	}

	// A bad value never reaches the wire.
	before = len(sent())
	_, errOut, code := runCLI(t, "chat", "handoff", "999", "--title", "x", "--issue", "7", "--merge", "theirs")
	if code == 0 || !strings.Contains(errOut, `--merge must be block or agent, got "theirs"`) {
		t.Errorf("bad --merge: exit %d, %q", code, errOut)
	}
	if n := len(sent()) - before; n != 0 {
		t.Errorf("bad --merge sent %d requests, want none", n)
	}

	// --merge without --issue is the daemon's 400 every time (review F2 of
	// the 598 train), so it is refused before the request.
	for _, mode := range []string{"agent", "block"} {
		before = len(sent())
		_, errOut, code = runCLI(t, "chat", "handoff", "999", "--title", "x", "--merge", mode)
		if code == 0 || !strings.Contains(errOut, "--merge needs --issue") {
			t.Errorf("--merge %s without --issue: exit %d, %q", mode, code, errOut)
		}
		if n := len(sent()) - before; n != 0 {
			t.Errorf("--merge %s without --issue sent %d requests, want none", mode, n)
		}
	}
}

// TestChatHandoffMergeOnAnIssue hands a real chat off through the real
// handlers: with --issue on an issue that has a main branch, --merge agent
// makes a side task merged back on agent, which is the case the flag exists
// for and which a fake server answering 201 could not prove.
func TestChatHandoffMergeOnAnIssue(t *testing.T) {
	f := newIssueWorktreeFixture(t)
	main := f.occupyMainWorktree(t)

	// The chat's own worktree and branch, cut the way a chat's would be.
	// Rows go through the store because POST /v1/chats would need an agent.
	dir := filepath.Join(t.TempDir(), "chat-wt")
	testrepo.Run(t, f.repo, "worktree", "add", "-q", "-b", "chat/side", dir, "main")
	sha := strings.TrimSpace(testrepo.Run(t, f.repo, "rev-parse", "main"))
	pid, _ := strconv.ParseInt(f.project, 10, 64)
	c := &store.Chat{
		ProjectID: pid, Title: "side work", State: chatstate.Idle, Agent: "claude",
		PermissionMode: string(agent.FullAuto), Branch: "chat/side",
		BaseBranch: "main", BaseSHA: sha, WorktreePath: dir,
	}
	if err := f.h.st.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}

	out, errOut, code := runCLI(t, "chat", "handoff", strconv.FormatInt(c.ID, 10),
		"--title", "side", "--issue", f.issue, "--merge", "agent", "--json")
	if code != 0 {
		t.Fatalf("handoff: exit %d (%s)", code, errOut)
	}
	var got struct {
		Task apiclient.TaskDetail `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("handoff --json = %q: %v", out, err)
	}
	if got.Task.Issue == nil || strconv.FormatInt(got.Task.Issue.ID, 10) != f.issue {
		t.Errorf("handoff issue = %+v, want %s", got.Task.Issue, f.issue)
	}
	side := taskShowJSON(t, got.Task.ID)
	if side.IssueWorktree == nil || *side.IssueWorktree != "side" ||
		side.MergeBack == nil || side.MergeBack.OnConflict != "agent" {
		t.Fatalf("handed-off task = %v / %+v, want a side task on agent", side.IssueWorktree, side.MergeBack)
	}
	if side.BranchName != "chat/side" || side.ID == main.ID {
		t.Errorf("handed-off task branch = %q (id %d), want the chat's", side.BranchName, side.ID)
	}
}

// taskShowJSON is the API's view of a task, read back through `task show`.
func taskShowJSON(t *testing.T, id int64) apiclient.TaskDetail {
	t.Helper()
	out, errOut, code := runCLI(t, "task", "show", strconv.FormatInt(id, 10), "--json")
	if code != 0 {
		t.Fatalf("show %d: exit %d (%s)", id, code, errOut)
	}
	var task apiclient.TaskDetail
	if err := json.Unmarshal([]byte(out), &task); err != nil {
		t.Fatalf("show %d --json = %q: %v", id, out, err)
	}
	return task
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}
