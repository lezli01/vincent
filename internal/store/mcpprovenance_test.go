package store

import (
	"path/filepath"
	"testing"
)

// mcpStore opens a throwaway store with one project.
func mcpStore(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p := &Project{Name: "p", Path: t.TempDir(), DefaultBranch: "main"}
	if err := s.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return s, p.ID
}

func mcpTask(t *testing.T, s *Store, projectID int64, title string, createdBy, parent *int64) *Task {
	t.Helper()
	task := &Task{
		ProjectID: projectID, Title: title,
		WorkflowName: "test", WorkflowSnapshot: "name: test\nsteps: []\n",
		BaseBranch: "main", BranchName: "vincent/" + title,
		State: TaskQueued, CreatedByTaskID: createdBy, ParentTaskID: parent,
	}
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask %s: %v", title, err)
	}
	return task
}

// TestMCPAncestryWalksTheCreationChain is what `mcp.max_depth` is enforced
// with (task 057 decision 7): the chain is discovered one insert at a time,
// so there is no snapshot to check it against.
func TestMCPAncestryWalksTheCreationChain(t *testing.T) {
	t.Parallel()
	s, proj := mcpStore(t)
	root := mcpTask(t, s, proj, "root", nil, nil)
	mid := mcpTask(t, s, proj, "mid", &root.ID, nil)
	leaf := mcpTask(t, s, proj, "leaf", &mid.ID, nil)

	got, err := s.MCPAncestry(t.Context(), leaf.ID, 10)
	if err != nil {
		t.Fatalf("MCPAncestry: %v", err)
	}
	if len(got) != 2 || got[0] != mid.ID || got[1] != root.ID {
		t.Errorf("ancestry = %v, want [%d %d] nearest creator first", got, mid.ID, root.ID)
	}
	if n, err := s.MCPChainSize(t.Context(), root.ID); err != nil || n != 3 {
		t.Errorf("chain size = %d, %v; want 3", n, err)
	}
	// A task nobody created over MCP has no ancestry and is a chain of one.
	if got, err := s.MCPAncestry(t.Context(), root.ID, 10); err != nil || len(got) != 0 {
		t.Errorf("ancestry of a root = %v, %v; want empty", got, err)
	}
}

// TestMCPProvenanceRoundTrips: the column is read back exactly, and NULL stays
// nil rather than becoming zero — "not created through MCP" and "created by
// task 0" are different claims.
func TestMCPProvenanceRoundTrips(t *testing.T) {
	t.Parallel()
	s, proj := mcpStore(t)
	creator := mcpTask(t, s, proj, "creator", nil, nil)
	made := mcpTask(t, s, proj, "made", &creator.ID, nil)

	got, err := s.GetTask(t.Context(), made.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.CreatedByTaskID == nil || *got.CreatedByTaskID != creator.ID {
		t.Errorf("created_by_task_id = %v, want %d", got.CreatedByTaskID, creator.ID)
	}
	plain, err := s.GetTask(t.Context(), creator.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if plain.CreatedByTaskID != nil {
		t.Errorf("created_by_task_id = %v on a task nobody created over MCP, want nil",
			plain.CreatedByTaskID)
	}
}

// TestMCPProvenanceDoesNotDisturbFanOut is the reason `created_by_task_id` is
// not `parent_task_id` (decision 7). A task an agent created over MCP must not
// be counted as a lane, must not join its creator's `awaiting_children` wait,
// and must not appear as a child under ChildrenExclude — or a live `fan_out`
// step would wait on work it never spawned.
func TestMCPProvenanceDoesNotDisturbFanOut(t *testing.T) {
	t.Parallel()
	s, proj := mcpStore(t)
	parent := mcpTask(t, s, proj, "parent", nil, nil)
	lane := mcpTask(t, s, proj, "lane", nil, &parent.ID)
	viaMCP := mcpTask(t, s, proj, "via-mcp", &parent.ID, nil)

	rollup, err := s.ChildrenOf(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	if rollup.Total != 1 {
		t.Errorf("children total = %d, want 1 — only the fan_out lane is a child", rollup.Total)
	}
	children, err := s.ListChildren(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(children) != 1 || children[0].ID != lane.ID {
		t.Errorf("children = %+v, want only lane %d", children, lane.ID)
	}

	// ChildrenExclude is the board's default: it hides lanes, and must still
	// show a task an agent created over MCP as a root of its own.
	roots, err := s.ListTasks(t.Context(), TaskFilter{Children: ChildrenExclude})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	seen := map[int64]bool{}
	for _, r := range roots {
		seen[r.ID] = true
	}
	if seen[lane.ID] {
		t.Error("a fan_out lane appeared under ChildrenExclude")
	}
	if !seen[viaMCP.ID] {
		t.Error("an MCP-created task was hidden as though it were a fan_out lane")
	}
}
