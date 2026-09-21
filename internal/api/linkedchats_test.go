package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// linkedHarness is the task spine and the chat spine over one store, wired to
// each other the way daemon.Run wires them (task 119): both runners are real,
// and so are the worktrees the chats work in.
type linkedHarness struct {
	*taskHarness
	chats *chatrun.Runner
}

func newLinkedHarness(t *testing.T) *linkedHarness {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "linked.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	dataDir := t.TempDir()
	wt := worktree.NewManager(git, dataDir)
	reg := agent.NewRegistry(claude.New(func() string { return fake }), agenttest.StubNonResuming{})
	cfg := config.Default
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())

	var runner *taskrun.Runner
	chats := chatrun.New(chatrun.Deps{
		Store: st, Config: cfg, Worktrees: wt, Agents: reg, DataDir: dataDir, Logger: log,
		Launchers: func(ctx context.Context, taskID, turnID int64) (agent.Launcher, string, []string, error) {
			return runner.ChatLauncher(ctx, taskID, turnID)
		},
	})
	runner = taskrun.New(taskrun.Deps{
		Store: st, Config: cfg, Worktrees: wt, Agents: reg, DataDir: dataDir, Logger: log,
		ChatTurns: chats,
	})
	chats.Start(t.Context())
	runner.Start(t.Context())
	t.Cleanup(chats.Stop)
	t.Cleanup(runner.Stop)
	s := New(Deps{
		Token: testToken, Config: cfg, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st, Git: git, Worktrees: wt,
		Agents: reg, Catalog: agent.NewCatalogCache(reg), Runner: runner, Chats: chats,
		Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	h := &linkedHarness{
		taskHarness: &taskHarness{projectHarness: &projectHarness{ts: ts, store: st, wt: wt}, runner: runner},
		chats:       chats,
	}
	h.repo = testrepo.Init(t, "main")
	resp, body := h.doJSON(t, http.MethodPost, "/v1/projects", map[string]any{"path": h.repo})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register project: %d %s", resp.StatusCode, body)
	}
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("project body: %v", err)
	}
	h.projectID = p.ID
	return h
}

// blockedWithWorktree is a task blocked at step 0 with a real worktree on its
// own branch, the state a linked chat is opened on.
func (h *linkedHarness) blockedWithWorktree(t *testing.T) taskResponse {
	t.Helper()
	task := blockedTask(t, h.taskHarness, "check_failed")
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if _, err := h.wt.CreateAndClaim(t.Context(), h.repo, worktree.TaskOwner(task.ID),
		stored.BranchName, stored.BaseBranch, false, func(c worktree.Created) error {
			path := c.Path
			return h.store.SetTaskProgress(t.Context(), task.ID, nil, &path, nil)
		}); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	return task
}

func (h *linkedHarness) post(t *testing.T, path string, req any) (int, []byte) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPost, path, req)
	return resp.StatusCode, body
}

func (h *linkedHarness) openChat(t *testing.T, taskID int64) chatBody {
	t.Helper()
	code, body := h.post(t, fmt.Sprintf("/v1/tasks/%d/chat", taskID), map[string]any{})
	if code != http.StatusCreated {
		t.Fatalf("open chat: %d %s", code, body)
	}
	var c chatBody
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatalf("chat body: %v", err)
	}
	return c
}

func (h *linkedHarness) getTask(t *testing.T, id int64) taskResponse {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.StatusCode, body)
	}
	return decodeTask(t, body)
}

// waitIdle waits for a chat's turn to end and returns the conversation.
func (h *linkedHarness) waitIdle(t *testing.T, chatID int64, turns int) []chatTurnBody {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d", chatID), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get chat: %d %s", resp.StatusCode, body)
		}
		var got struct {
			Chat  chatBody       `json:"chat"`
			Turns []chatTurnBody `json:"turns"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("chat body: %v", err)
		}
		if got.Chat.State == string(chatstate.Idle) && len(got.Turns) == turns &&
			got.Turns[turns-1].State != string(chatstate.TurnRunning) {
			return got.Turns
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("chat %d never finished turn %d", chatID, turns)
	return nil
}

// TestLinkedChatLocksTheTaskUntilClosed is the lock end to end over the real
// handlers: every §6 action but cancel is refused with the lock's code while
// the chat is open, available_actions says so, and closing lifts it with the
// task exactly as it was.
func TestLinkedChatLocksTheTaskUntilClosed(t *testing.T) {
	h := newLinkedHarness(t)
	task := h.blockedWithWorktree(t)
	before, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(h.getTask(t, task.ID).AvailableActions, "chat") {
		t.Fatal("a blocked task with a worktree does not offer chat")
	}

	chat := h.openChat(t, task.ID)
	if chat.LinkedTaskID == nil || *chat.LinkedTaskID != task.ID {
		t.Fatalf("linked_task_id = %v, want %d", chat.LinkedTaskID, task.ID)
	}
	if chat.State != string(chatstate.Idle) || chat.WorktreePath != "" {
		t.Errorf("chat = %s at %q, want idle with no worktree of its own", chat.State, chat.WorktreePath)
	}
	if chat.Branch != before.BranchName {
		t.Errorf("chat branch = %q, want the task's %q", chat.Branch, before.BranchName)
	}

	locked := h.getTask(t, task.ID)
	if locked.OpenChatID == nil || *locked.OpenChatID != chat.ID {
		t.Errorf("open_chat_id = %v, want %d", locked.OpenChatID, chat.ID)
	}
	if got := locked.AvailableActions; len(got) != 1 || got[0] != "cancel" {
		t.Errorf("available_actions while locked = %v, want [cancel]", got)
	}

	for _, tc := range []struct {
		path string
		body any
	}{
		{"retry", nil},
		{"retry", map[string]any{"branch_override": "renamed-while-locked"}},
		{"skip", nil},
		{"repair", map[string]any{"prompt": "fix it"}},
		{"chat", map[string]any{}},
	} {
		code, body := h.post(t, fmt.Sprintf("/v1/tasks/%d/%s", task.ID, tc.path), tc.body)
		if code != http.StatusConflict {
			t.Errorf("%s while locked = %d %s, want 409", tc.path, code, body)
			continue
		}
		e := decodeError(t, body)
		if e.Code != CodeTaskLockedByChat || e.Details["chat_id"] != fmt.Sprint(chat.ID) {
			t.Errorf("%s refusal = %s %v, want %s naming chat %d", tc.path, e.Code, e.Details,
				CodeTaskLockedByChat, chat.ID)
		}
	}
	// The branch_override rename commits before the action, so it must have
	// been refused before it ran.
	if out, err := exec.Command("git", "-C", h.repo, "branch", "--list", "renamed-while-locked").Output(); err != nil {
		t.Fatal(err)
	} else if strings.TrimSpace(string(out)) != "" {
		t.Error("retry's branch_override renamed the branch of a locked task")
	}

	// Chat-side actions that would reach the task's worktree name the task.
	for _, path := range []string{"archive", "handoff"} {
		code, body := h.post(t, fmt.Sprintf("/v1/chats/%d/%s", chat.ID, path), map[string]any{"title": "x"})
		if code != http.StatusConflict || decodeError(t, body).Code != CodeChatLinkedToTask {
			t.Errorf("%s on a linked chat = %d %s, want 409 %s", path, code, body, CodeChatLinkedToTask)
		}
	}

	code, body := h.post(t, fmt.Sprintf("/v1/chats/%d/close", chat.ID), nil)
	if code != http.StatusOK {
		t.Fatalf("close: %d %s", code, body)
	}
	after, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != before.State || after.CurrentStep != before.CurrentStep ||
		after.BlockReason != before.BlockReason || after.WorktreePath != before.WorktreePath {
		t.Errorf("task after close = %s/%d/%s/%s, want %s/%d/%s/%s", after.State, after.CurrentStep,
			after.BlockReason, after.WorktreePath, before.State, before.CurrentStep, before.BlockReason,
			before.WorktreePath)
	}
	if _, err := os.Stat(before.WorktreePath); err != nil {
		t.Errorf("the task's worktree is gone after close: %v", err)
	}
	if unlocked := h.getTask(t, task.ID); unlocked.OpenChatID != nil || !hasAction(unlocked.AvailableActions, "retry") {
		t.Errorf("after close open_chat_id = %v, actions = %v", unlocked.OpenChatID, unlocked.AvailableActions)
	}
	// Deleting a closed linked chat's branch is refused: the copy is history.
	resp, dbody := h.doJSON(t, http.MethodDelete, fmt.Sprintf("/v1/chats/%d?delete_branch=true", chat.ID), nil)
	if resp.StatusCode != http.StatusConflict || decodeError(t, dbody).Code != CodeChatLinkedToTask {
		t.Errorf("delete_branch on a linked chat = %d %s", resp.StatusCode, dbody)
	}

	// A later stop can open a new chat, and both are the task's history.
	second := h.openChat(t, task.ID)
	resp, lbody := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats?task_id=%d&archived=all", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, lbody)
	}
	var list struct {
		Chats []chatBody `json:"chats"`
	}
	if err := json.Unmarshal(lbody, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Chats) != 2 || list.Chats[0].ID != second.ID || list.Chats[1].State != string(chatstate.Closed) {
		t.Errorf("task's chats = %+v, want the open one and the closed one", list.Chats)
	}
	// The default listing hides closed chats, as it hides every terminal one.
	resp, lbody = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats?task_id=%d", task.ID), nil)
	list.Chats = nil
	if err := json.Unmarshal(lbody, &list); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, lbody)
	}
	if len(list.Chats) != 1 {
		t.Errorf("default listing = %d chats, want the open one only", len(list.Chats))
	}
}

// TestLinkedChatTurnWorksInTheTaskWorktree: the turn runs in the task's
// worktree, its first prompt carries the opening context and its second does
// not.
func TestLinkedChatTurnWorksInTheTaskWorktree(t *testing.T) {
	h := newLinkedHarness(t)
	task := h.blockedWithWorktree(t)
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKEAGENT_EDIT_FILE", "README.md")
	chat := h.openChat(t, task.ID)
	if code, body := h.post(t, fmt.Sprintf("/v1/chats/%d/send", chat.ID),
		map[string]any{"message": "why did it fail?"}); code != http.StatusAccepted {
		t.Fatalf("send: %d %s", code, body)
	}
	turns := h.waitIdle(t, chat.ID, 1)
	if turns[0].State != string(chatstate.TurnDone) {
		t.Fatalf("turn 1 = %s (%s)", turns[0].State, turns[0].ErrorMessage)
	}
	out, err := exec.Command("git", "-C", stored.WorktreePath, "diff", "--name-only").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "README.md") {
		t.Errorf("task worktree diff = %q, want the chat's edit", out)
	}

	full, err := h.store.GetChat(t.Context(), chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<task id=", "block reason: check_failed", "<blocked-step"} {
		if !strings.Contains(full.OpeningContext, want) {
			t.Errorf("opening context lacks %q:\n%s", want, full.OpeningContext)
		}
	}
	if !strings.Contains(turns[0].ResultText, "<task id=") {
		t.Errorf("turn 1 was not sent the opening context: %q", turns[0].ResultText)
	}
	if code, body := h.post(t, fmt.Sprintf("/v1/chats/%d/send", chat.ID),
		map[string]any{"message": "and now?"}); code != http.StatusAccepted {
		t.Fatalf("send 2: %d %s", code, body)
	}
	turns = h.waitIdle(t, chat.ID, 2)
	if strings.Contains(turns[1].ResultText, "<task id=") {
		t.Errorf("turn 2 was sent the opening context again: %q", turns[1].ResultText)
	}
}

// TestCancelOnALockedTaskClosesTheChat: cancel is the one action a lock
// leaves, and it closes the chat and aborts the task together.
func TestCancelOnALockedTaskClosesTheChat(t *testing.T) {
	h := newLinkedHarness(t)
	task := h.blockedWithWorktree(t)
	chat := h.openChat(t, task.ID)
	code, body := h.post(t, fmt.Sprintf("/v1/tasks/%d/cancel", task.ID), nil)
	if code != http.StatusOK {
		t.Fatalf("cancel: %d %s", code, body)
	}
	if got := decodeTask(t, body); got.State != string(store.TaskAborted) {
		t.Errorf("task = %s, want aborted", got.State)
	}
	c, err := h.store.GetChat(t.Context(), chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != chatstate.Closed {
		t.Errorf("chat = %s, want closed", c.State)
	}
}

// TestOpenChatRefusals: the wrong state, no worktree, and an adapter that
// cannot resume are each refused before anything is written.
func TestOpenChatRefusals(t *testing.T) {
	h := newLinkedHarness(t)
	queued := queuedTask(t, h.taskHarness)
	code, body := h.post(t, fmt.Sprintf("/v1/tasks/%d/chat", queued.ID), map[string]any{})
	if code != http.StatusConflict || decodeError(t, body).Details["state"] != "queued" {
		t.Errorf("open from queued = %d %s, want 409 naming the state", code, body)
	}

	noTree := blockedTask(t, h.taskHarness, "branch_exists")
	code, body = h.post(t, fmt.Sprintf("/v1/tasks/%d/chat", noTree.ID), map[string]any{})
	if code != http.StatusConflict || decodeError(t, body).Code != CodeTaskHasNoWorktree {
		t.Errorf("open with no worktree = %d %s, want 409 %s", code, body, CodeTaskHasNoWorktree)
	}

	task := h.blockedWithWorktree(t)
	code, body = h.post(t, fmt.Sprintf("/v1/tasks/%d/chat", task.ID),
		map[string]any{"agent": agenttest.StubNonResuming{}.Name()})
	if code != http.StatusBadRequest || decodeError(t, body).Code != CodeAgentCannotResume {
		t.Errorf("open on a non-resuming adapter = %d %s, want 400 %s", code, body, CodeAgentCannotResume)
	}
	if id, err := h.store.OpenLinkedChatID(t.Context(), task.ID); err != nil || id != 0 {
		t.Errorf("a refused open left chat %d (%v)", id, err)
	}
}
