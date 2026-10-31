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
