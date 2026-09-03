package taskrun

// The round scheduler and derived lane lists (spec §7.6, task 080).

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// requireFileStep is a command step that fails unless a path is already in the
// worktree's HEAD tree. It is how a lane asserts, from inside its own
// worktree, that its dependencies' commits were there when it started —
// which is the thing `needs:` promises and the thing ordering alone would not
// prove.
func requireFileStep(id, file string) string {
	return commandStep(id, fmt.Sprintf("git cat-file -e HEAD:%s", file), "max_retries: 0")
}

// dagSnapshot is a fan_out over three lanes: `api` and `db` depend on nothing,
// and `wire` needs both. Each of the first two writes its own file; `wire`
// refuses to pass unless both are already on its branch.
func dagSnapshot() string {
	var sb strings.Builder
	sb.WriteString("name: root\nsteps:\n  - id: build\n    type: fan_out\n    lanes:\n")
	for _, lane := range [][2]string{{"api", "api.txt"}, {"db", "db.txt"}} {
		fmt.Fprintf(&sb, "      - id: %s\n        steps:\n", lane[0])
		sb.WriteString(indent(writeFileStep("write-"+lane[0], lane[1], lane[0])))
	}
	sb.WriteString("      - id: wire\n        needs: [api, db]\n        steps:\n")
	sb.WriteString(indent(requireFileStep("see-api", "api.txt")))
	sb.WriteString(indent(requireFileStep("see-db", "db.txt")))
	sb.WriteString(indent(writeFileStep("write-wire", "wire.txt", "wire")))
	return sb.String()
}

// TestFanOutNeedsSpawnsInRounds is the whole of phase 1 in one pass: the two
// independent lanes spawn together, `wire` does not spawn until both have
// merged, and its worktree carries their commits when its first step runs.
func TestFanOutNeedsSpawnsInRounds(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, dagSnapshot())

	// Round 1 is exactly the lanes with no `needs:`.
	first := h.waitForChildren(t, task.ID, 2)
	for _, lane := range first {
		if lane.LaneID == "wire" {
			t.Fatalf("lane %q spawned in round 1; its needs were not merged yet", lane.LaneID)
		}
	}
	// Round 2 adds the dependent one, and lane_order is still the *declared*
	// index — the merge order — not a position within its round.
	all := h.waitForChildren(t, task.ID, 3)
	var wire *store.Task
	for i := range all {
		if all[i].LaneID == "wire" {
			wire = &all[i]
		}
	}
	if wire == nil {
		t.Fatal("the dependent lane never spawned")
	}
	if wire.LaneOrder != 2 {
		t.Errorf("wire lane_order = %d, want 2 (the declared index)", wire.LaneOrder)
	}

	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	// `wire`'s steps only pass if its dependencies' commits were on its branch
	// when it started, so its being done is the worktree assertion.
	if wire.State != store.TaskDone {
		reloaded, err := h.store.GetTask(t.Context(), wire.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if reloaded.State != store.TaskDone {
			t.Fatalf("wire state = %s (%s); its worktree was missing a dependency's commits",
				reloaded.State, reloaded.BlockReason)
		}
	}
	for _, file := range []string{"api.txt", "db.txt", "wire.txt"} {
		if !h.fileOnBranch(t, task.BranchName, file) {
			t.Errorf("%s is missing from the parent's branch", file)
		}
	}

	// One merge row per round, discriminated by `iteration` (decision 3), so
	// round 2's attempt cannot be counted as round 1's retry.
	rounds := map[int]bool{}
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID == "build" && run.State == store.StepSucceeded {
			rounds[run.Iteration] = true
		}
	}
	if !rounds[0] || !rounds[1] {
		t.Errorf("merge rows sit at iterations %v, want one at 0 and one at 1", rounds)
	}
}

// TestFanOutFlatListIsOneRound pins the compatibility claim: a lane list with
// no `needs:` writes exactly one merge row, at iteration 0, where every
// pre-task-080 fan-out wrote it.
func TestFanOutFlatListIsOneRound(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, fanOutSnapshot([2]string{"api", "api.txt"}, [2]string{"docs", "docs.txt"}))

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
			t.Errorf("a flat lane list wrote iteration %d; one round means 0", run.Iteration)
		}
	}
	if merges != 1 {
		t.Errorf("a flat lane list recorded %d fan_out rows, want 1", merges)
	}
}

// derivedSnapshot is a producing step emitting one JSON object per line and a
// fan_out deriving its lanes from it. Each lane writes a file named after the
// id the item carried, so two lanes never collide and the branch says which
// lanes ran.
func derivedSnapshot(items []string, extra ...string) string {
	var sb strings.Builder
	sb.WriteString("name: root\nsteps:\n")
	sb.WriteString(commandStep("plan", emitLines(items), "max_retries: 0"))
	sb.WriteString("  - id: build\n    type: fan_out\n")
	for _, line := range extra {
		fmt.Fprintf(&sb, "    %s\n", line)
	}
	sb.WriteString("    for_each: '{{ (index .Steps \"plan\").Result }}'\n")
	sb.WriteString("    lane:\n      id: '{{ .Item.id }}'\n      needs: '{{ .Item.needs }}'\n")
	sb.WriteString("      fields:\n        name: '{{ .Item.id }}'\n      steps:\n")
	sb.WriteString(indent(writeFileStep("write", `{{ index .Task.Fields "name" }}.txt`, "x")))
	return sb.String()
}

// emitLines prints one line per item on either shell. The JSON is single-quoted
// in both, so its double quotes survive without an escape either shell would
// read differently (§8.3).
func emitLines(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		quoted = append(quoted, "'"+item+"'")
	}
	if len(quoted) == 0 {
		return script("printf ''", "Write-Output ''")
	}
	return script(
		"printf '%s\\n' "+strings.Join(quoted, " "),
		"Write-Output "+strings.Join(quoted, ", "),
	)
}

// TestFanOutDerivesItsLanes: the lane list comes from what the previous step
// printed, and after the spawn the snapshot reads as an ordinary static
// fan_out (decision 5).
func TestFanOutDerivesItsLanes(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, derivedSnapshot([]string{
		`{"id":"api","needs":[]}`,
		`{"id":"db","needs":[]}`,
		`{"id":"wire","needs":["api"]}`,
	}))

	first := h.waitForChildren(t, task.ID, 2)
	for _, lane := range first {
		if lane.LaneID == "wire" {
			t.Fatalf("derived lane %q spawned before its dependency merged", lane.LaneID)
		}
	}
	all := h.waitForChildren(t, task.ID, 3)
	ids := map[string]bool{}
	for _, lane := range all {
		ids[lane.LaneID] = true
	}
	for _, want := range []string{"api", "db", "wire"} {
		if !ids[want] {
			t.Errorf("derived lanes %v are missing %q", ids, want)
		}
	}

	// Materialized: the step in the task's own snapshot is a plain lanes list,
	// which is what makes every snapshot consumer correct with no new case —
	// and it re-validates, because the snapshot is re-parsed on every later
	// admission (§5.3).
	reloaded, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	wf, _, perr := workflow.Parse([]byte(reloaded.WorkflowSnapshot), workflow.Options{})
	if perr != nil {
		t.Fatalf("the materialized snapshot no longer validates: %v\n%s",
			perr, reloaded.WorkflowSnapshot)
	}
	step, ok := stepByID(wf, "build")
	if !ok {
		t.Fatalf("the snapshot has no build step:\n%s", reloaded.WorkflowSnapshot)
	}
	if step.Lane != nil || len(step.ForEach) > 0 {
		t.Errorf("the snapshot still carries a live lane:/for_each: driver:\n%s",
			reloaded.WorkflowSnapshot)
	}
	if len(step.Lanes) != 3 {
		t.Errorf("the snapshot has %d materialized lanes, want 3:\n%s",
			len(step.Lanes), reloaded.WorkflowSnapshot)
	}
	// The provenance survives the materialization: a derived list and a
	// hand-authored one must not read the same afterwards, because the graph
	// draws the difference (task 080 decision 5 as amended).
	if step.DerivedFrom == nil {
		t.Fatalf("the materialized step records nothing about what it derived from:\n%s",
			reloaded.WorkflowSnapshot)
	}
	if len(step.DerivedFrom.ForEach) == 0 {
		t.Errorf("derived_from carries no for_each templates: %+v", *step.DerivedFrom)
	}
	if step.DerivedFrom.Lane != "{{ .Item.id }}" {
		t.Errorf("derived_from.lane = %q, want the lane id template",
			step.DerivedFrom.Lane)
	}
}

func stepByID(wf *workflow.Workflow, id string) (workflow.Step, bool) {
	for _, s := range wf.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return workflow.Step{}, false
}

// TestFanOutBlocksOnANonJSONItem: an item that is not a JSON object blocks at
// spawn, naming the line, with nothing spawned.
func TestFanOutBlocksOnANonJSONItem(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, derivedSnapshot([]string{`{"id":"api","needs":[]}`, "not json"}))

	blocked := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonFanOutInvalid {
		t.Fatalf("state = %s reason = %q, want blocked/%s",
			blocked.State, blocked.BlockReason, ReasonFanOutInvalid)
	}
	kids, err := h.store.ListChildren(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(kids) != 0 {
		t.Errorf("%d lanes were spawned; a bad list must spawn nothing", len(kids))
	}
	var named bool
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID == "build" && strings.Contains(run.ResultSummary, "not json") {
			named = true
		}
	}
	if !named {
		t.Error("the recorded row does not name the offending line")
	}
}

// TestFanOutBlocksPastMaxLanes: the ceiling is checked on the list, before a
// single worktree exists (decision 6).
func TestFanOutBlocksPastMaxLanes(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, derivedSnapshot(
		[]string{`{"id":"a","needs":[]}`, `{"id":"b","needs":[]}`}, "max_lanes: 1"))

	blocked := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonFanOutLimit {
		t.Fatalf("state = %s reason = %q, want blocked/%s",
			blocked.State, blocked.BlockReason, ReasonFanOutLimit)
	}
	kids, err := h.store.ListChildren(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(kids) != 0 {
		t.Errorf("%d lanes were spawned past the ceiling; want none", len(kids))
	}
}

// TestFanOutBlocksPastTheTreeBound: `fan_out.max_tasks` is a creation-time
// check for a static list and a spawn-time one for a derived list, because the
// width is a fact the run discovers (decision 6).
func TestFanOutBlocksPastTheTreeBound(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) { c.FanOut.MaxTasks = 1 })
	h.start(t)
	task := h.createTask(t, derivedSnapshot(
		[]string{`{"id":"a","needs":[]}`, `{"id":"b","needs":[]}`}))

	blocked := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonFanOutLimit {
		t.Fatalf("state = %s reason = %q, want blocked/%s",
			blocked.State, blocked.BlockReason, ReasonFanOutLimit)
	}
}

// TestFanOutEmptyDerivedListSucceeds: an empty list is a no-op success
// recording one row, matching §7.6's all-lanes-guarded-off case and §7.8's
// empty `for_each` case. It must not park — a parent awaiting children it
// never spawned has no exit.
func TestFanOutEmptyDerivedListSucceeds(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, derivedSnapshot(nil))

	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("state = %s (%s), want done", done.State, done.BlockReason)
	}
	kids, err := h.store.ListChildren(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(kids) != 0 {
		t.Errorf("an empty derived list spawned %d lanes", len(kids))
	}
	rows := 0
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID == "build" {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("an empty derived list recorded %d fan_out rows, want 1", rows)
	}
}
