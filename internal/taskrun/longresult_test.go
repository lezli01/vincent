package taskrun

// A command step's `.Result` is the last 200 lines of its stdout (spec §8.4),
// bounded by outputTailLines and outputTailBytes (256 KiB) — not by
// resultSummaryLimit, which bounds the row a *human* reads. A `for_each:`
// (§7.6, §7.8) consuming that value is what makes the exact bound
// load-bearing: cut at 4096 bytes with a raw byte slice, a lane list longer
// than that loses its items mid-line, and an item that is 4 KiB on its own is
// destroyed even when it is the first one. Issue #313.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// longUnitBytes makes one item comfortably larger than resultSummaryLimit
// (4096), which is the shape task 161 hit: a planner emitting a paragraph of
// prose per unit produces ~6 KB objects, so a *single* unit already exceeds
// the cap and the cut never reaches a newline.
const longUnitBytes = 6000

// longUnit is one JSONL planner unit padded to longUnitBytes with prose —
// `fields:` rendered into the lane prompt is the documented channel for
// per-lane text (§7.6). It carries no single quote, so it survives being
// embedded in either shell's single-quoted string the way emitLines needs
// (§8.3).
func longUnit(t *testing.T, id string) string {
	t.Helper()
	unit := map[string]any{"id": id, "needs": []string{}, "brief": ""}
	head, err := json.Marshal(unit)
	if err != nil {
		t.Fatalf("marshal unit: %v", err)
	}
	unit["brief"] = strings.Repeat("word ", (longUnitBytes-len(head))/5)
	line, err := json.Marshal(unit)
	if err != nil {
		t.Fatalf("marshal unit: %v", err)
	}
	if len(line) <= resultSummaryLimit {
		t.Fatalf("unit %q is %d bytes, want more than resultSummaryLimit (%d)",
			id, len(line), resultSummaryLimit)
	}
	return string(line)
}

// TestLoopForEachOverLongList: a loop's `for_each` reading a step's `.Result`
// iterates over every line the command printed, whatever their size. Two
// units of ~6 KB are far inside §8.4's 200 lines and inside the 256 KiB the
// capture already bounds the tail by, so both must arrive intact.
func TestLoopForEachOverLongList(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)

	units := []string{longUnit(t, "alpha"), longUnit(t, "beta")}
	snapshot := "name: each\nsteps:\n" +
		commandStep("plan", emitLines(units), "max_retries: 0") +
		"  - id: visit\n    type: loop\n    for_each: '{{ .Steps.plan.Result }}'\n    steps:\n" +
		bodyIndent(commandStep("noop", script("exit 0", "exit 0"), "max_retries: 0"))
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	var items []string
	for _, r := range h.stepRuns(t, task.ID) {
		if r.StepIndex == 1 && r.LoopItem != "" {
			items = append(items, r.LoopItem)
		}
	}
	if !equalStrings(items, units) {
		t.Errorf("loop ran %d iteration(s) over items of %s bytes, want %d over %s; "+
			"`.Result` is bounded by outputTailLines/outputTailBytes (§8.4), "+
			"never by resultSummaryLimit (%d)",
			len(items), byteSizes(items), len(units), byteSizes(units), resultSummaryLimit)
	}
}

// TestFanOutDerivesLanesFromLongList is task 161's own shape: `plan-emit`
// prints one JSON object per unit, each carrying a `brief`, and the derived
// fan-out (§7.6) blocks `fan_out_invalid` on "item 1 is not a JSON object"
// because the stored tail ends mid-object — the planner wrote a valid graph
// and the engine lost it.
func TestFanOutDerivesLanesFromLongList(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, derivedSnapshot([]string{longUnit(t, "api"), longUnit(t, "db")}))

	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent state = %s (block_reason %q), want done — a lane list longer "+
			"than resultSummaryLimit (%d) is cut mid-item (§8.4)",
			done.State, done.BlockReason, resultSummaryLimit)
	}
	kids, err := h.store.ListChildren(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	ids := make([]string, 0, len(kids))
	for _, kid := range kids {
		ids = append(ids, kid.LaneID)
	}
	if !equalStrings(ids, []string{"api", "db"}) {
		t.Errorf("derived lanes = %v, want [api db]", ids)
	}
}

// byteSizes describes a list by the size of each entry, which is what a
// failure here is about — the ids read the same either way, the lengths do
// not.
func byteSizes(items []string) string {
	sizes := make([]string, 0, len(items))
	for _, item := range items {
		sizes = append(sizes, fmt.Sprint(len(item)))
	}
	return "[" + strings.Join(sizes, " ") + "]"
}
