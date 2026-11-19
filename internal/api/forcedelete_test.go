package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// brokenWorktree makes a directory under the manager's root whose `.git`
// points at nothing: git refuses every question about it, which is the
// archive failure no retry fixes that task 153 exists for.
func brokenWorktree(t *testing.T, h *taskHarness, name string) string {
	t.Helper()
	dir := filepath.Join(h.wt.Root(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitdir := filepath.Join(t.TempDir(), "gone", "worktrees", name)
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// doneTaskIn leaves a task done with its worktree_path set to dir.
func doneTaskIn(t *testing.T, h *taskHarness, dir string) taskResponse {
	t.Helper()
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskRunning)
	if _, _, err := h.store.TransitionTask(t.Context(), task.ID, store.TaskRunning, store.TaskDone,
		store.TaskChange{WorktreePath: &dir}); err != nil {
		t.Fatalf("done: %v", err)
	}
	return task
}

// TestForceDeleteTaskWhoseArchiveGitRefuses is the reported case: archive
// fails on a git error a human cannot resolve from vincent, and the force
// delete removes the task — and the directory — anyway.
func TestForceDeleteTaskWhoseArchiveGitRefuses(t *testing.T) {
	h := newActionHarness(t)
	dir := brokenWorktree(t, h, "task-broken")
	task := doneTaskIn(t, h, dir)
	path := "/v1/tasks/" + itoa(task.ID)

	resp, body := h.doJSON(t, http.MethodPost, path+"/archive", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("archive over a broken worktree: %d %s, want 409", resp.StatusCode, body)
	}
	if e := decodeError(t, body); e.Details["reason"] != "git_error" {
		t.Fatalf("archive refusal details %v, want reason git_error", e.Details)
	}

	resp, body = h.doJSON(t, http.MethodDelete, path+"?force=true", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("force delete: %d %s", resp.StatusCode, body)
	}
	if got := decodeDelete(t, body); !got.Deleted || !got.Forced {
		t.Fatalf("response %s does not report a forced delete", body)
	}
	if resp, _ := h.doJSON(t, http.MethodGet, path, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after force delete: %d, want 404", resp.StatusCode)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the broken worktree is still there: %v", err)
	}
}

// TestForceDeleteTaskLeavesTheMainCheckoutAlone: a task that ran in the
// project's own checkout (task 125) is removed, and the checkout is not.
func TestForceDeleteTaskLeavesTheMainCheckoutAlone(t *testing.T) {
	h := newActionHarness(t)
	task := doneTaskIn(t, h, h.repo)
	resp, body := h.doJSON(t, http.MethodDelete, "/v1/tasks/"+itoa(task.ID)+"?force=true", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("force delete: %d %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(h.repo, ".git")); err != nil {
		t.Fatalf("the project checkout was touched: %v", err)
	}
}

func TestForceDeleteTaskRefusesALiveProcess(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskRunning)

	resp, body := h.doJSON(t, http.MethodDelete, "/v1/tasks/"+itoa(task.ID)+"?force=true", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("force delete of a running task: %d %s, want 409", resp.StatusCode, body)
	}
	if e := decodeError(t, body); e.Details["reason"] != store.DeleteRefusedLive {
		t.Fatalf("details %v, want reason %q", e.Details, store.DeleteRefusedLive)
	}
	if resp, _ := h.doJSON(t, http.MethodGet, "/v1/tasks/"+itoa(task.ID), nil); resp.StatusCode != http.StatusOK {
		t.Fatal("the running task was deleted anyway")
	}
}

func TestForceDeleteRejectsDeleteBranch(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	for _, q := range []string{"?force=true&delete_branch=true", "?force=maybe"} {
		resp, body := h.doJSON(t, http.MethodDelete, "/v1/tasks/"+itoa(task.ID)+q, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d %s, want 400", q, resp.StatusCode, body)
		}
	}
}

// TestForceDeleteIssueTakesItsHolders: an issue held by an unsettled main
// task is a 409 on the ordinary delete and gone, with the task, on the
// forced one.
func TestForceDeleteIssueTakesItsHolders(t *testing.T) {
	h := newActionHarness(t)
	iss := h.issue(t)
	main := h.createTask(t, map[string]any{"issue_id": iss.ID})
	path := fmt.Sprintf("/v1/issues/%d", iss.ID)

	if resp, body := h.doJSON(t, http.MethodDelete, path, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("plain delete of a held issue: %d %s, want 409", resp.StatusCode, body)
	}
	resp, body := h.doJSON(t, http.MethodDelete, path+"?force=true", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("force delete: %d %s", resp.StatusCode, body)
	}
	var out forceDeleteIssueResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if !out.Deleted || len(out.TaskIDs) != 1 || out.TaskIDs[0] != main.ID {
		t.Fatalf("response %+v, want the issue and task %d deleted", out, main.ID)
	}
	if resp, _ := h.doJSON(t, http.MethodGet, path, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET issue after force delete: %d, want 404", resp.StatusCode)
	}
	if resp, _ := h.doJSON(t, http.MethodGet, "/v1/tasks/"+itoa(main.ID), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET main task after force delete: %d, want 404", resp.StatusCode)
	}
}

// TestForceDeleteIssueRefusesALiveHolderWhole: a holder with a live process
// refuses the whole call, before anything is removed.
func TestForceDeleteIssueRefusesALiveHolderWhole(t *testing.T) {
	h := newActionHarness(t)
	iss := h.issue(t)
	main := h.createTask(t, map[string]any{"issue_id": iss.ID})
	setState(t, h, main.ID, store.TaskRunning)
	path := fmt.Sprintf("/v1/issues/%d", iss.ID)

	resp, body := h.doJSON(t, http.MethodDelete, path+"?force=true", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("force delete over a running main task: %d %s, want 409", resp.StatusCode, body)
	}
	if e := decodeError(t, body); e.Details["reason"] != store.DeleteRefusedLive {
		t.Fatalf("details %v, want reason %q", e.Details, store.DeleteRefusedLive)
	}
	if resp, _ := h.doJSON(t, http.MethodGet, path, nil); resp.StatusCode != http.StatusOK {
		t.Fatal("the issue was deleted anyway")
	}
}

// TestForceDeleteChatRemovesAnIdleChat: a chat that was never archived goes,
// with its worktree.
func TestForceDeleteChatRemovesAnIdleChat(t *testing.T) {
	h := newChatHarness(t)
	status, chat := h.create(t, "")
	if status != http.StatusCreated {
		t.Fatalf("create chat: %d %v", status, chat)
	}
	id := int64(chat["id"].(float64))
	wt, _ := chat["worktree_path"].(string)
	path := fmt.Sprintf("/v1/chats/%d", id)

	resp, body := h.doJSON(t, http.MethodDelete, path, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("plain delete of an idle chat: %d %s, want 409", resp.StatusCode, body)
	}
	resp, body = h.doJSON(t, http.MethodDelete, path+"?force=true", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("force delete: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.doJSON(t, http.MethodGet, path, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after force delete: %d, want 404", resp.StatusCode)
	}
	if wt != "" {
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Fatalf("the chat's worktree %s is still there: %v", wt, err)
		}
	}
}

// TestChatArchiveRefusalNamesItsReason: the dirty chat's 409 carries the
// worktree's reason in details, which the TUI's force re-ask and its force
// delete offer both branch on; a force delete then removes the real
// worktree without git's consent to the dirty files.
func TestChatArchiveRefusalNamesItsReason(t *testing.T) {
	h := newChatHarness(t)
	id, chat := handoffFixture(t, h)
	dir, _ := chat["worktree_path"].(string)
	if dir == "" {
		t.Fatal("the chat has no worktree")
	}
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/v1/chats/%d", id)
	resp, body := h.doJSON(t, http.MethodPost, path+"/archive", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("archive of a dirty chat: %d %s, want 409", resp.StatusCode, body)
	}
	if e := decodeError(t, body); e.Details["reason"] != "worktree_dirty" {
		t.Fatalf("details %v, want reason worktree_dirty", e.Details)
	}
	resp, body = h.doJSON(t, http.MethodDelete, path+"?force=true", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("force delete: %d %s", resp.StatusCode, body)
	}
	if got := decodeDelete(t, body); len(got.Warnings) != 0 {
		t.Errorf("a clean removal left warnings: %v", got.Warnings)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the chat's worktree is still there: %v", err)
	}
	if out := gitOut(t, h.repo, "worktree", "list"); strings.Contains(out, dir) {
		t.Fatalf("git still lists the removed worktree:\n%s", out)
	}
}
