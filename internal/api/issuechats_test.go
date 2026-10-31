package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// issueChatView is what the issue-chat tests read off GET /v1/issues/{id}.
type issueChatView struct {
	Lane         string `json:"lane"`
	Active       bool   `json:"active"`
	MainWorktree *struct {
		Branch         string `json:"branch"`
		OccupantTaskID *int64 `json:"occupant_task_id"`
		OccupantChatID *int64 `json:"occupant_chat_id"`
	} `json:"main_worktree"`
}

func (h *chatHarness) issue(t *testing.T, id int64) issueChatView {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d", id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get issue: %d %s", resp.StatusCode, body)
	}
	var v issueChatView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("issue body: %v", err)
	}
	return v
}

func (h *chatHarness) issueChat(t *testing.T, issueID int64) (int, map[string]any) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPost, "/v1/chats", map[string]any{
		"project_id": h.projectID, "title": "work the issue", "agent": "claude", "issue_id": issueID,
	})
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// TestIssueChatHoldsTheMainWorktree is task 140 decisions 1–4, 6 and 10 end
// to end over the API: a chat opened on an issue with no work cuts the
// issue's main branch, moves it to in_progress and names itself the
// occupant; a second chat and the issue's delete are refused while it is
// live; closing it releases the worktree in place, and the next chat on the
// issue receives that same directory, uncommitted work included.
func TestIssueChatHoldsTheMainWorktree(t *testing.T) {
	h := newChatHarness(t)
	ctx := context.Background()
	iss, err := h.store.CreateIssue(ctx, store.NewIssue{
		ProjectID: h.projectID, Title: "Fix the frobnicator", Body: "It frobs twice.",
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if _, err := h.store.AddIssueComment(ctx, iss.ID, "ann", "seen on linux too", "", issuestate.Human); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if got := h.issue(t, iss.ID).Lane; got != "open" {
		t.Fatalf("lane before = %s, want open", got)
	}

	code, first := h.issueChat(t, iss.ID)
	if code != http.StatusCreated {
		t.Fatalf("issue chat: %d %v", code, first)
	}
	chatID := int64(first["id"].(float64))
	branch, _ := first["branch"].(string)
	path, _ := first["worktree_path"].(string)
	if branch == "" || path == "" || first["issue_id"] != float64(iss.ID) {
		t.Fatalf("issue chat body = %v", first)
	}
	v := h.issue(t, iss.ID)
	if v.Lane != "in_progress" || !v.Active {
		t.Errorf("lane = %s active = %v, want in_progress and active", v.Lane, v.Active)
	}
	if v.MainWorktree == nil || v.MainWorktree.Branch != branch ||
		v.MainWorktree.OccupantChatID == nil || *v.MainWorktree.OccupantChatID != chatID {
		t.Errorf("main_worktree = %+v, want branch %s held by chat %d", v.MainWorktree, branch, chatID)
	}
	stored, err := h.store.GetChat(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Fix the frobnicator", "It frobs twice.", "seen on linux too"} {
		if !strings.Contains(stored.OpeningContext, want) {
			t.Errorf("opening context lacks %q:\n%s", want, stored.OpeningContext)
		}
	}

	if code, body := h.issueChat(t, iss.ID); code != http.StatusConflict || errCode(body) != CodeIssueHasLiveChat {
		t.Errorf("second chat = %d %v, want 409 %s", code, body, CodeIssueHasLiveChat)
	}
	resp, body := h.doJSON(t, http.MethodDelete, fmt.Sprintf("/v1/issues/%d", iss.ID), nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), CodeIssueHasLiveChat) {
		t.Errorf("issue delete = %d %s, want 409 %s", resp.StatusCode, body, CodeIssueHasLiveChat)
	}

	if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("half done"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, body = h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/close", chatID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("close: %d %s", resp.StatusCode, body)
	}
	var closed map[string]any
	_ = json.Unmarshal(body, &closed)
	if closed["state"] != "closed" || closed["end_sha"] == nil || closed["worktree_path"] != path {
		t.Errorf("closed chat = %v, want closed, end_sha set, worktree kept", closed)
	}
	v = h.issue(t, iss.ID)
	if v.Lane != "in_progress" || v.Active || v.MainWorktree.OccupantChatID != nil {
		t.Errorf("after close: lane = %s active = %v occupant = %v; want in_progress, idle, free",
			v.Lane, v.Active, v.MainWorktree.OccupantChatID)
	}

	resp, body = h.doJSON(t, http.MethodDelete, fmt.Sprintf("/v1/chats/%d?delete_branch=true", chatID), nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), CodeIssueChatBranch) {
		t.Errorf("delete_branch = %d %s, want 409 %s", resp.StatusCode, body, CodeIssueChatBranch)
	}

	code, second := h.issueChat(t, iss.ID)
	if code != http.StatusCreated {
		t.Fatalf("next chat: %d %v", code, second)
	}
	if second["branch"] != branch || second["worktree_path"] != path {
		t.Errorf("next chat = branch %v path %v, want the issue's %s at %s",
			second["branch"], second["worktree_path"], branch, path)
	}
	if b, err := os.ReadFile(filepath.Join(path, "wip.txt")); err != nil || string(b) != "half done" {
		t.Errorf("uncommitted work after transfer = %q, %v", b, err)
	}
	prev, err := h.store.GetChat(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if prev.WorktreePath != "" || prev.EndSHA == "" {
		t.Errorf("predecessor chat path = %q end_sha = %q, want released with end_sha", prev.WorktreePath, prev.EndSHA)
	}
}

// TestIssueChatRefusesAnotherBranch is task 140 decision 1: a chat on an
// issue cannot name a branch the issue's main branch is not.
func TestIssueChatRefusesAnotherBranch(t *testing.T) {
	h := newChatHarness(t)
	iss, err := h.store.CreateIssue(context.Background(), store.NewIssue{
		ProjectID: h.projectID, Title: "x",
	}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	resp, body := h.doJSON(t, http.MethodPost, "/v1/chats", map[string]any{
		"project_id": h.projectID, "title": "t", "agent": "claude", "issue_id": iss.ID,
		"branch_name": "feature/elsewhere", "existing_branch": true,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("branch_name on an issue with no main branch = %d %s, want 400", resp.StatusCode, body)
	}
}

// newIssue creates an issue on the harness's project.
func (h *chatHarness) newIssue(t *testing.T, title string) *store.Issue {
	t.Helper()
	iss, err := h.store.CreateIssue(context.Background(), store.NewIssue{ProjectID: h.projectID, Title: title}, issuestate.Human)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	return iss
}

// openIssueChat opens a chat on issueID over the API and returns its id,
// branch and worktree path.
func (h *chatHarness) openIssueChat(t *testing.T, issueID int64) (id int64, branch, path string) {
	t.Helper()
	code, body := h.issueChat(t, issueID)
	if code != http.StatusCreated {
		t.Fatalf("issue chat: %d %v", code, body)
	}
	branch, _ = body["branch"].(string)
	path, _ = body["worktree_path"].(string)
	return int64(body["id"].(float64)), branch, path
}

// TestIssueChatHandoff is task 140 decision 7 (review F5): a handoff from an
// issue chat defaults issue_id to the chat's and is a main task on the
// chat's branch and worktree; another issue, or merge_back, is a 400.
func TestIssueChatHandoff(t *testing.T) {
	h := newChatHarness(t)
	iss := h.newIssue(t, "hand it on")
	other := h.newIssue(t, "elsewhere")
	chatID, branch, path := h.openIssueChat(t, iss.ID)

	if code, body := h.handoff(t, chatID, map[string]any{"issue_id": other.ID}); code != http.StatusBadRequest {
		t.Errorf("handoff to another issue = %d %v, want 400", code, body)
	}
	if code, body := h.handoff(t, chatID, map[string]any{"merge_back": map[string]any{}}); code != http.StatusBadRequest {
		t.Errorf("handoff with merge_back = %d %v, want 400", code, body)
	}
	wantMainTask := func(name string, req map[string]any, issueID int64, branch, path string) {
		t.Helper()
		code, body := h.handoff(t, chatID, req)
		if code != http.StatusCreated {
			t.Fatalf("%s: handoff = %d %v", name, code, body)
		}
		task, _ := body["task"].(map[string]any)
		got, err := h.store.GetTask(context.Background(), int64(task["id"].(float64)))
		if err != nil {
			t.Fatal(err)
		}
		if got.IssueID == nil || *got.IssueID != issueID || got.IssueWorktree != store.IssueWorktreeMain ||
			got.BranchName != branch || got.WorktreePath != path {
			t.Errorf("%s: task = issue %v role %q branch %q path %q; want main of %d on %s at %s",
				name, got.IssueID, got.IssueWorktree, got.BranchName, got.WorktreePath, issueID, branch, path)
		}
	}
	wantMainTask("naming the chat's own issue", map[string]any{"issue_id": iss.ID, "title": "explicit"}, iss.ID, branch, path)

	// Without issue_id the chat's issue is the default.
	second := h.newIssue(t, "default issue")
	chatID, branch, path = h.openIssueChat(t, second.ID)
	wantMainTask("without issue_id", map[string]any{"title": "carry on"}, second.ID, branch, path)
}

// TestIssueChatArchive is task 140 decision 4 (review F5): archiving an issue
// chat never deletes its branch, which stays the issue's main branch; the
// directory goes when no main task is waiting for it — the next chat revives
// it from the branch — and stays, with the chat's claim, when one is.
func TestIssueChatArchive(t *testing.T) {
	h := newChatHarness(t)
	ctx := context.Background()
	archive := func(id int64) map[string]any {
		t.Helper()
		resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/archive", id), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("archive: %d %s", resp.StatusCode, body)
		}
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		return out
	}

	iss := h.newIssue(t, "nothing waits")
	chatID, branch, path := h.openIssueChat(t, iss.ID)
	if err := os.WriteFile(filepath.Join(path, "kept.txt"), []byte("committed"), 0o600); err != nil {
		t.Fatal(err)
	}
	testrepo.Run(t, path, "add", "kept.txt")
	testrepo.Run(t, path, "commit", "-m", "chat work")
	out := archive(chatID)
	if out["state"] != "archived" || out["end_sha"] == nil || out["worktree_path"] != nil {
		t.Errorf("archived chat = %v, want archived, end_sha set, no worktree", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("directory after archive with nothing waiting: %v, want removed", err)
	}
	testrepo.Run(t, h.repo, "rev-parse", "--verify", "refs/heads/"+branch)
	if mw, err := h.store.GetIssueMainWorktree(ctx, iss.ID); err != nil || mw.Branch != branch {
		t.Errorf("main branch after archive = %q (%v), want %s", mw.Branch, err, branch)
	}
	_, again, revived := h.openIssueChat(t, iss.ID)
	if again != branch {
		t.Errorf("next chat branch = %q, want the issue's %s", again, branch)
	}
	if b, err := os.ReadFile(filepath.Join(revived, "kept.txt")); err != nil || string(b) != "committed" {
		t.Errorf("revived worktree lacks the archived chat's commit: %q, %v", b, err)
	}

	waiting := h.newIssue(t, "a main task waits")
	chatID, _, path = h.openIssueChat(t, waiting.ID)
	issueID := waiting.ID
	queued := &store.Task{
		ProjectID: h.projectID, Title: "next", WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", State: store.TaskQueued, IssueID: &issueID, IssueWorktree: store.IssueWorktreeMain,
	}
	if err := h.store.CreateTask(ctx, queued, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("half done"), 0o600); err != nil {
		t.Fatal(err)
	}
	out = archive(chatID)
	if out["state"] != "archived" || out["worktree_path"] != path {
		t.Errorf("archived chat with a waiting task = %v, want its worktree %s kept", out, path)
	}
	if b, err := os.ReadFile(filepath.Join(path, "wip.txt")); err != nil || string(b) != "half done" {
		t.Errorf("uncommitted work after archive = %q, %v", b, err)
	}
	if holder, err := h.store.IssueMainWorktreeChatHolder(ctx, waiting.ID); err != nil || holder == nil || holder.ID != chatID {
		t.Errorf("chat holder after archive = %v, %v; want chat %d", holder, err, chatID)
	}
}

// TestIssueChatPlacementRefusalsAreConflicts is review F7: a placement
// failure a person can fix is a 409 naming its reason, not a 500, and the
// refused chat leaves nothing behind.
func TestIssueChatPlacementRefusalsAreConflicts(t *testing.T) {
	h := newChatHarness(t)
	ctx := context.Background()
	wantConflict := func(name string, issueID int64, reason string) {
		t.Helper()
		code, body := h.issueChat(t, issueID)
		e, _ := body["error"].(map[string]any)
		details, _ := e["details"].(map[string]any)
		if code != http.StatusConflict || details["reason"] != reason {
			t.Errorf("%s: issue chat = %d %v, want 409 reason %s", name, code, body, reason)
		}
		if v := h.issue(t, issueID); v.MainWorktree == nil || v.MainWorktree.OccupantChatID != nil {
			t.Errorf("%s: refused chat still occupies the issue: %+v", name, v.MainWorktree)
		}
	}

	rebasing := h.newIssue(t, "rebase left over")
	chatID, _, path := h.openIssueChat(t, rebasing.ID)
	if resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/close", chatID), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("close: %d %s", resp.StatusCode, body)
	}
	gitDir := testrepo.Run(t, path, "rev-parse", "--absolute-git-dir")
	if err := os.MkdirAll(filepath.Join(filepath.FromSlash(gitDir), "rebase-merge"), 0o700); err != nil {
		t.Fatal(err)
	}
	wantConflict("half-finished rebase", rebasing.ID, "repo_operation_in_progress")
	if prev, err := h.store.GetChat(ctx, chatID); err != nil || prev.WorktreePath != path {
		t.Errorf("predecessor after the refusal = %+v, %v; want it to keep %s", prev, err, path)
	}

	checkedOut := h.newIssue(t, "branch in the main checkout")
	chatID, branch, _ := h.openIssueChat(t, checkedOut.ID)
	if resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/archive", chatID), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive: %d %s", resp.StatusCode, body)
	}
	testrepo.Run(t, h.repo, "checkout", branch)
	wantConflict("branch in the main checkout", checkedOut.ID, "issue_branch_checked_out")
}
