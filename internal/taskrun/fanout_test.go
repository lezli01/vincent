package taskrun

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
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
//
// Every lane takes the default `on_conflict: block`; a test that wants
// `agent` writes its own `merge:` block, which is what the removed parameter
// never once carried.
func fanOutSnapshot(lanes ...[2]string) string {
	var sb strings.Builder
	sb.WriteString("name: root\nsteps:\n  - id: build\n    type: fan_out\n")
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

// TestFanOutSpawnsLanesAndMerges is the whole of phase 2 in one pass: the
// parent spawns real child tasks, parks without holding a slot, and — once
// they finish — merges both branches into its own.
func TestFanOutSpawnsLanesAndMerges(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, fanOutSnapshot([2]string{"api", "api.txt"}, [2]string{"docs", "docs.txt"}))

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
		// Provenance (task 043 decision 6): a lane's steps came from the
		// parent's snapshot, not from a registry file, so it records `derived`
		// naming the parent. Copying the parent's file and digest would claim
		// a source these steps never had; leaving it NULL would read as a task
		// created before origins were recorded.
		origin := lane.WorkflowOrigin
		if origin == nil || origin.Scope != store.WorkflowScopeDerived {
			t.Errorf("lane %q origin = %+v, want scope %q", lane.LaneID, origin, store.WorkflowScopeDerived)
			continue
		}
		if origin.ParentTaskID == nil || *origin.ParentTaskID != task.ID {
			t.Errorf("lane %q origin parent = %v, want %d", lane.LaneID, origin.ParentTaskID, task.ID)
		}
		if origin.File != "" || origin.Digest != "" {
			t.Errorf("lane %q origin claims a file or digest: %+v", lane.LaneID, origin)
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
	task := h.createTask(t, fanOutSnapshot([2]string{"api", "api.txt"}))

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
	task := h.createTask(t, fanOutSnapshot(
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
			if _, err := h.runner.Cancel(t.Context(), lane.ID); err != nil {
				t.Fatalf("cancel lane %d: %v", lane.ID, err)
			}
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

// TestFanOutParksWhenItsLanesHaveNotSettled: a parent admitted at a fan_out
// step whose lanes exist but are still running parks again instead of joining.
//
// The route is narrow and expensive. runFanOut decides "spawn or join" by
// whether this step has lanes, which is only the same question as "have the
// lanes finished" when the parent reliably parked after spawning them. It does
// not: the park is a transition that can lose its compare-and-swap or fail to
// commit, leaving a `running` parent with `queued` lanes for recovery to
// re-queue. Joining there reads every lane as "not done" and blocks
// lane_failed on work that is about to run perfectly well — and `retry` walks
// straight back into it, because the lanes are still not done.
//
// It is driven directly rather than through the scheduler because the state it
// needs is one no admission produces on purpose.
func TestFanOutParksWhenItsLanesHaveNotSettled(t *testing.T) {
	h := newEngineHarness(t)
	ctx := t.Context()

	snapshot := fanOutSnapshot([2]string{"api", "api.txt"}, [2]string{"docs", "docs.txt"})
	parent := h.createTask(t, snapshot)
	if _, _, err := h.store.TransitionTask(ctx, parent.ID,
		store.TaskQueued, store.TaskRunning, store.TaskChange{}); err != nil {
		t.Fatalf("put the parent in running: %v", err)
	}
	parent, err := h.store.GetTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	// Two lanes that exist and have not settled — exactly what a spawn
	// followed by a lost park leaves behind.
	index := 0
	for order, lane := range []string{"api", "docs"} {
		child := &store.Task{
			ProjectID: h.projectID, Title: "lane " + lane,
			WorkflowName: lane, WorkflowSnapshot: "name: " + lane + "\nsteps: []",
			BaseBranch: parent.BranchName, BranchName: "vincent/lane-" + lane,
			State:        store.TaskQueued,
			ParentTaskID: &parent.ID, ParentStepIndex: &index,
			LaneID: lane, LaneOrder: order,
		}
		if err := h.store.CreateTask(ctx, child, nil); err != nil {
			t.Fatalf("create lane %s: %v", lane, err)
		}
	}

	env := h.firstStepEnv(t, parent, snapshot)
	outcome, stop := h.runner.runFanOut(ctx, env)
	if !stop {
		t.Fatalf("runFanOut stop = false, want true — the parent must park, not fall through")
	}
	if outcome.state != "" {
		t.Errorf("outcome state = %q, want empty — parking is not a step outcome", outcome.state)
	}

	got, err := h.store.GetTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != store.TaskAwaitingChildren {
		t.Errorf("parent state = %s, want %s", got.State, store.TaskAwaitingChildren)
	}
	if got.BlockReason != "" {
		t.Errorf("block_reason = %q, want empty — nothing failed here", got.BlockReason)
	}
	for _, run := range h.stepRuns(t, parent.ID) {
		if run.FailureReason == ReasonLaneFailed {
			t.Errorf("the join ran and blocked %q against lanes that had not started", ReasonLaneFailed)
		}
	}
}

// firstStepEnv is the stepEnv the engine would build for the first step of
// snapshot, for a test that drives one step directly.
func (h *engineHarness) firstStepEnv(t *testing.T, task *store.Task, snapshot string) *stepEnv {
	t.Helper()
	wf, _, err := workflow.Parse([]byte(snapshot), workflow.Options{})
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	project, err := h.store.GetProject(t.Context(), task.ProjectID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	return &stepEnv{
		task: task, project: project, wf: wf,
		step: wf.Steps[0], index: 0,
		log: slog.New(slog.DiscardHandler),
	}
}
