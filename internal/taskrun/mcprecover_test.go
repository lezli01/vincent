package taskrun

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/store"
)

// TestRecoverSweepsLeftoverCursorMCPConfig covers §12.4's task-057 clause: a
// cursor step writes `.cursor/mcp.json` into the task worktree and removes it
// in Wait, so a daemon that died mid-step leaves it behind — untracked, inside
// a git worktree, visible to `git status`, to the task diff and to dirty
// detection on a task that is about to be re-queued.
func TestRecoverSweepsLeftoverCursorMCPConfig(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()

	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	worktreePath := t.TempDir()
	task := &store.Task{
		ProjectID: project.ID, Title: "crashed",
		WorkflowName: "test", WorkflowSnapshot: "name: test\nsteps: []\n",
		BaseBranch: "main", BranchName: "vincent/crashed",
		WorktreePath: worktreePath, State: store.TaskRunning,
	}
	if err := st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	srv := &agent.MCPServer{Name: "vincent", URL: "http://127.0.0.1:1/mcp/step/1", Token: "s3cret"}
	if err := agent.WriteCursorMCPConfig(worktreePath, srv); err != nil {
		t.Fatalf("WriteCursorMCPConfig: %v", err)
	}

	if _, err := Recover(ctx, st, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if _, err := os.Stat(agent.CursorMCPConfigPath(worktreePath)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat after recovery = %v, want the leftover config removed", err)
	}
}

// TestRemoveCursorMCPConfigKeepsANonEmptyDotCursor: the directory goes only
// when nothing else is in it. A `.cursor` the user or the agent put something
// else in is not vincent's to delete.
func TestRemoveCursorMCPConfigKeepsANonEmptyDotCursor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srv := &agent.MCPServer{Name: "vincent", URL: "http://127.0.0.1:1/mcp", Token: "s"}
	if err := agent.WriteCursorMCPConfig(dir, srv); err != nil {
		t.Fatalf("WriteCursorMCPConfig: %v", err)
	}
	keep := filepath.Join(dir, agent.CursorMCPDir, "rules.md")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatalf("write sibling: %v", err)
	}
	if err := agent.RemoveCursorMCPConfig(dir); err != nil {
		t.Fatalf("RemoveCursorMCPConfig: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("stat sibling = %v, want a non-empty .cursor left alone", err)
	}
}

// TestRemoveCursorMCPConfigIsIdempotent: recovery calls it on every live
// task's worktree, and the common case is that there was never one.
func TestRemoveCursorMCPConfigIsIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for range 2 {
		if err := agent.RemoveCursorMCPConfig(dir); err != nil {
			t.Fatalf("RemoveCursorMCPConfig on a clean worktree: %v", err)
		}
	}
}
