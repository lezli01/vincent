package taskrun

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

// fanOutBudget is how long a fan-out test waits. It is larger than the
// default because a fan-out is a chain of admissions — parent, then each lane,
// then the parent again — with a git worktree per lane and a merge at the
// end, paced by the scheduler's 5s safety tick.
const fanOutBudget = 150 * time.Second

// laneStepIndent shifts a commandStep block (written at the top level's two
// spaces) under a lane's `steps:`, which sits four levels in.
const laneStepIndent = "        "

// writeFileStep is a command step that writes one file, so a lane produces a
// commit the join has something to merge.
func writeFileStep(id, file, content string) string {
	return commandStep(id, script(
		fmt.Sprintf("printf '%s\\n' > %s && git add -A && git commit -m %s", content, file, id),
		fmt.Sprintf("Set-Content -Path %s -Value '%s'; git add -A; git commit -m %s", file, content, id),
	), "max_retries: 0")
}

// fanOutSnapshot builds a workflow whose only step fans out into inline
// lanes, each writing a file. The content is the lane id, so two lanes given
// the same filename produce a real conflict rather than an identical blob git
// merges without noticing.
func fanOutSnapshot(merge string, lanes ...[2]string) string {
	var sb strings.Builder
	sb.WriteString("name: root\nsteps:\n  - id: build\n    type: fan_out\n")
	if merge != "" {
		sb.WriteString(merge)
	}
	sb.WriteString("    lanes:\n")
	for _, lane := range lanes {
		fmt.Fprintf(&sb, "      - id: %s\n        steps:\n", lane[0])
		sb.WriteString(indent(writeFileStep("write-"+lane[0], lane[1], lane[0])))
	}
	return sb.String()
}

// waitForChildren polls until the parent has the expected number of lanes.
func (h *engineHarness) waitForChildren(t *testing.T, parentID int64, want int) []store.Task {
	t.Helper()
	deadline := time.Now().Add(fanOutBudget)
	for time.Now().Before(deadline) {
		kids, err := h.store.ListChildren(t.Context(), parentID)
		if err != nil {
			t.Fatalf("ListChildren: %v", err)
		}
		if len(kids) >= want {
			return kids
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %d never spawned %d lanes", parentID, want)
	return nil
}

// cancelRacingAdmission cancels a task, tolerating the one race any test that
// cancels a freshly spawned lane has.
//
// waitForChildren returns as soon as the lane rows exist, and a lane is
// `queued` for a moment before the scheduler admits it. Cancel reads the task,
// checks the action against the state it read, and then transitions with a
// compare-and-swap on that state — so an admission landing in between fails
// the swap with "task N is running, not queued", even though `cancel` is
// perfectly legal from `running` too. It failed about one CI run in ten.
//
// Retrying is the right fix *for the test*, which asserts nothing about that
// race: it wants the lane cancelled, from whichever state it has reached by
// then. Whether a human action that loses a CAS to the scheduler should retry
// itself is a §6 question about the product, and is deliberately not answered
// here.
func (h *engineHarness) cancelRacingAdmission(t *testing.T, id int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := h.runner.Cancel(t.Context(), id)
		if err == nil {
			return
		}
		if _, conflict := store.AsStateConflict(err); !conflict {
			t.Fatalf("cancel task %d: %v", id, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancel task %d: still losing the transition race after 10s: %v", id, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestFanOutSpawnsLanesAndMerges is the whole of phase 2 in one pass: the
// parent spawns real child tasks, parks without holding a slot, and — once
// they finish — merges both branches into its own.
func TestFanOutSpawnsLanesAndMerges(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, fanOutSnapshot("", [2]string{"api", "api.txt"}, [2]string{"docs", "docs.txt"}))

	lanes := h.waitForChildren(t, task.ID, 2)
	// A lane is an ordinary task carrying four extra columns (decision 1).
	for i, lane := range lanes {
		if lane.ParentTaskID == nil || *lane.ParentTaskID != task.ID {
			t.Errorf("lane %d has no parent link", lane.ID)
		}
		if lane.LaneOrder != i {
			t.Errorf("lane %q order = %d, want %d — declared order is the merge order", lane.LaneID, lane.LaneOrder, i)
		}
		if lane.BaseBranch != task.BranchName {
			t.Errorf("lane base_branch = %q, want the parent's branch %q", lane.BaseBranch, task.BranchName)
		}
		if lane.BranchName == task.BranchName {
			t.Errorf("lane %q shares the parent's branch; it needs its own", lane.LaneID)
		}
		if !strings.Contains(lane.Title, lane.LaneID) {
			t.Errorf("lane title %q does not name the lane", lane.Title)
		}
	}

	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	// One branch is delivered: both lanes' files are on the parent's branch.
	for _, file := range []string{"api.txt", "docs.txt"} {
		if !h.fileOnBranch(t, task.BranchName, file) {
			t.Errorf("%s is missing from the parent's branch after the join", file)
		}
	}
	// And the join is visible as a step, not an invisible git operation.
	var sawJoin bool
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID == "build" && run.State == store.StepSucceeded {
			sawJoin = true
		}
	}
	if !sawJoin {
		t.Error("the fan_out step recorded no successful step run")
	}
}

// TestFanOutParentHoldsNoSlotWhileWaiting is what makes deep fan-out
// deadlock-free: the parent releases its slot before its children need one.
// With a global cap of 1, a lane can only run if the parent gave up its slot.
func TestFanOutParentHoldsNoSlotWhileWaiting(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) { c.MaxParallelTasks = 1 })
	h.start(t)
	task := h.createTask(t, fanOutSnapshot("", [2]string{"api", "api.txt"}))

	// Under a cap of 1 this can only finish if the parked parent stopped
	// counting against it.
	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent state = %s (%q); a parked parent still held its slot",
			done.State, done.BlockReason)
	}
}

// TestFanOutBlocksOnMergeConflict: two lanes writing the same file conflict,
// and the default is to stop and leave the worktree conflicted for a human —
// not to guess (decision 8).
func TestFanOutBlocksOnMergeConflict(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, fanOutSnapshot("",
		[2]string{"api", "shared.txt"}, [2]string{"docs", "shared.txt"}))

	blocked := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("parent state = %s, want blocked on the conflict", blocked.State)
	}
	if blocked.BlockReason != ReasonMergeConflict {
		t.Fatalf("block_reason = %q, want %q", blocked.BlockReason, ReasonMergeConflict)
	}
	// The worktree is left conflicted on purpose: that is what a human
	// resolves in place.
	inMerge, err := h.runner.deps.Worktrees.InMerge(t.Context(), blocked.WorktreePath)
	if err != nil {
		t.Fatalf("InMerge: %v", err)
	}
	if !inMerge {
		t.Error("the conflicted merge was cleaned up; there is nothing left to resolve")
	}
}

// TestFanOutBlocksWhenALaneDidNotFinish is decision 21: a cancelled lane
// settles, but the join must not merge around it and deliver a branch missing
// that lane's work with nothing saying so.
func TestFanOutBlocksWhenALaneDidNotFinish(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	// The slow lane is cancelled while it runs; the quick one finishes.
	snapshot := "name: root\nsteps:\n  - id: build\n    type: fan_out\n    lanes:\n" +
		"      - id: quick\n        steps:\n" +
		indent(writeFileStep("write-quick", "quick.txt", "quick")) +
		"      - id: slow\n        steps:\n" +
		indent(commandStep("wait", sleepCmd(30), "max_retries: 0"))
	task := h.createTask(t, snapshot)

	lanes := h.waitForChildren(t, task.ID, 2)
	for _, lane := range lanes {
		if lane.LaneID == "slow" {
			h.cancelRacingAdmission(t, lane.ID)
		}
	}

	blocked := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("parent state = %s, want blocked", blocked.State)
	}
	if blocked.BlockReason != ReasonLaneFailed {
		t.Errorf("block_reason = %q, want %q", blocked.BlockReason, ReasonLaneFailed)
	}
	// Nothing was merged: a partial merge is indistinguishable downstream
	// from a complete one.
	if h.fileOnBranch(t, task.BranchName, "quick.txt") {
		t.Error("the successful lane was merged anyway; the branch now looks complete")
	}
}

// indent shifts a block of YAML right to sit under a lane's `steps:`, which
// is the only place these tests ever need it.
func indent(block string) string {
	var sb strings.Builder
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		sb.WriteString(laneStepIndent + line + "\n")
	}
	return sb.String()
}

// fileOnBranch reports whether a path exists in a branch's tip tree.
func (h *engineHarness) fileOnBranch(t *testing.T, branch, file string) bool {
	t.Helper()
	out, err := h.git().Run(t.Context(), h.repo, "ls-tree", "--name-only", branch, file)
	if err != nil {
		t.Fatalf("ls-tree %s %s: %v", branch, file, err)
	}
	return strings.TrimSpace(out) != ""
}

// git is a handle for reading the repository the test asserts against.
func (h *engineHarness) git() *gitx.Git { return gitx.New() }
