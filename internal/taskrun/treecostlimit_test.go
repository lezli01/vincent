package taskrun

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// `max_tree_cost_usd` end to end (task 115, §12.3, §18). Every cap here is
// chosen against fakeAgentCostUSD, like costlimit_test.go's: one claude-shaped
// agent attempt anywhere in a tree adds exactly that to the tree's rollup.

// agentStep is an agent step that spends one fakeAgentCostUSD per attempt,
// written at the top level's two spaces like commandStep.
func agentStep(id string, extra ...string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "  - id: %s\n    type: agent\n", id)
	for _, line := range extra {
		fmt.Fprintf(&sb, "    %s\n", line)
	}
	sb.WriteString("    prompt: spend some money\n")
	return sb.String()
}

// spendingFanOut is a workflow whose fan_out lanes each spend once and then
// write a file, so a lane the cap lets through has a commit to merge. before
// and after are top-level steps around the fan_out, already written at two
// spaces.
func spendingFanOut(before, after string, lanes ...string) string {
	var sb strings.Builder
	sb.WriteString("name: tree\nsteps:\n")
	sb.WriteString(before)
	sb.WriteString("  - id: build\n    type: fan_out\n    lanes:\n")
	for _, lane := range lanes {
		fmt.Fprintf(&sb, "      - id: %s\n        steps:\n", lane)
		sb.WriteString(indent(agentStep("think-"+lane, "max_retries: 2")))
		sb.WriteString(indent(writeFileStep("write-"+lane, lane+".txt", lane)))
	}
	sb.WriteString(after)
	return sb.String()
}

// settleLanes waits for every lane of parent to reach done or blocked and
// returns them split that way.
func (h *engineHarness) settleLanes(t *testing.T, parentID int64, want int) (done, blocked []*store.Task) {
	t.Helper()
	for _, lane := range h.waitForChildren(t, parentID, want) {
		got := h.waitForStateWithin(t, lane.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
		if got.State == store.TaskDone {
			done = append(done, got)
		} else {
			blocked = append(blocked, got)
		}
	}
	return done, blocked
}

// TestEngineTreeCostCapBlocksTheLaneThatCrosses is task 115's done-when. Two
// lanes, each well under any per-task cap, together spend past
// `max_tree_cost_usd`: the lane whose attempt crosses blocks
// `tree_cost_limit`, its row keeps its own state, and no retry is consumed.
//
// The rest is decision 6's remedy, in the order an operator meets it. A retry
// on the lane buys one attempt and re-blocks; a cascade retry on the parent
// (task 090) re-admits the lane with the same one-attempt result; raising the
// cap by a reload and retrying lets the tree finish.
//
// One slot makes "the lane that crosses" a single lane: with both running at
// once, both boundaries could see the second attempt's money.
func TestEngineTreeCostCapBlocksTheLaneThatCrosses(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.MaxParallelTasks = 1
		c.MaxTaskCostUSD = fakeAgentCostUSD * 100
		c.MaxTreeCostUSD = fakeAgentCostUSD * 1.5 // the second attempt in the tree is over
	})
	h.start(t)
	parent := h.createTask(t, spendingFanOut("", "", "api", "docs"))

	done, blocked := h.settleLanes(t, parent.ID, 2)
	if len(done) != 1 || len(blocked) != 1 {
		t.Fatalf("lanes done/blocked = %d/%d, want 1/1 — only the lane that crossed blocks", len(done), len(blocked))
	}
	lane := blocked[0]
	if lane.BlockReason != ReasonTreeCostLimit {
		t.Fatalf("lane block reason = %q, want %q", lane.BlockReason, ReasonTreeCostLimit)
	}
	runs := h.stepRuns(t, lane.ID)
	if len(runs) != 1 {
		t.Fatalf("lane step runs = %d, want 1 — the write after the cap must not run: %+v", len(runs), runs)
	}
	if runs[0].State != store.StepSucceeded || runs[0].FailureReason != "" {
		t.Errorf("lane step run = %s/%q, want succeeded with no failure reason", runs[0].State, runs[0].FailureReason)
	}
	ref := store.StepRef{TaskID: lane.ID, StepIndex: 0, StepID: runs[0].StepID}
	attempts := func() store.StepAttempts {
		t.Helper()
		a, err := h.store.CountStepAttempts(t.Context(), ref, time.Time{})
		if err != nil {
			t.Fatalf("CountStepAttempts: %v", err)
		}
		return a
	}
	if a := attempts(); a.Last != 1 || a.Failed != 0 {
		t.Fatalf("attempts = %+v, want one attempt and no failure — the cap consumes no retry", a)
	}
	// The parent makes no attempt while parked, so it never blocks here; the
	// join stays open on the blocked lane, which its rollup already names.
	if got := h.waitForState(t, parent.ID, store.TaskAwaitingChildren); got.BlockReason != "" {
		t.Errorf("parent block reason = %q, want none", got.BlockReason)
	}

	if _, _, err := h.runner.Retry(t.Context(), lane.ID, store.Override{}); err != nil {
		t.Fatalf("Retry lane: %v", err)
	}
	if got := h.waitForStateWithin(t, lane.ID, fanOutBudget, store.TaskBlocked); got.BlockReason != ReasonTreeCostLimit {
		t.Errorf("lane block reason after retry = %q, want %q", got.BlockReason, ReasonTreeCostLimit)
	}
	if a := attempts(); a.Last != 2 {
		t.Errorf("attempts after a lane retry = %d, want 2 — a retry buys exactly one attempt", a.Last)
	}

	_, n, err := h.runner.Retry(t.Context(), parent.ID, store.Override{})
	if err != nil {
		t.Fatalf("Retry parent: %v", err)
	}
	if n != 1 {
		t.Errorf("cascade re-admitted %d lanes, want 1", n)
	}
	if got := h.waitForStateWithin(t, lane.ID, fanOutBudget, store.TaskBlocked); got.BlockReason != ReasonTreeCostLimit {
		t.Errorf("lane block reason after a cascade = %q, want %q", got.BlockReason, ReasonTreeCostLimit)
	}
	if a := attempts(); a.Last != 3 {
		t.Errorf("attempts after a cascade retry = %d, want 3 — each press buys one attempt per lane", a.Last)
	}

	h.reload(func(c *config.Config) { c.MaxTreeCostUSD = fakeAgentCostUSD * 100 })
	if _, _, err := h.runner.Retry(t.Context(), parent.ID, store.Override{}); err != nil {
		t.Fatalf("Retry parent after raising the cap: %v", err)
	}
	final := h.waitForStateWithin(t, parent.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if final.State != store.TaskDone {
		t.Fatalf("parent = %s (%s), want done once the cap was raised", final.State, final.BlockReason)
	}
}

// TestEngineTreeCostCapOffOrGenerousRunsToDone: a tree that stays under the
// cap, or has none, is untouched. Equal is not over — the check is `>`.
func TestEngineTreeCostCapOffOrGenerousRunsToDone(t *testing.T) {
	for name, capUSD := range map[string]float64{
		"unset is off":        0,
		"cap above the spend": fakeAgentCostUSD * 100,
		"cap equal the spend": fakeAgentCostUSD * 2,
	} {
		t.Run(name, func(t *testing.T) {
			h := newEngineHarnessWith(t, func(c *config.Config) { c.MaxTreeCostUSD = capUSD })
			h.start(t)
			parent := h.createTask(t, spendingFanOut("", "", "api", "docs"))

			final := h.waitForStateWithin(t, parent.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
			if final.State != store.TaskDone {
				t.Fatalf("parent = %s (%s), want done", final.State, final.BlockReason)
			}
			if _, blocked := h.settleLanes(t, parent.ID, 2); len(blocked) != 0 {
				t.Errorf("%d lanes blocked, want none", len(blocked))
			}
		})
	}
}

// TestEngineTreeCostCapCountsAndBlocksTheParent: the parent's own spend before
// the fan-out is in the tree, and a parent attempt after the join is a
// boundary like a lane's (decision 4). The cap sits where only the parent's
// first attempt makes the difference: the lane sees two attempts' money, the
// post-join step three.
func TestEngineTreeCostCapCountsAndBlocksTheParent(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.MaxTreeCostUSD = fakeAgentCostUSD * 2.5
	})
	h.start(t)
	parent := h.createTask(t, spendingFanOut(
		agentStep("plan", "max_retries: 0"),
		agentStep("review", "max_retries: 0")+commandStep("after", "exit 0", "max_retries: 0"),
		"only"))

	final := h.waitForStateWithin(t, parent.ID, fanOutBudget, store.TaskBlocked, store.TaskDone)
	if final.State != store.TaskBlocked || final.BlockReason != ReasonTreeCostLimit {
		t.Fatalf("parent = %s (%s), want blocked %s", final.State, final.BlockReason, ReasonTreeCostLimit)
	}
	if final.CurrentStep != 2 {
		t.Errorf("parent current_step = %d, want 2 — it blocks at the post-join step", final.CurrentStep)
	}
	if _, blocked := h.settleLanes(t, parent.ID, 1); len(blocked) != 0 {
		t.Errorf("the lane blocked (%s); two attempts are under the cap", blocked[0].BlockReason)
	}
	for _, run := range h.stepRuns(t, parent.ID) {
		if run.StepID == "after" {
			t.Errorf("the step after the cap ran: %+v", run)
		}
	}
	root, rollup, err := h.store.TreeCost(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("TreeCost: %v", err)
	}
	if root != parent.ID || !rollup.HasCost {
		t.Fatalf("TreeCost = root %d %+v, want root %d with a cost", root, rollup, parent.ID)
	}
	if want := fakeAgentCostUSD * 3; rollup.CostUSD < want-1e-9 || rollup.CostUSD > want+1e-9 {
		t.Errorf("tree cost = %v, want %v — the parent's own attempts count", rollup.CostUSD, want)
	}
}

// TestEngineTreeCostCapReachesAGrandchild: a lane that fans out again is still
// in its root's tree. The grandchild's boundary climbs to the root, sees the
// root's own spend, and the grandchild is the task that blocks.
func TestEngineTreeCostCapReachesAGrandchild(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.MaxTreeCostUSD = fakeAgentCostUSD * 1.5
	})
	h.start(t)
	inner := "  - id: nest\n    type: fan_out\n    lanes:\n" +
		"      - id: inner\n        steps:\n" +
		indent(agentStep("think-inner", "max_retries: 0")) +
		indent(writeFileStep("write-inner", "inner.txt", "inner"))
	root := h.createTask(t, "name: tree\nsteps:\n"+
		agentStep("plan", "max_retries: 0")+
		"  - id: build\n    type: fan_out\n    lanes:\n"+
		"      - id: outer\n        steps:\n"+indent(inner))

	outer := h.waitForChildren(t, root.ID, 1)[0]
	grandchild := h.waitForChildren(t, outer.ID, 1)[0]
	got := h.waitForStateWithin(t, grandchild.ID, fanOutBudget, store.TaskBlocked, store.TaskDone)
	if got.State != store.TaskBlocked || got.BlockReason != ReasonTreeCostLimit {
		t.Fatalf("grandchild = %s (%s), want blocked %s — the root's spend is in its tree",
			got.State, got.BlockReason, ReasonTreeCostLimit)
	}
	for _, id := range []int64{root.ID, outer.ID} {
		if s := h.waitForState(t, id, store.TaskAwaitingChildren); s.BlockReason != "" {
			t.Errorf("task %d block reason = %q, want none — only the grandchild crossed", id, s.BlockReason)
		}
	}
}

// TestEngineTreeCostCapTaskCapWins is decision 2's precedence: with both caps
// over at one boundary the block is cost_limit, the one raising
// `max_tree_cost_usd` cannot clear.
func TestEngineTreeCostCapTaskCapWins(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.MaxTaskCostUSD = fakeAgentCostUSD / 2
		c.MaxTreeCostUSD = fakeAgentCostUSD / 2
	})
	task := h.createTask(t, costCappedWorkflow)
	h.start(t)

	final := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if final.State != store.TaskBlocked || final.BlockReason != ReasonCostLimit {
		t.Fatalf("task = %s (%s), want blocked %s", final.State, final.BlockReason, ReasonCostLimit)
	}
}

// TestEngineTreeCostCapOnATreeOfOne runs the propagation branches task 033
// built — a failure with retries and a backoff left, a loop body, a parallel
// group with and without `allow_failure` — against the tree cap. A task that
// never fans out is a tree of one, so these need no lanes; what they prove is
// that each branch carries the reason rather than a cost_limit flag.
func TestEngineTreeCostCapOnATreeOfOne(t *testing.T) {
	loop := "name: looping\nsteps:\n  - id: grind\n    type: loop\n    count: 3\n    steps:\n" +
		"      - id: think\n        type: agent\n        max_retries: 0\n        prompt: iterate\n"
	for _, tc := range []struct {
		name, snapshot, scenario string
	}{
		{
			name:     "a failure with retries and a backoff left",
			snapshot: "name: failing\nsteps:\n" + agentStep("think", "max_retries: 2", "retry_backoff: 30s"),
			scenario: "nonzero-exit",
		},
		{name: "inside a loop", snapshot: loop},
		{
			name: "inside a parallel group",
			snapshot: groupSnapshot("", agentStep("think", "max_retries: 0")) +
				commandStep("after", "exit 0", "max_retries: 0"),
		},
		{
			name: "past allow_failure in a parallel group",
			snapshot: groupSnapshot("", agentStep("think", "max_retries: 0", "allow_failure: true")) +
				commandStep("after", "exit 0", "max_retries: 0"),
			scenario: "nonzero-exit",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newEngineHarnessWith(t, func(c *config.Config) {
				c.MaxTreeCostUSD = fakeAgentCostUSD / 2
			})
			if tc.scenario != "" {
				t.Setenv("FAKEAGENT_SCENARIO", tc.scenario)
			}
			task := h.createTask(t, tc.snapshot)
			h.start(t)

			final := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
			if final.State != store.TaskBlocked || final.BlockReason != ReasonTreeCostLimit {
				t.Fatalf("task = %s (%s), want blocked %s", final.State, final.BlockReason, ReasonTreeCostLimit)
			}
			if final.QueuedReason != "" {
				t.Errorf("queued reason = %q, want none — no retry_backoff hold may be taken", final.QueuedReason)
			}
			var agentRuns int
			for _, run := range h.stepRuns(t, task.ID) {
				if run.StepID == "after" {
					t.Errorf("the step after the cap ran: %+v", run)
				}
				if run.StepID == "think" {
					agentRuns++
				}
			}
			if agentRuns != 1 {
				t.Errorf("agent attempts = %d, want 1 — nothing past the crossing attempt runs", agentRuns)
			}
			if hasEvent(h.eventTypes(t, task.ID), eventStepRetrying) {
				t.Error("a step.retrying event was emitted; the retry was announced and then not taken")
			}
		})
	}
}

// TestEngineTreeCostCapCountsOnlyReportedCost is decision 7. On its own, an
// adapter that reports no cost leaves the cap inert however tight it is; in a
// tree mixed with claude, only claude's money is counted — which a cap equal
// to one claude attempt proves, since codex and cursor adding anything at all
// would put it over.
func TestEngineTreeCostCapCountsOnlyReportedCost(t *testing.T) {
	type adapter struct{ agent, model string }
	costless := []adapter{{"codex", "gpt-5.6-sol"}, {"cursor", "claude-sonnet-5-thinking-high"}}
	for _, tc := range costless {
		t.Run(tc.agent+" alone", func(t *testing.T) {
			h := newEngineHarnessWith(t, func(c *config.Config) { c.MaxTreeCostUSD = 0.000001 })
			task := h.createTask(t, "name: costless\nsteps:\n"+
				agentStep("think", "agent: "+tc.agent, "model: "+tc.model, "max_retries: 0"))
			h.start(t)
			if final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked); final.State != store.TaskDone {
				t.Fatalf("task = %s (%s), want done — %s reports no cost", final.State, final.BlockReason, tc.agent)
			}
		})
	}
	t.Run("mixed lanes", func(t *testing.T) {
		h := newEngineHarnessWith(t, func(c *config.Config) { c.MaxTreeCostUSD = fakeAgentCostUSD })
		h.start(t)
		var sb strings.Builder
		sb.WriteString("name: mixed\nsteps:\n  - id: build\n    type: fan_out\n    lanes:\n")
		for _, lane := range append(costless, adapter{"claude", ""}) {
			fmt.Fprintf(&sb, "      - id: %s\n        steps:\n", lane.agent)
			extra := []string{"max_retries: 0", "agent: " + lane.agent}
			if lane.model != "" {
				extra = append(extra, "model: "+lane.model)
			}
			sb.WriteString(indent(agentStep("think-"+lane.agent, extra...)))
			sb.WriteString(indent(writeFileStep("write-"+lane.agent, lane.agent+".txt", lane.agent)))
		}
		parent := h.createTask(t, sb.String())

		final := h.waitForStateWithin(t, parent.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
		if final.State != store.TaskDone {
			t.Fatalf("parent = %s (%s), want done", final.State, final.BlockReason)
		}
		_, rollup, err := h.store.TreeCost(t.Context(), parent.ID)
		if err != nil {
			t.Fatalf("TreeCost: %v", err)
		}
		if !rollup.HasCost || rollup.CostUSD != fakeAgentCostUSD {
			t.Errorf("tree cost = %+v, want %v — only claude reported", rollup, fakeAgentCostUSD)
		}
	})
}
