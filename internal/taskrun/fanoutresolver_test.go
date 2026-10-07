package taskrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// conflictingResolverSnapshot is two lanes writing different content to the
// same file, joined under `on_conflict: agent` with a resolver that does not
// retry: whatever its one attempt leaves is what the join judges.
func conflictingResolverSnapshot() string {
	return fanOutSnapshot([2]string{"api", "shared.txt"}, [2]string{"docs", "shared.txt"}) +
		`    merge:
      on_conflict: agent
      agent:
        id: resolve
        prompt: "Resolve the merge conflict in: {{ range .Conflicts }}{{.}} {{ end }}"
        max_retries: 0
`
}

// TestFanOutAgentResolverResolves: the resolver rewrites the conflicted file,
// the join commits the merge it was handed — with the lane's own message, so
// `diff?by=lane` still attributes it — and the parent finishes.
func TestFanOutAgentResolverResolves(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompts.jsonl")
	t.Setenv("FAKEAGENT_SCENARIO", "echo-prompt")
	t.Setenv("FAKEAGENT_PROMPT_FILE", promptFile)
	t.Setenv("FAKEAGENT_WRITE_FILE", "shared.txt")
	t.Setenv("FAKEAGENT_WRITE_CONTENT", "resolved\n")
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, conflictingResolverSnapshot())

	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	content, err := h.git().Run(t.Context(), h.repo, "show", task.BranchName+":shared.txt")
	if err != nil {
		t.Fatalf("show shared.txt: %v", err)
	}
	if strings.TrimSpace(content) != "resolved" {
		t.Errorf("shared.txt on the parent's branch = %q, want the resolver's content", content)
	}
	subjects, err := h.git().Run(t.Context(), h.repo, "log", "--format=%s", task.BranchName)
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	for _, lane := range []string{"api", "docs"} {
		want := "Merge lane '" + lane + "' of task "
		if !strings.Contains(subjects, want) {
			t.Errorf("no %q merge on the parent's branch; diff?by=lane cannot attribute it:\n%s", want, subjects)
		}
	}

	var sawResolver bool
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID == "resolve" && run.State == store.StepSucceeded {
			sawResolver = true
		}
	}
	if !sawResolver {
		t.Error("the resolver recorded no successful step run")
	}
	prompts, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("read the resolver's prompt: %v", err)
	}
	if !strings.Contains(string(prompts), "Resolve the merge conflict in: shared.txt") {
		t.Errorf("{{.Conflicts}} did not reach the resolver's prompt: %s", prompts)
	}
}

// TestFanOutAgentResolverFailsBlocks: a resolver that fails leaves the
// conflict for a human, exactly as `on_conflict: block` would.
func TestFanOutAgentResolverFailsBlocks(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "nonzero-exit")
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, conflictingResolverSnapshot())

	blocked := h.assertResolverBlocked(t, task)
	var sawFailed bool
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID == "resolve" && run.State == store.StepFailed {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Errorf("no failed resolver row for task %d", blocked.ID)
	}
}

// TestFanOutAgentResolverLeavingMarkersBlocks is #756: a resolver that
// succeeds without resolving must not get its conflict markers committed into
// the parent's branch. Staging clears the index's unmerged entries whatever
// the files hold, so only their content can say so.
func TestFanOutAgentResolverLeavingMarkersBlocks(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	t.Setenv("FAKEAGENT_EDIT_FILE", "shared.txt") // touches the file, resolves nothing
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, conflictingResolverSnapshot())

	blocked := h.assertResolverBlocked(t, task)
	// Nothing was committed: HEAD is still the first lane's merge.
	head, err := h.git().Run(t.Context(), blocked.WorktreePath, "log", "-1", "--format=%s")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(head), "Merge lane 'api' of task ") {
		t.Errorf("HEAD = %q, want the api lane's merge — the conflicted one was committed", head)
	}
	data, err := os.ReadFile(filepath.Join(blocked.WorktreePath, "shared.txt"))
	if err != nil {
		t.Fatalf("read shared.txt: %v", err)
	}
	if !strings.Contains(string(data), "<<<<<<< ") || !strings.Contains(string(data), fakeAgentMark) {
		t.Errorf("shared.txt = %q, want the resolver's edit beside the surviving markers", data)
	}
}

// assertResolverBlocked waits for the parent to block on the conflict, with
// the worktree still mid-merge and the build step's row failed for it.
//
// The block's message — `lane "docs" (task N) conflicts in:` and the paths —
// is not asserted here because the join's stepOutcome.output reaches no row
// today; handleConflict's subject keeps it byte-identical by construction.
func (h *engineHarness) assertResolverBlocked(t *testing.T, task *store.Task) *store.Task {
	t.Helper()
	blocked := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonMergeConflict {
		t.Fatalf("parent = %s/%q, want blocked/%s", blocked.State, blocked.BlockReason, ReasonMergeConflict)
	}
	inMerge, err := h.runner.deps.Worktrees.InMerge(t.Context(), blocked.WorktreePath)
	if err != nil {
		t.Fatalf("InMerge: %v", err)
	}
	if !inMerge {
		t.Error("the conflicted merge was cleaned up; there is nothing left to resolve")
	}
	var sawJoin bool
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID == "build" && run.State == store.StepFailed && run.FailureReason == ReasonMergeConflict {
			sawJoin = true
		}
	}
	if !sawJoin {
		t.Errorf("no build row failed with %s", ReasonMergeConflict)
	}
	paths, err := h.runner.deps.Worktrees.ConflictedPaths(t.Context(), blocked.WorktreePath)
	if err != nil || len(paths) != 1 || paths[0] != "shared.txt" {
		t.Errorf("conflicted paths = %v, %v; want shared.txt left for a human", paths, err)
	}
	return blocked
}
