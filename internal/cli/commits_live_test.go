package cli

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// addCommitsTask records a task on dir's `feature` branch, in a project whose
// repository is dir — the commits route reads the project repository, not the
// worktree.
func (h *liveHarness) addCommitsTask(t *testing.T, dir string) string {
	t.Helper()
	p := &store.Project{Name: "commits", Path: dir, DefaultBranch: "main"}
	if err := h.st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task := &store.Task{
		ProjectID: p.ID, Title: "list the commits", WorkflowName: "adhoc",
		WorkflowSnapshot: "x", BaseBranch: "main", BranchName: "feature",
		State: store.TaskRunning, WorktreePath: dir,
	}
	if err := h.st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return strconv.FormatInt(task.ID, 10)
}

// One line per commit, oldest first, with the lane marked on a lane merge;
// --json is the wire list.
func TestTaskCommitsPrintsOneLinePerCommit(t *testing.T) {
	h := newLiveHarness(t)
	dir, mergeA, _ := laneRepo(t)
	id := h.addCommitsTask(t, dir)

	out, errOut, code := runCLI(t, "task", "commits", id)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(out, "\r\n", "\n"), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want the parent's commit and two lane merges:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "the parent's own work") || strings.Contains(lines[0], "[lane") {
		t.Errorf("line 0 = %q, want the parent's own commit, unmarked", lines[0])
	}
	if !strings.HasPrefix(lines[1], strings.TrimSpace(mergeA)[:12]) ||
		!strings.Contains(lines[1], "[lane a, task 41]") {
		t.Errorf("line 1 = %q, want lane a's merge, marked", lines[1])
	}
	if !strings.Contains(lines[2], "[lane b, task 42]") {
		t.Errorf("line 2 = %q, want lane b's merge, marked", lines[2])
	}

	out, _, code = runCLI(t, "task", "commits", id, "--json")
	if code != 0 {
		t.Fatalf("--json exit = %d, want 0", code)
	}
	var got []apiclient.Commit
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 3 {
		t.Fatalf("--json = %q, want three commits (%v)", out, err)
	}
	if got[2].LaneID != "b" || got[2].ChildTaskID != 42 {
		t.Errorf("--json lane merge = %+v", got[2])
	}
}

// A task with no branch yet is the daemon's 409, in its own words, exit 1.
func TestTaskCommitsWithoutABranch(t *testing.T) {
	h := newLiveHarness(t)
	task := &store.Task{
		ProjectID: h.projectID, Title: "never admitted", WorkflowName: "adhoc",
		WorkflowSnapshot: "x", BaseBranch: "main", BranchName: "later",
		State: store.TaskQueued,
	}
	if err := h.st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_, errOut, code := runCLI(t, "task", "commits", strconv.FormatInt(task.ID, 10))
	if code != 1 || !strings.Contains(errOut, "task has no branch yet") {
		t.Errorf("exit = %d, stderr = %q; want 1 and the daemon's message", code, errOut)
	}
}
