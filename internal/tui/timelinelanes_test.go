package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// fanOutDetail is a task that fans out twice: step 1 spawns `api` and `web`,
// step 3 spawns `docs`, with a command step between and after them.
func fanOutDetail(t *testing.T) *detail {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 40
	fan := func(id int64, index int, name string) apiclient.StepRun {
		r := attempt(id, index, 1, name, "succeeded", false)
		r.StepType = stepTypeFanOut
		return r
	}
	rows := []apiclient.StepRun{
		attempt(1, 0, 1, "plan", "succeeded", false),
		fan(2, 1, "build"),
		attempt(3, 2, 1, "test", "succeeded", false),
		fan(4, 3, "document"),
	}
	d.applyLoaded(detailLoadedMsg{id: d.taskID, task: apiclient.TaskDetail{
		Task:  apiclient.Task{ID: d.taskID, Title: "fan", State: stateRunning, StepTotal: 4},
		Steps: rows,
	}})
	lane := func(id int64, index int, name, state string) apiclient.Task {
		parent := d.taskID
		return apiclient.Task{
			ID: id, State: state, ParentTaskID: &parent,
			ParentStepIndex: &index, LaneID: &name,
		}
	}
	reason := "check_failed"
	web := lane(52, 1, "web", stateBlocked)
	web.BlockReason = &reason
	d.laneRows = []apiclient.Task{lane(51, 1, "api", stateDone), web, lane(53, 3, "docs", stateRunning)}
	d.focus = focusTimeline
	return d
}

func timelineText(d *detail) []string {
	return strings.Split(ansi.Strip(d.renderTimeline(60)), "\n")
}

func lineIndex(lines []string, sub string) int {
	for i, l := range lines {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

// TestTimelineDrawsLanesUnderTheirOwnFanOut: each fan_out step lists its own
// lanes, indented under its attempts and before the next step.
func TestTimelineDrawsLanesUnderTheirOwnFanOut(t *testing.T) {
	lines := timelineText(fanOutDetail(t))
	api, web, docs := lineIndex(lines, "↳ ✓ api"), lineIndex(lines, "web"), lineIndex(lines, "docs")
	test, document := lineIndex(lines, "test"), lineIndex(lines, "document")
	if api < 0 || web < 0 || docs < 0 {
		t.Fatalf("lane rows missing:\n%s", strings.Join(lines, "\n"))
	}
	if api > web || web > test || test > document || document > docs {
		t.Fatalf("lanes not under their own fan_out step:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[web], "#52") || !strings.Contains(lines[web], "check_failed") {
		t.Fatalf("blocked lane row %q lacks its task number or block reason", lines[web])
	}
}

// TestTimelineCursorWalksOntoLanes: ↓ stops on every lane between the
// fan_out's attempt and the next step, and ↑ walks back out.
func TestTimelineCursorWalksOntoLanes(t *testing.T) {
	d := fanOutDetail(t)
	d.selectedRun = 2 // the first fan_out
	var got []int64
	for range 4 {
		d.moveTimelineSelection(1)
		if lane := d.cursorLane(); lane != 0 {
			got = append(got, -lane)
		} else {
			got = append(got, d.selectedRun)
		}
	}
	want := []int64{-51, -52, 3, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("↓ walk = %v, want %v (negative = lane)", got, want)
		}
	}
	d.moveTimelineSelection(-1)
	d.moveTimelineSelection(-1)
	if d.cursorLane() != 52 {
		t.Fatalf("↑ back from the next step landed on lane %d, want 52", d.cursorLane())
	}
	lines := timelineText(d)
	sel := lineIndex(lines, "web")
	if sel < 0 {
		t.Fatal("selected lane row not drawn")
	}
}

// TestTimelineLaneSelectionDropsWhenTheCursorMovesElsewhere: a lane selection
// holds only while the attempt it was anchored to is still selected.
func TestTimelineLaneSelectionDropsWhenTheCursorMovesElsewhere(t *testing.T) {
	d := fanOutDetail(t)
	d.selectLaneRow(51, 2)
	if d.cursorLane() != 51 {
		t.Fatal("lane selection did not take")
	}
	d.selectedRun = 3 // e.g. `N`, or a click on an attempt
	if d.cursorLane() != 0 {
		t.Fatal("lane selection survived the cursor moving to another attempt")
	}
}

// TestEnterOnALaneRowOpensIt: enter and `l` on a lane row open that lane's
// workspace, remembering the parent so esc comes back.
func TestEnterOnALaneRowOpensIt(t *testing.T) {
	for _, key := range []string{"enter", "l"} {
		v := newTaskView(fanOutDetail(t))
		v.detail.loaded = true
		v.lanes = v.detail.laneRows
		v.tab = taskTabSteps
		v.detail.selectLaneRow(53, 4)
		cmd := v.updateKey(registryKey(t, key))
		if cmd == nil {
			t.Fatalf("%s on a lane row did nothing", key)
		}
		msg, ok := cmd().(openTaskMsg)
		if !ok {
			t.Fatalf("%s on a lane row produced %T, want openTaskMsg", key, cmd())
		}
		if msg.id != 53 || msg.from != 40 {
			t.Fatalf("%s opened %d from %d, want 53 from 40", key, msg.id, msg.from)
		}
	}
}

// TestTimelineLanesWithoutAStepIndex: an older daemon serves no
// parent_step_index, and its lanes go under the first fan_out step.
func TestTimelineLanesWithoutAStepIndex(t *testing.T) {
	d := fanOutDetail(t)
	for i := range d.laneRows {
		d.laneRows[i].ParentStepIndex = nil
	}
	lines := timelineText(d)
	if docs, test := lineIndex(lines, "docs"), lineIndex(lines, "test"); docs < 0 || docs > test {
		t.Fatalf("lanes without a step index not under the first fan_out:\n%s", strings.Join(lines, "\n"))
	}
}

// TestOutputFollowsTheStepsCursorOntoALane: standing on a lane row points the
// Output tab's lane selector at it, and walking off the lanes hands it back
// to the task's own output — unless the reader chose a lane with `<`/`>`.
func TestOutputFollowsTheStepsCursorOntoALane(t *testing.T) {
	v := newTaskView(fanOutDetail(t))
	v.lanes = v.detail.laneRows
	v.tab = taskTabSteps
	v.detail.selectedRun = 2

	v.updateKey(registryKey(t, "down"))
	if lane, ok := v.selectedLane(); !ok || lane.ID != 51 {
		t.Fatalf("Output lane after ↓ onto api = %+v, %v; want lane 51", lane, ok)
	}
	v.updateKey(registryKey(t, "down"))
	if lane, ok := v.selectedLane(); !ok || lane.ID != 52 {
		t.Fatalf("Output lane after ↓ onto web = %+v, %v; want lane 52", lane, ok)
	}
	v.updateKey(registryKey(t, "down"))
	if _, ok := v.selectedLane(); ok {
		t.Fatal("walking off the lanes left the Output tab on a lane")
	}

	// A lane picked on the Output tab survives walking the attempts.
	v.tab = taskTabOutput
	v.updateKey(synthKey(">"))
	v.tab = taskTabSteps
	v.updateKey(registryKey(t, "up"))
	if lane, ok := v.selectedLane(); !ok || lane.ID != 52 {
		// ↑ from `test` lands on web, which the timeline now owns.
		t.Fatalf("↑ onto web = %+v, %v; want lane 52", lane, ok)
	}
	v.updateKey(registryKey(t, "up"))
	v.updateKey(registryKey(t, "up"))
	if _, ok := v.selectedLane(); ok {
		t.Fatal("walking up off the lanes left the Output tab on a lane")
	}
	v.tab = taskTabOutput
	v.updateKey(synthKey(">"))
	v.tab = taskTabSteps
	v.updateKey(registryKey(t, "up"))
	if lane, ok := v.selectedLane(); !ok || lane.ID != 51 {
		t.Fatalf("a `>` choice was undone by walking the attempts: %+v, %v", lane, ok)
	}
}

// roundsDetail is a fan_out that ran two rounds: `api` and `db` spawned in
// round 0, `wire` in round 1.
func roundsDetail(t *testing.T) *detail {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 60
	merge := func(id int64, round int) apiclient.StepRun {
		r := attempt(id, 0, 1, "build", "succeeded", false)
		r.StepType, r.Iteration = stepTypeFanOut, round
		return r
	}
	d.applyLoaded(detailLoadedMsg{id: d.taskID, task: apiclient.TaskDetail{
		Task:          apiclient.Task{ID: d.taskID, Title: "rounds", State: stateRunning, StepTotal: 1},
		Steps:         []apiclient.StepRun{merge(1, 0), merge(2, 1)},
		WorkflowSteps: []apiclient.WorkflowStep{{Index: 0, ID: "build", Type: stepTypeFanOut}},
	}})
	lane := func(id int64, name string, round int) apiclient.Task {
		parent, index := d.taskID, 0
		return apiclient.Task{
			ID: id, State: stateDone, ParentTaskID: &parent,
			ParentStepIndex: &index, LaneID: &name, LaneRound: &round,
		}
	}
	d.laneRows = []apiclient.Task{lane(61, "api", 0), lane(62, "db", 0), lane(63, "wire", 1)}
	d.focus = focusTimeline
	return d
}

// TestTimelineLanesSitInTheirRound: each lane is drawn inside the round that
// spawned it, and folding a round hides its lanes with its attempts.
func TestTimelineLanesSitInTheirRound(t *testing.T) {
	d := roundsDetail(t)
	d.setFold(tierKey{0, 0}, true)
	lines := timelineText(d)
	r0, r1 := lineIndex(lines, "round 0"), lineIndex(lines, "round 1")
	api, wire := lineIndex(lines, "api"), lineIndex(lines, "wire")
	if r0 < 0 || r1 < 0 || r0 >= api || api >= r1 || r1 >= wire {
		t.Fatalf("lanes not inside their rounds:\n%s", strings.Join(lines, "\n"))
	}

	d.setFold(tierKey{0, 0}, false)
	lines = timelineText(d)
	if lineIndex(lines, "api") >= 0 {
		t.Fatalf("a folded round still draws its lanes:\n%s", strings.Join(lines, "\n"))
	}
	if lineIndex(lines, "wire") < 0 {
		t.Fatalf("the open round lost its lane:\n%s", strings.Join(lines, "\n"))
	}

	// ↓ from round 1's attempt stops on its lane; round 0's are not stops.
	d.selectedRun = 2
	d.moveTimelineSelection(1)
	if d.cursorLane() != 63 {
		t.Fatalf("↓ from round 1 landed on lane %d, want 63", d.cursorLane())
	}
}

// TestTimelineUnfoldsANestedFanOut: a lane that fanned out itself folds like
// a round — → unfolds its own lanes in place, one level deeper, and they are
// cursor stops that open like any lane; ← folds them away again.
func TestTimelineUnfoldsANestedFanOut(t *testing.T) {
	v := newTaskView(fanOutDetail(t))
	v.detail.loaded = true
	v.lanes = v.detail.laneRows
	v.tab = taskTabSteps
	v.detail.laneRows[0].Children = &apiclient.ChildrenRollup{Total: 2}
	v.detail.selectLaneRow(51, 2)

	lines := timelineText(v.detail)
	if row := lines[lineIndex(lines, "api")]; !strings.Contains(row, "▸") || !strings.Contains(row, "2 below") {
		t.Fatalf("a lane that fans out reads %q, want ▸ and its count", row)
	}

	// No client in this harness, so the fetch is the reply sent below.
	v.updateKey(registryKey(t, "right"))
	if !v.detail.laneOpen[51] {
		t.Fatal("→ on a lane that fans out did not unfold it")
	}
	// The fetch's reply, as the daemon would send it.
	parent := int64(51)
	kid := func(id int64, name string) apiclient.Task {
		return apiclient.Task{ID: id, State: stateRunning, ParentTaskID: &parent, LaneID: &name}
	}
	v.detail.update(laneKidsMsg{taskID: 40, parent: 51, kids: []apiclient.Task{kid(71, "api-auth"), kid(72, "api-rate")}})

	lines = timelineText(v.detail)
	api, auth, rate, web := lineIndex(lines, "▾"), lineIndex(lines, "api-auth"), lineIndex(lines, "api-rate"), lineIndex(lines, "web")
	if api < 0 || api >= auth || auth >= rate || rate >= web {
		t.Fatalf("nested lanes not unfolded under their lane:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Index(lines[auth], "↳") <= strings.Index(lines[api], "▾") {
		t.Fatalf("nested lane is not indented deeper than its parent lane:\n%s", strings.Join(lines, "\n"))
	}

	v.updateKey(registryKey(t, "down"))
	if v.detail.cursorLane() != 71 {
		t.Fatalf("↓ from the unfolded lane landed on %d, want the nested lane 71", v.detail.cursorLane())
	}
	msg, ok := v.updateKey(registryKey(t, "enter"))().(openTaskMsg)
	if !ok || msg.id != 71 {
		t.Fatalf("enter on a nested lane opened %+v, want task 71", msg)
	}

	v.detail.selectLaneRow(51, 2)
	v.updateKey(registryKey(t, "left"))
	if lineIndex(timelineText(v.detail), "api-auth") >= 0 {
		t.Fatal("← did not fold the nested lanes away")
	}
}

// TestLaneRowSaysWhereAnUnfinishedLaneIs: a running lane names its step, a
// finished one does not.
func TestLaneRowSaysWhereAnUnfinishedLaneIs(t *testing.T) {
	d := fanOutDetail(t)
	d.laneRows[0].StepTotal, d.laneRows[0].CurrentStep = 4, 3 // api: done
	d.laneRows[2].StepTotal, d.laneRows[2].CurrentStep, d.laneRows[2].StepName = 4, 1, "test"
	lines := timelineText(d)
	if row := lines[lineIndex(lines, "docs")]; !strings.Contains(row, "step 2/4 test") {
		t.Fatalf("running lane row %q does not name its step", row)
	}
	if row := lines[lineIndex(lines, "api")]; strings.Contains(row, "step") {
		t.Fatalf("finished lane row %q names a step", row)
	}
}
