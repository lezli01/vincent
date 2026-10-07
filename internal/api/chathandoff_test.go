package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// handoffFixture creates one chat and returns its id and the chat body.
func handoffFixture(t *testing.T, h *chatHarness) (int64, map[string]any) {
	t.Helper()
	code, body := h.create(t, "claude")
	if code != http.StatusCreated {
		t.Fatalf("create chat = %d (%v)", code, body)
	}
	id, ok := body["id"].(float64)
	if !ok {
		t.Fatalf("chat body has no id: %v", body)
	}
	return int64(id), body
}

func (h *chatHarness) handoff(t *testing.T, id int64, req map[string]any) (int, map[string]any) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPost, "/v1/chats/"+strconv.FormatInt(id, 10)+"/handoff", req)
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out
}

// TestHandoffInheritsTheWorkspaceExactly is the acceptance criterion, asserted
// character for character: the task's workspace is the chat's, not a copy of
// it and not a replacement for it.
func TestHandoffInheritsTheWorkspaceExactly(t *testing.T) {
	h := newChatHarness(t)
	id, chat := handoffFixture(t, h)
	code, body := h.handoff(t, id, map[string]any{"title": "finish the exploration"})
	if code != http.StatusCreated {
		t.Fatalf("handoff = %d (%v)", code, body)
	}
	task, _ := body["task"].(map[string]any)
	for _, f := range []struct{ task, chat string }{
		{"branch_name", "branch"},
		{"base_branch", "base_branch"},
		{"worktree_path", "worktree_path"},
		{"base_sha", "base_sha"},
	} {
		if got, want := task[f.task], chat[f.chat]; got != want {
			t.Errorf("task %s = %v, want the chat's %s %v", f.task, got, f.chat, want)
		}
	}
	if task["project_id"] != chat["project_id"] {
		t.Errorf("task project_id = %v, want %v", task["project_id"], chat["project_id"])
	}
	// And the link, in both directions.
	after, _ := body["chat"].(map[string]any)
	if after["state"] != string(chatstate.HandedOff) {
		t.Errorf("chat state = %v, want handed_off", after["state"])
	}
	if after["handoff_task_id"] != task["id"] {
		t.Errorf("chat handoff_task_id = %v, want %v", after["handoff_task_id"], task["id"])
	}
	if task["source_chat_id"] != chat["id"] {
		t.Errorf("task source_chat_id = %v, want %v", task["source_chat_id"], chat["id"])
	}
	// The claim moved rather than being shared: two rows naming one directory
	// is the ambiguity gc must never see (§10).
	if after["worktree_path"] != nil && after["worktree_path"] != "" {
		t.Errorf("chat still claims %v after the handoff", after["worktree_path"])
	}
	// The reverse lookup is a real query, not a rendering of the response.
	got, err := h.store.SourceChatID(t.Context(), int64(task["id"].(float64)))
	if err != nil {
		t.Fatalf("source chat: %v", err)
	}
	if got != id {
		t.Errorf("SourceChatID = %d, want %d", got, id)
	}
}

// TestHandoffPreservesDirtyAndCommittedWork is the "no implicit commit, copy,
// merge or rename" criterion: the directory is simply not touched.
func TestHandoffPreservesDirtyAndCommittedWork(t *testing.T) {
	h := newChatHarness(t)
	id, chat := handoffFixture(t, h)
	dir, _ := chat["worktree_path"].(string)
	if dir == "" {
		t.Fatal("the chat has no worktree")
	}
	committed := filepath.Join(dir, "committed.txt")
	if err := os.WriteFile(committed, []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "committed.txt"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-m", "chat work"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	dirty := filepath.Join(dir, "dirty.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head := gitOut(t, dir, "rev-parse", "HEAD")

	code, body := h.handoff(t, id, map[string]any{"title": "carry on"})
	if code != http.StatusCreated {
		t.Fatalf("handoff = %d (%v)", code, body)
	}
	task, _ := body["task"].(map[string]any)
	if task["worktree_path"] != dir {
		t.Fatalf("task worktree = %v, want %s", task["worktree_path"], dir)
	}
	for _, f := range []string{committed, dirty} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s did not survive the handoff: %v", f, err)
		}
	}
	// No hidden commit: HEAD is exactly where the conversation left it, and
	// the uncommitted file is still uncommitted.
	if got := gitOut(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved from %s to %s", head, got)
	}
	if out := gitOut(t, dir, "status", "--porcelain"); out == "" {
		t.Error("the uncommitted file was committed or removed by the handoff")
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestHandoffIsTerminal covers the whole of "the chat cannot be resumed or
// handed off twice", through the routes a client would actually try.
func TestHandoffIsTerminal(t *testing.T) {
	h := newChatHarness(t)
	id, _ := handoffFixture(t, h)
	if code, body := h.handoff(t, id, map[string]any{"title": "first"}); code != http.StatusCreated {
		t.Fatalf("handoff = %d (%v)", code, body)
	}
	for _, tc := range []struct {
		route string
		body  map[string]any
	}{
		{"handoff", map[string]any{"title": "second"}},
		{"send", map[string]any{"message": "hello?"}},
		{"cancel", nil},
		{"archive", nil},
	} {
		resp, body := h.doJSON(t, http.MethodPost, "/v1/chats/"+strconv.FormatInt(id, 10)+"/"+tc.route, tc.body)
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("%s on a handed-off chat = %d, want 409 (%s)", tc.route, resp.StatusCode, body)
		}
	}
	// answer takes an InputResponse, and is refused for the same reason.
	resp, body := h.doJSON(t, http.MethodPost, "/v1/chats/"+strconv.FormatInt(id, 10)+"/answer",
		map[string]any{"text": "no"})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("answer on a handed-off chat = %d, want 409 (%s)", resp.StatusCode, body)
	}
}

// TestHandoffLeavesTheChatAloneWhenTheTaskDoesNotValidate is the atomicity
// half a client can see: validation happens before anything is written, so a
// refused handoff is indistinguishable from one that never happened.
func TestHandoffLeavesTheChatAloneWhenTheTaskDoesNotValidate(t *testing.T) {
	h := newChatHarness(t)
	id, chat := handoffFixture(t, h)
	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"unknown workflow", map[string]any{"title": "x", "workflow": "nope"}, http.StatusBadRequest},
		{"no title", map[string]any{}, http.StatusBadRequest},
		{"unknown agent", map[string]any{"title": "x", "agent": "nope"}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := h.handoff(t, id, tc.body); code != tc.want {
				t.Fatalf("handoff = %d, want %d (%v)", code, tc.want, body)
			}
			c, err := h.store.GetChat(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if c.State != chatstate.Idle {
				t.Errorf("chat state = %s, want idle", c.State)
			}
			if c.WorktreePath != chat["worktree_path"] {
				t.Errorf("chat worktree = %q, want %v", c.WorktreePath, chat["worktree_path"])
			}
			if c.HandoffTaskID != nil {
				t.Errorf("chat links task %d after a refused handoff", *c.HandoffTaskID)
			}
			tasks, err := h.store.ListTasks(t.Context(), store.TaskFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if len(tasks) != 0 {
				t.Errorf("a refused handoff left %d task(s) behind", len(tasks))
			}
		})
	}
}

// TestHandoffRefusesAnOperationInProgress is decision 4: a half-finished
// rebase is named rather than inherited silently.
func TestHandoffRefusesAnOperationInProgress(t *testing.T) {
	h := newChatHarness(t)
	id, chat := handoffFixture(t, h)
	dir, _ := chat["worktree_path"].(string)
	gitDir := gitOut(t, dir, "rev-parse", "--absolute-git-dir")
	if err := os.MkdirAll(filepath.Join(gitDir, "rebase-merge"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, body := h.handoff(t, id, map[string]any{"title": "carry on"})
	if code != http.StatusConflict {
		t.Fatalf("handoff over a rebase = %d, want 409 (%v)", code, body)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != worktree.ReasonRepoOperationInProgress {
		t.Errorf("error code = %v, want %q", errObj["code"], worktree.ReasonRepoOperationInProgress)
	}
	details, _ := errObj["details"].(map[string]any)
	if details["operation"] != "rebase" {
		t.Errorf("details.operation = %v, want rebase", details["operation"])
	}
	// Ordinary dirty state, by contrast, is not a refusal: it is the feature.
	if err := os.RemoveAll(filepath.Join(gitDir, "rebase-merge")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, body := h.handoff(t, id, map[string]any{"title": "carry on"}); code != http.StatusCreated {
		t.Fatalf("handoff over a dirty worktree = %d, want 201 (%v)", code, body)
	}
}

// TestHandoffRefusesAChatWithNoWorktree is the edge case the brief names: a
// chat whose claim is gone produces a typed refusal, never a task with no
// workspace that admission would quietly fill in by cutting a new one.
func TestHandoffRefusesAChatWithNoWorktree(t *testing.T) {
	h := newChatHarness(t)
	id, _ := handoffFixture(t, h)
	if _, err := h.store.SetChatWorktree(t.Context(), id, "", ""); err != nil {
		t.Fatal(err)
	}
	code, body := h.handoff(t, id, map[string]any{"title": "x"})
	if code != http.StatusConflict {
		t.Fatalf("handoff of a claimless chat = %d, want 409 (%v)", code, body)
	}
}

// TestHandoffDoesNotChangeTheOrphanCount is the ownership criterion: gc sees
// exactly one claim on the directory across the transfer, so a handed-off
// worktree is never reported as an orphan.
func TestHandoffDoesNotChangeTheOrphanCount(t *testing.T) {
	h := newChatHarness(t)
	id, _ := handoffFixture(t, h)
	before := h.orphans(t)
	if code, body := h.handoff(t, id, map[string]any{"title": "own it"}); code != http.StatusCreated {
		t.Fatalf("handoff = %d (%v)", code, body)
	}
	if after := h.orphans(t); after != before {
		t.Fatalf("orphan count went from %v to %v across the handoff", before, after)
	}
}

func (h *chatHarness) orphans(t *testing.T) float64 {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, "/v1/info", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/info = %d (%s)", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	n, _ := out["orphans"].(float64)
	return n
}

// TestHandoffFromAnIssue (task 130.7): `issue_id` goes through the shared
// prepareTaskCreate, so a handed-off task is linked and snapshotted exactly
// like a direct create, and a closed issue warns rather than refusing.
func TestHandoffFromAnIssue(t *testing.T) {
	h := newChatHarness(t)
	iss, err := h.store.CreateIssue(t.Context(), store.NewIssue{
		ProjectID: h.projectID, Title: "Carry the chat on", Body: "context",
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if _, err := h.store.TransitionIssue(t.Context(), iss.ID, issuestate.Close, issuestate.Completed, nil, issuestate.Human); err != nil {
		t.Fatalf("close: %v", err)
	}
	id, _ := handoffFixture(t, h)
	code, body := h.handoff(t, id, map[string]any{"issue_id": iss.ID})
	if code != http.StatusCreated {
		t.Fatalf("handoff = %d (%v)", code, body)
	}
	task, _ := body["task"].(map[string]any)
	if task["title"] != iss.Title {
		t.Errorf("title = %v, want the issue's", task["title"])
	}
	warnings, _ := task["warnings"].([]any)
	if len(warnings) == 0 || !strings.Contains(fmt.Sprint(warnings), "is closed") {
		t.Errorf("warnings = %v, want the closed-issue warning", task["warnings"])
	}
	stored, err := h.store.GetTask(t.Context(), int64(task["id"].(float64)))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.IssueID == nil || *stored.IssueID != iss.ID || stored.Issue == nil || stored.Issue.State != "closed" {
		t.Errorf("stored issue_id %v / snapshot %+v, want issue %d snapshotted closed", stored.IssueID, stored.Issue, iss.ID)
	}
}

// handoffIssue creates an open issue for the merge_back handoff tests.
func handoffIssue(t *testing.T, h *chatHarness) *store.Issue {
	t.Helper()
	iss, err := h.store.CreateIssue(t.Context(), store.NewIssue{ProjectID: h.projectID, Title: "Carry on"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return iss
}

// handoffRole is a handed-off task's issue_worktree and merge_back as the
// DTO renders them.
func handoffRole(t *testing.T, body map[string]any) (role string, mergeBack any) {
	t.Helper()
	task, _ := body["task"].(map[string]any)
	role, _ = task["issue_worktree"].(string)
	return role, task["merge_back"]
}

// TestHandoffMergeBackSelectsOnConflict is task 134.15: onto an issue that
// already has a main branch, a handoff is a side task whose merge_back is
// the body's on_conflict, and `block` when the body names none.
func TestHandoffMergeBackSelectsOnConflict(t *testing.T) {
	h := newChatHarness(t)
	iss := handoffIssue(t, h)
	first, _ := handoffFixture(t, h)
	if code, body := h.handoff(t, first, map[string]any{"issue_id": iss.ID}); code != http.StatusCreated {
		t.Fatalf("main handoff = %d (%v)", code, body)
	}
	for _, tc := range []struct {
		name      string
		mergeBack any
		want      string
	}{
		{"omitted", nil, store.MergeOnConflictBlock},
		{"empty", map[string]any{}, store.MergeOnConflictBlock},
		{"agent", map[string]any{"on_conflict": "agent"}, store.MergeOnConflictAgent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, _ := handoffFixture(t, h)
			req := map[string]any{"issue_id": iss.ID}
			if tc.mergeBack != nil {
				req["merge_back"] = tc.mergeBack
			}
			code, body := h.handoff(t, id, req)
			if code != http.StatusCreated {
				t.Fatalf("handoff = %d (%v)", code, body)
			}
			role, mb := handoffRole(t, body)
			got, _ := mb.(map[string]any)
			if role != store.IssueWorktreeSide || got["on_conflict"] != tc.want {
				t.Errorf("handoff = (%q, %v), want side/%s", role, mb, tc.want)
			}
		})
	}
}

// TestHandoffMergeBackIsInertWithoutAMainBranch is task 134 decision 7 as
// 134.15 settles it: a handoff onto an issue with no main branch takes the
// chat's branch as the main branch whatever merge_back says, and drops it.
func TestHandoffMergeBackIsInertWithoutAMainBranch(t *testing.T) {
	h := newChatHarness(t)
	iss := handoffIssue(t, h)
	id, chat := handoffFixture(t, h)
	code, body := h.handoff(t, id, map[string]any{
		"issue_id": iss.ID, "merge_back": map[string]any{"on_conflict": "agent"},
	})
	if code != http.StatusCreated {
		t.Fatalf("handoff = %d (%v)", code, body)
	}
	role, mb := handoffRole(t, body)
	if role != store.IssueWorktreeMain || mb != nil {
		t.Errorf("handoff = (%q, %v), want main with merge_back null", role, mb)
	}
	if iw, err := h.store.GetIssueMainWorktree(t.Context(), iss.ID); err != nil || iw.Branch != chat["branch"] {
		t.Errorf("main branch = %q (%v), want the chat's %v", iw.Branch, err, chat["branch"])
	}
}

// TestHandoffMergeBackRefusalsAreTheCreatePaths: the rules that still hold on
// a handoff are refused by POST /v1/tasks' own code, with its messages, and
// leave the chat as it was.
func TestHandoffMergeBackRefusalsAreTheCreatePaths(t *testing.T) {
	h := newChatHarness(t)
	iss := handoffIssue(t, h)
	id, chat := handoffFixture(t, h)
	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{
			"bad on_conflict",
			map[string]any{"issue_id": iss.ID, "merge_back": map[string]any{"on_conflict": "rebase"}},
			`merge_back.on_conflict must be one of: block, agent; got "rebase"`,
		},
		{
			"no issue_id",
			map[string]any{"title": "x", "merge_back": map[string]any{}},
			"merge_back requires issue_id: a side worktree is merged back into an issue's main branch",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := h.handoff(t, id, tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("handoff = %d (%v), want 400", code, body)
			}
			if msg := fmt.Sprint(body["error"]); !strings.Contains(msg, tc.want) {
				t.Errorf("error = %s, want %q", msg, tc.want)
			}
			c, err := h.store.GetChat(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if c.State != chatstate.Idle || c.WorktreePath != chat["worktree_path"] || c.HandoffTaskID != nil {
				t.Errorf("chat = (%s, %q, %v) after a refused handoff, want idle and untouched",
					c.State, c.WorktreePath, c.HandoffTaskID)
			}
		})
	}
}
