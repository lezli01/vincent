package taskrun

import (
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// The two per-task limits `POST /v1/tasks` can set (task 096 decisions 17,
// 18): the one-way `restricted` clamp and the task's own cost cap. Both are
// snapshotted on the row, so the engine reads them from the task rather than
// from anything that can change under a running task.

// TestEngineRestrictedClampBeatsAStepField is decision 17's whole claim: a
// clamped task runs a step that *declares* `full-auto` as `restricted`. A
// clamp that were merely a level in §8.6's chain would lose to the step field,
// which is exactly why it is applied after resolution.
func TestEngineRestrictedClampBeatsAStepField(t *testing.T) {
	const declaredFullAuto = `name: loose
steps:
  - id: think
    type: agent
    permission_mode: full-auto
    max_retries: 0
    prompt: do the thing
`
	for name, clamp := range map[string]bool{"clamped": true, "unclamped": false} {
		t.Run(name, func(t *testing.T) {
			h := newEngineHarness(t)
			task := h.createTaskWith(t, declaredFullAuto, func(tk *store.Task) { tk.Restricted = clamp })
			h.start(t)

			final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
			if final.State != store.TaskDone {
				t.Fatalf("task = %s (%s), want done", final.State, final.BlockReason)
			}
			if final.Restricted != clamp {
				t.Errorf("stored restricted = %v, want %v", final.Restricted, clamp)
			}
			runs := h.stepRuns(t, task.ID)
			if len(runs) != 1 {
				t.Fatalf("step runs = %d, want 1: %+v", len(runs), runs)
			}
			want := "full-auto"
			if clamp {
				want = "restricted"
			}
			if runs[0].PermissionMode != want {
				t.Errorf("permission_mode = %q, want %q", runs[0].PermissionMode, want)
			}
		})
	}
}

// TestEngineTaskCostCapIsTheLowerOfTheTwo pins decision 18: the effective cap
// is the lower of config's and the task's, 0 on either side is "no cap from
// this side", and the block is `cost_limit` whichever side tripped it.
func TestEngineTaskCostCapIsTheLowerOfTheTwo(t *testing.T) {
	tight, generous := fakeAgentCostUSD/2, fakeAgentCostUSD*100
	for _, tc := range []struct {
		name         string
		global, task float64
		want         store.TaskState
	}{
		{"task cap alone blocks", 0, tight, store.TaskBlocked},
		{"task cap tighter than global blocks", generous, tight, store.TaskBlocked},
		{"task cap cannot lift a tight global", tight, generous, store.TaskBlocked},
		{"generous task cap alone runs", 0, generous, store.TaskDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newEngineHarnessWith(t, func(c *config.Config) { c.MaxTaskCostUSD = tc.global })
			task := h.createTaskWith(t, costCappedWorkflow, func(tk *store.Task) { tk.MaxTaskCostUSD = tc.task })
			h.start(t)

			final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
			if final.State != tc.want {
				t.Fatalf("task = %s (%s), want %s", final.State, final.BlockReason, tc.want)
			}
			if tc.want == store.TaskBlocked && final.BlockReason != ReasonCostLimit {
				t.Errorf("block reason = %q, want %q", final.BlockReason, ReasonCostLimit)
			}
		})
	}
}

func TestEffectiveCostCap(t *testing.T) {
	for _, tc := range []struct{ global, task, want float64 }{
		{0, 0, 0},
		{10, 0, 10},
		{0, 5, 5},
		{10, 5, 5},
		{5, 10, 5},
		{-1, 3, 3},
		{0, -1, 0},
	} {
		if got := effectiveCostCap(tc.global, tc.task); got != tc.want {
			t.Errorf("effectiveCostCap(%v, %v) = %v, want %v", tc.global, tc.task, got, tc.want)
		}
	}
}
