package taskrun

// Eager lane scheduling (spec §7.6, task 081).

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// sleepStep holds a lane open without needing a file on disk, in the sh∩pwsh
// intersection every workflow body is spelled in (§8.3).
func sleepStep(id string, seconds int) string {
	return commandStep(id, fmt.Sprintf("sleep %d", seconds), "max_retries: 0")
}

// eagerDAG is the shape the issue argues about, in four lanes:
//
//	slow      — nothing depends on it, and it holds the step open
//	quick     — finishes at once
//	dep       — needs quick
//	deeper    — needs dep
//
// Under a barrier `dep` cannot spawn until `slow` has settled too, because a
// round is a barrier over every lane. Under eager it spawns as soon as
// `quick` merges, and `deeper` behind it — which is the whole feature.
func eagerDAG(schedule string) string {
	var sb strings.Builder
	sb.WriteString("name: root\nsteps:\n  - id: build\n    type: fan_out\n")
	if schedule != "" {
		fmt.Fprintf(&sb, "    schedule: %s\n", schedule)
	}
	sb.WriteString("    lanes:\n")
	sb.WriteString("      - id: slow\n        steps:\n")
	sb.WriteString(indent(sleepStep("wait", 6)))
	sb.WriteString(indent(writeFileStep("write-slow", "slow.txt", "slow")))
	sb.WriteString("      - id: quick\n        steps:\n")
	sb.WriteString(indent(writeFileStep("write-quick", "quick.txt", "quick")))
	sb.WriteString("      - id: dep\n        needs: [quick]\n        steps:\n")
	sb.WriteString(indent(requireFileStep("see-quick", "quick.txt")))
	sb.WriteString(indent(writeFileStep("write-dep", "dep.txt", "dep")))
	sb.WriteString("      - id: deeper\n        needs: [dep]\n        steps:\n")
	sb.WriteString(indent(requireFileStep("see-dep", "dep.txt")))
	sb.WriteString(indent(writeFileStep("write-deeper", "deeper.txt", "deeper")))
	return sb.String()
}

// laneStates maps a parent's lanes to their current states.
func (h *engineHarness) laneStates(t *testing.T, parentID int64) map[string]store.TaskState {
	t.Helper()
	kids, err := h.store.ListChildren(t.Context(), parentID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	out := make(map[string]store.TaskState, len(kids))
	for _, kid := range kids {
		out[kid.LaneID] = kid.State
	}
	return out
}

// waitForLane polls until a lane exists, returning the states of every lane at
// that moment — so a test can assert what the *rest* of the tree was doing
// when it appeared.
func (h *engineHarness) waitForLane(
	t *testing.T, parentID int64, laneID string,
) map[string]store.TaskState {
	t.Helper()
	deadline := time.Now().Add(fanOutBudget)
	for time.Now().Before(deadline) {
		states := h.laneStates(t, parentID)
		if _, ok := states[laneID]; ok {
			return states
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("lane %q never spawned under task %d", laneID, parentID)
	return nil
}

// TestFanOutEagerSpawnsBeforeSiblingsSettle is the feature: `dep` starts as
// soon as `quick` has merged, with the unrelated `slow` lane still running.
// Under a barrier that cannot happen, which the sibling test pins.
func TestFanOutEagerSpawnsBeforeSiblingsSettle(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, eagerDAG("eager"))

	states := h.waitForLane(t, task.ID, "dep")
	if slow := states["slow"]; slow == store.TaskDone {
		t.Fatalf("`slow` had already finished when `dep` spawned; the test proved nothing")
	}
	if states["quick"] != store.TaskDone {
		t.Errorf("`dep` spawned with its own dependency %s, not done", states["quick"])
	}

	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent state = %s (%s), want done", done.State, done.BlockReason)
	}
	for _, file := range []string{"slow.txt", "quick.txt", "dep.txt", "deeper.txt"} {
		if !h.fileOnBranch(t, task.BranchName, file) {
			t.Errorf("%s is missing from the parent's branch", file)
		}
	}

	// Decision 2: two eager merges never share an `iteration`, or the second
	// would spend the first's retry budget and overwrite its transcript.
	seen := map[int]bool{}
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID != "build" || run.State != store.StepSucceeded {
			continue
		}
		if seen[run.Iteration] {
			t.Errorf("two succeeded merge rows share iteration %d", run.Iteration)
		}
		seen[run.Iteration] = true
	}
	if len(seen) < 2 {
		t.Errorf("an eager DAG recorded %d merge rows, want at least 2", len(seen))
	}
}

// TestFanOutBarrierHoldsDependentsBack is the same DAG with the default
// schedule: `dep` does not exist until every lane of its round has settled.
func TestFanOutBarrierHoldsDependentsBack(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, eagerDAG(""))

	states := h.waitForLane(t, task.ID, "dep")
	if states["slow"] != store.TaskDone {
		t.Errorf("`dep` spawned under a barrier with `slow` %s; a round waits for every lane",
			states["slow"])
	}

	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent state = %s (%s), want done", done.State, done.BlockReason)
	}
	// Wave-derived rounds, unchanged: three waves, three merge rows at 0, 1, 2.
	seen := map[int]bool{}
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID == "build" && run.State == store.StepSucceeded {
			seen[run.Iteration] = true
		}
	}
	for wave := range 3 {
		if !seen[wave] {
			t.Errorf("barrier merge rows sit at %v, want one per wave 0..2", seen)
			break
		}
	}
}

// TestFanOutEagerFlatListRunsAsABarrier is decision 4: `schedule: eager` over
// lanes that declare no `needs:` among themselves is redundant, not wrong. It
// merges once at the end, exactly where task 080 left the flat-list case —
// merging as lanes finish would widen that decision's reversal to flat lists,
// which #301 deliberately kept bit-for-bit.
func TestFanOutEagerFlatListRunsAsABarrier(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := strings.Replace(
		fanOutSnapshot([2]string{"api", "api.txt"}, [2]string{"docs", "docs.txt"}),
		"    type: fan_out\n", "    type: fan_out\n    schedule: eager\n", 1)
	task := h.createTask(t, snapshot)

	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent state = %s (%s), want done", done.State, done.BlockReason)
	}
	merges := 0
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID != "build" {
			continue
		}
		merges++
		if run.Iteration != 0 {
			t.Errorf("a flat eager list wrote iteration %d; one round means 0", run.Iteration)
		}
	}
	if merges != 1 {
		t.Errorf("a flat eager list recorded %d fan_out rows, want 1", merges)
	}
}

// TestFanOutEagerBlocksOnASettledFailure is decision 3: a lane that settles
// without finishing blocks `lane_failed` **merging nothing new** in that
// admission, while lanes merged earlier stay merged and the in-flight lane is
// left to finish.
//
// "Settled without finishing" is `aborted`, not `blocked` — a blocked lane
// holds the join open until a human resolves it, under either schedule (§7.6),
// so the lane is cancelled here the way TestFanOutBlocksWhenALaneDidNotFinish
// cancels its own.
func TestFanOutEagerBlocksOnASettledFailure(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	var sb strings.Builder
	sb.WriteString("name: root\nsteps:\n  - id: build\n    type: fan_out\n    schedule: eager\n")
	sb.WriteString("    lanes:\n")
	// Merges first, and stays merged.
	sb.WriteString("      - id: quick\n        steps:\n")
	sb.WriteString(indent(writeFileStep("write-quick", "quick.txt", "quick")))
	// Spawns behind it and is cancelled while it runs.
	sb.WriteString("      - id: dep\n        needs: [quick]\n        steps:\n")
	sb.WriteString(indent(sleepStep("wait", 30)))
	// Unrelated, still in flight when `dep` settles, and left to finish.
	sb.WriteString("      - id: slow\n        steps:\n")
	sb.WriteString(indent(sleepStep("wait", 6)))
	sb.WriteString(indent(writeFileStep("write-slow", "slow.txt", "slow")))
	task := h.createTask(t, sb.String())

	h.waitForLane(t, task.ID, "dep")
	kids, err := h.store.ListChildren(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	for _, kid := range kids {
		if kid.LaneID == "dep" {
			if _, cErr := h.runner.Cancel(t.Context(), kid.ID); cErr != nil {
				t.Fatalf("cancel lane %d: %v", kid.ID, cErr)
			}
		}
	}

	blocked := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonLaneFailed {
		t.Fatalf("state = %s reason = %q, want blocked/%s",
			blocked.State, blocked.BlockReason, ReasonLaneFailed)
	}
	// Task 080 decision 2, unchanged: what was merged before the failure is
	// on the branch. Resetting it would destroy integrated commits.
	if !h.fileOnBranch(t, task.BranchName, "quick.txt") {
		t.Error("the lane merged before the failure was rolled back off the branch")
	}
	// Nothing new: the failing admission merged no further lane, and none was
	// merged afterwards either — the step is blocked.
	if h.fileOnBranch(t, task.BranchName, "slow.txt") {
		t.Error("a lane was merged in or after the admission that blocked lane_failed")
	}
	// In-flight lanes are real tasks and are left to finish (§7.6's posture
	// on cancel, applied here).
	deadline := time.Now().Add(fanOutBudget)
	for time.Now().Before(deadline) {
		if h.laneStates(t, task.ID)["slow"] == store.TaskDone {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("the in-flight lane never finished; states = %v", h.laneStates(t, task.ID))
}

// TestFanOutEagerMakesProgressUnderATightCap: an eager step is woken once per
// lane settling, and each wake takes a slot briefly. With one slot for the
// whole daemon a depth-3 tree still completes rather than thrashing — a wake
// that finds nothing to do parks again holding no slot, and writes no row.
func TestFanOutEagerMakesProgressUnderATightCap(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) { c.MaxParallelTasks = 1 })
	h.start(t)
	task := h.createTask(t, eagerDAG("eager"))

	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent state = %s (%s), want done under max_parallel_tasks: 1",
			done.State, done.BlockReason)
	}
	// Bounded by the direct lane count, not by the number of wakes: a wake
	// that found nothing mergeable records no row at all.
	rows := 0
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID == "build" {
			rows++
		}
	}
	if rows > 4 {
		t.Errorf("an eager step over 4 lanes wrote %d fan_out rows; churn is unbounded", rows)
	}
}
