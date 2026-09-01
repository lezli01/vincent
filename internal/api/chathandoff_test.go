package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
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
