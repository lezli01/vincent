package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// fullRefresh sets every field of both halves, so a round trip that dropped
// or swapped one would show.
func fullRefresh() *BaseRefresh {
	return &BaseRefresh{
		Fetch: BaseFetch{Remote: "origin", Ref: "refs/heads/main", Result: "error", Error: "fetch failed"},
		FastForward: BaseFastForward{
			Result: "skipped", Reason: "checkout_dirty", Worktree: "/repo/main", Error: "dirty",
		},
	}
}

func assertRefresh(t *testing.T, what string, got, want *BaseRefresh) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s base refresh = %+v, want nil", what, *got)
	case want != nil && got == nil:
		t.Errorf("%s base refresh = nil, want %+v", what, *want)
	case want != nil && *got != *want:
		t.Errorf("%s base refresh = %+v, want %+v", what, *got, *want)
	}
}

func TestClaimTaskWorktreeRoundTripsBaseRefresh(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	want := fullRefresh()
	if err := s.ClaimTaskWorktree(ctx, task.ID, "/wt/1", "abc123", want); err != nil {
		t.Fatalf("ClaimTaskWorktree: %v", err)
	}
	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.WorktreePath != "/wt/1" || got.BaseSHA != "abc123" {
		t.Errorf("worktree/base = %q/%q, want /wt/1/abc123", got.WorktreePath, got.BaseSHA)
	}
	assertRefresh(t, "GetTask", got.BaseRefresh, want)

	list, err := s.ListTasks(ctx, TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListTasks = %d rows, want 1", len(list))
	}
	assertRefresh(t, "ListTasks", list[0].BaseRefresh, want)
}

func TestClaimTaskWorktreeNilRefreshWritesNull(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	if err := s.ClaimTaskWorktree(ctx, task.ID, "/wt/1", "abc123", fullRefresh()); err != nil {
		t.Fatalf("ClaimTaskWorktree: %v", err)
	}
	if err := s.ClaimTaskWorktree(ctx, task.ID, "/wt/1", "", nil); err != nil {
		t.Fatalf("ClaimTaskWorktree(nil): %v", err)
	}
	var refresh, baseSHA sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT base_refresh, base_sha FROM tasks WHERE id = ?`, task.ID).Scan(&refresh, &baseSHA); err != nil {
		t.Fatalf("read columns: %v", err)
	}
	if refresh.Valid || baseSHA.Valid {
		t.Errorf("base_refresh = %+v, base_sha = %+v; want both NULL", refresh, baseSHA)
	}
	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	assertRefresh(t, "GetTask", got.BaseRefresh, nil)
}

func TestClaimTaskWorktreeMissingTask(t *testing.T) {
	s := openTest(t)
	err := s.ClaimTaskWorktree(t.Context(), 999, "/wt/999", "abc", fullRefresh())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ClaimTaskWorktree(missing) = %v, want ErrNotFound", err)
	}
}

// A task created with the record set — a chat handoff is the live case —
// keeps it, and a full-row UpdateTask writes it back rather than clearing it.
func TestBaseRefreshSurvivesInsertAndUpdateTask(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	want := fullRefresh()

	task := newTask(p.ID, "t", TaskQueued)
	task.BaseRefresh = want
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	assertRefresh(t, "after insert", got.BaseRefresh, want)

	got.Title = "renamed"
	if err := s.UpdateTask(ctx, got); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	again, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if again.Title != "renamed" {
		t.Errorf("title = %q, want renamed", again.Title)
	}
	assertRefresh(t, "after UpdateTask", again.BaseRefresh, want)
}

func TestClaimChatWorktreeRoundTripsBaseRefresh(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	want := fullRefresh()

	out, err := s.ClaimChatWorktree(ctx, c.ID, "/wt/chat-2", "def456", want)
	if err != nil {
		t.Fatalf("ClaimChatWorktree: %v", err)
	}
	assertRefresh(t, "returned chat", out.BaseRefresh, want)
	got, err := s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.WorktreePath != "/wt/chat-2" || got.BaseSHA != "def456" {
		t.Errorf("worktree/base = %q/%q, want /wt/chat-2/def456", got.WorktreePath, got.BaseSHA)
	}
	assertRefresh(t, "GetChat", got.BaseRefresh, want)

	// Clearing the claim is archive's write; the record is history and stays.
	cleared, err := s.SetChatWorktree(ctx, c.ID, "", "")
	if err != nil {
		t.Fatalf("SetChatWorktree: %v", err)
	}
	if cleared.WorktreePath != "" || cleared.BaseSHA != "def456" {
		t.Errorf("after clear worktree/base = %q/%q", cleared.WorktreePath, cleared.BaseSHA)
	}
	got, err = s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	assertRefresh(t, "after SetChatWorktree", got.BaseRefresh, want)
}

func TestCreateChatPersistsBaseRefresh(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	want := fullRefresh()
	c := &Chat{
		ProjectID: p.ID, Title: "a talk", Agent: "claude",
		Branch: "vincent/1-a-talk", BaseBranch: "main", PermissionMode: "full_auto",
		BaseRefresh: want,
	}
	if err := s.CreateChat(ctx, c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	got, err := s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	assertRefresh(t, "GetChat", got.BaseRefresh, want)
}

func TestHandoffChatCopiesBaseRefresh(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	want := fullRefresh()
	if _, err := s.ClaimChatWorktree(ctx, c.ID, c.WorktreePath, c.BaseSHA, want); err != nil {
		t.Fatalf("ClaimChatWorktree: %v", err)
	}

	task := newTask(p.ID, "carry on", TaskQueued)
	task.BranchName, task.BaseBranch, task.BaseSHA = c.Branch, c.BaseBranch, c.BaseSHA
	task.WorktreePath = c.WorktreePath
	after, err := s.HandoffChat(ctx, c.ID, task)
	if err != nil {
		t.Fatalf("HandoffChat: %v", err)
	}
	assertRefresh(t, "handed-off chat", after.BaseRefresh, want)
	stored, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	assertRefresh(t, "adopting task", stored.BaseRefresh, want)
	chat, err := s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	assertRefresh(t, "chat row after handoff", chat.BaseRefresh, want)
}

func TestBaseRefreshSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := t.Context()
	task := testTask(t, s)
	c := testChat(t, s, task.ProjectID)
	want := fullRefresh()
	if err := s.ClaimTaskWorktree(ctx, task.ID, "/wt/1", "abc123", want); err != nil {
		t.Fatalf("ClaimTaskWorktree: %v", err)
	}
	if _, err := s.ClaimChatWorktree(ctx, c.ID, "/wt/chat-1", "abc123", want); err != nil {
		t.Fatalf("ClaimChatWorktree: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	assertRefresh(t, "task after reopen", got.BaseRefresh, want)
	chat, err := s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	assertRefresh(t, "chat after reopen", chat.BaseRefresh, want)
}

// A row an 0030-era binary wrote has no record at all, and must read back as
// "nothing to show", not as an error or a zero-valued outcome (task 099).
func TestMigrate0031LeavesPreexistingRowsNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 30)

	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer func() { _ = db.Close() }()
		ts := formatTime(time.Now())
		if _, err := db.Exec(`INSERT INTO projects (id, name, path, default_branch, created_at, updated_at)
			VALUES (1, 'p1', '/p1', 'main', ?, ?)`, ts, ts); err != nil {
			t.Fatalf("seed project: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, workflow_name, workflow_snapshot,
				base_branch, branch_name, worktree_path, base_sha, state, created_at, updated_at)
			VALUES (1, 1, 't', 'adhoc', 'steps: []', 'main', 'vincent/1-t', '/wt/1', 'abc', 'running', ?, ?)`,
			ts, ts); err != nil {
			t.Fatalf("seed task: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO chats (id, project_id, title, state, agent, permission_mode,
				branch, base_branch, base_sha, worktree_path, created_at, updated_at)
			VALUES (1, 1, 'c', 'idle', 'claude', 'full_auto', 'vincent/1-c', 'main', 'abc', '/wt/c1', ?, ?)`,
			ts, ts); err != nil {
			t.Fatalf("seed chat: %v", err)
		}
	}()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating to 0031): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	task, err := s.GetTask(ctx, 1)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.BaseSHA != "abc" {
		t.Errorf("base_sha = %q, want abc", task.BaseSHA)
	}
	assertRefresh(t, "pre-0031 task", task.BaseRefresh, nil)
	chat, err := s.GetChat(ctx, 1)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	assertRefresh(t, "pre-0031 chat", chat.BaseRefresh, nil)
}

// A bad value is a lost line of history, never an unreadable task or chat.
func TestMalformedBaseRefreshDecodesToNil(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)
	c := testChat(t, s, task.ProjectID)
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET base_refresh = '{not json' WHERE id = ?`, task.ID); err != nil {
		t.Fatalf("corrupt task: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE chats SET base_refresh = '[1,' WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("corrupt chat: %v", err)
	}

	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	assertRefresh(t, "GetTask", got.BaseRefresh, nil)
	if _, err := s.ListTasks(ctx, TaskFilter{}); err != nil {
		t.Errorf("ListTasks: %v", err)
	}
	chat, err := s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	assertRefresh(t, "GetChat", chat.BaseRefresh, nil)
}
