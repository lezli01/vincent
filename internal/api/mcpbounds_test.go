package api

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// boundsServer is a server with just what checkMCPBounds reads: a store, a
// config and a logger.
func boundsServer(t *testing.T, mutate func(*config.Config)) (*Server, *store.Store, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bounds.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &store.Project{Name: "p", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	srv := New(Deps{
		Token:  testToken,
		Store:  st,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: func() config.Config {
			c := config.Default()
			if mutate != nil {
				mutate(&c)
			}
			return c
		},
	})
	return srv, st, p.ID
}

func boundsTask(t *testing.T, st *store.Store, projectID int64, title string, createdBy *int64) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: projectID, Title: title,
		WorkflowName: "test", WorkflowSnapshot: "name: test\nsteps: []\n",
		BaseBranch: "main", BranchName: "vincent/" + title,
		State: store.TaskQueued, CreatedByTaskID: createdBy,
	}
	if err := st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask %s: %v", title, err)
	}
	return task
}

// TestMCPMaxDepthRefusesAtCreation: the chain a step's agent builds is
// discovered one insert at a time, so it is bounded where every other creation
// bound is — at creation, with a message naming the key it hit (task 055
// decision 7).
func TestMCPMaxDepthRefusesAtCreation(t *testing.T) {
	t.Parallel()
	srv, st, proj := boundsServer(t, func(c *config.Config) { c.MCP.MaxDepth = 2 })

	root := boundsTask(t, st, proj, "root", nil)
	// A first MCP-created task is depth 1 and allowed.
	if msg := srv.checkMCPBounds(t.Context(), root.ID); msg != "" {
		t.Fatalf("depth 1 refused: %s", msg)
	}
	mid := boundsTask(t, st, proj, "mid", &root.ID)
	// The next one would be depth 3 past a max_depth of 2.
	msg := srv.checkMCPBounds(t.Context(), mid.ID)
	if msg == "" {
		t.Fatal("a chain past mcp.max_depth was accepted")
	}
	if !strings.Contains(msg, "mcp.max_depth") {
		t.Errorf("refusal = %q, want it to name mcp.max_depth", msg)
	}
}

// TestMCPMaxTasksRefusesAtCreation is the count bound beside the depth bound:
// a shallow chain that is wide is the same runaway as a deep one.
func TestMCPMaxTasksRefusesAtCreation(t *testing.T) {
	t.Parallel()
	srv, st, proj := boundsServer(t, func(c *config.Config) {
		c.MCP.MaxDepth = 10
		c.MCP.MaxTasks = 3
	})

	root := boundsTask(t, st, proj, "root", nil)
	boundsTask(t, st, proj, "a", &root.ID)
	if msg := srv.checkMCPBounds(t.Context(), root.ID); msg != "" {
		t.Fatalf("a chain of 2 was refused under max_tasks 3: %s", msg)
	}
	boundsTask(t, st, proj, "b", &root.ID)
	msg := srv.checkMCPBounds(t.Context(), root.ID)
	if msg == "" {
		t.Fatal("a chain past mcp.max_tasks was accepted")
	}
	if !strings.Contains(msg, "mcp.max_tasks") {
		t.Errorf("refusal = %q, want it to name mcp.max_tasks", msg)
	}
}

// TestMCPBoundsIgnoreNonMCPTasks: a task nobody created over MCP is not part
// of any chain, so no bound applies to what it creates first.
func TestMCPBoundsIgnoreNonMCPTasks(t *testing.T) {
	t.Parallel()
	srv, st, proj := boundsServer(t, func(c *config.Config) {
		c.MCP.MaxDepth = 3
		c.MCP.MaxTasks = 32
	})
	// Twenty ordinary tasks — fan-out lanes, board creations, whatever — must
	// not consume an MCP chain's budget.
	for i := range 20 {
		boundsTask(t, st, proj, string(rune('a'+i)), nil)
	}
	root := boundsTask(t, st, proj, "root", nil)
	if msg := srv.checkMCPBounds(t.Context(), root.ID); msg != "" {
		t.Errorf("refused with only non-MCP tasks around: %s", msg)
	}
}
