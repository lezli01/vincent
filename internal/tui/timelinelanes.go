package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Lanes on the Steps timeline.
//
// A fan_out's lanes are child tasks (§7.6), and the timeline used to name
// them only through the fan_out attempt's rollup: reaching one meant `l`,
// which opened whichever lane the cursor's tab resolved to. Here each lane
// is a row of its own, indented under the fan_out step that spawned it, and
// a cursor stop like any attempt — so walking the steps walks the lanes too,
// and `enter` or `l` on a lane opens its workspace.
//
// A lane is not an attempt and the timeline's cursor is an attempt id, so
// the lane selection rides beside it: selectedLane names the lane, and
// laneAnchor the attempt the cursor stood on when the lane was chosen. The
// selection holds only while selectedRun still equals the anchor, so every
// other way of moving the cursor — a click on an attempt, `N`, a refresh
// tracking the live attempt — drops it without having to know it exists.

// timelineStop is one place ↑/↓ may land: an attempt (lane 0) or a lane row,
// which carries the position of the attempt it is anchored to.
type timelineStop struct {
	run  int
	lane int64
}

// cursorLane is the lane the timeline's cursor is on, or 0 when it is on an
// attempt.
func (d *detail) cursorLane() int64 {
	if d.selectedLane == 0 || d.selectedRun != d.laneAnchor {
		return 0
	}
	if _, ok := d.laneRow(d.selectedLane); !ok {
		return 0
	}
	return d.selectedLane
}

func (d *detail) laneRow(id int64) (apiclient.Task, bool) {
	for _, lane := range d.laneRows {
		if lane.ID == id {
			return lane, true
		}
	}
	for _, kids := range d.laneKids {
		for _, lane := range kids {
			if lane.ID == id {
				return lane, true
			}
		}
	}
	return apiclient.Task{}, false
}

// laneEntry is one drawn lane row: the lane and how many unfolded fan-outs
// deep it sits (0 for the task's own lanes).
type laneEntry struct {
	lane  apiclient.Task
	depth int
}

// laneTree is lanes in drawing order, each followed by its own lanes when
// the reader unfolded it — a nested fan-out read in place, without opening
// the lane (laneKids, laneOpen).
func (d *detail) laneTree(lanes []apiclient.Task) []laneEntry {
	var out []laneEntry
	var walk func(lanes []apiclient.Task, depth int)
	walk = func(lanes []apiclient.Task, depth int) {
		for _, lane := range lanes {
			out = append(out, laneEntry{lane, depth})
			// Bounded by the fan_out.max_depth the engine enforces at
			// creation, and by a lane never being its own descendant.
			if d.laneOpen[lane.ID] {
				walk(d.laneKids[lane.ID], depth+1)
			}
		}
	}
	walk(lanes, 0)
	return out
}

// laneHasLanes reports whether a lane fanned out itself, which is what makes
// its row unfoldable.
func laneHasLanes(lane apiclient.Task) bool {
	return lane.Children != nil && lane.Children.Total > 0
}

// laneKidsMsg is one lane's own lanes, fetched when its row unfolds.
type laneKidsMsg struct {
	taskID int64 // the workspace's task, so a reply for one already left is dropped
	parent int64
	kids   []apiclient.Task
	err    error
}

func (d *detail) laneKidsCmd(parent int64) tea.Cmd {
	client, taskID := d.client, d.taskID
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		kids, err := client.ListTasks(ctx, apiclient.ListTasksOptions{ParentID: parent})
		return laneKidsMsg{taskID: taskID, parent: parent, kids: cleanTasks(kids), err: err}
	}
}

func (d *detail) applyLaneKids(msg laneKidsMsg) {
	if msg.taskID != d.taskID || msg.err != nil {
		return
	}
	if d.laneKids == nil {
		d.laneKids = map[int64][]apiclient.Task{}
	}
	d.laneKids[msg.parent] = msg.kids
}

// setLaneOpen unfolds (→) or folds (←) the lane row under the cursor, and
// reports whether the cursor was on a lane that fans out — the keys mean the
// round folds everywhere else. Unfolding fetches the lane's own lanes.
func (d *detail) setLaneOpen(open bool) (tea.Cmd, bool) {
	id := d.cursorLane()
	lane, ok := d.laneRow(id)
	if id == 0 || !ok || !laneHasLanes(lane) {
		return nil, false
	}
	if d.laneOpen == nil {
		d.laneOpen = map[int64]bool{}
	}
	d.laneOpen[id] = open
	if open {
		return d.laneKidsCmd(id), true
	}
	return nil, true
}

// toggleLaneOpen is space on a lane row.
func (d *detail) toggleLaneOpen() (tea.Cmd, bool) {
	return d.setLaneOpen(!d.laneOpen[d.cursorLane()])
}

// nestedLaneNoteCmd re-reads an unfolded lane's own lanes when an event
// touches one of them, so a nested row follows its lane the way a direct one
// does.
func (d *detail) nestedLaneNoteCmd(id int64) tea.Cmd {
	for parent, kids := range d.laneKids {
		if !d.laneOpen[parent] {
			continue
		}
		for _, lane := range kids {
			if lane.ID == id {
				return d.laneKidsCmd(parent)
			}
		}
	}
	return nil
}

// resetLaneTree forgets every unfolded nested fan-out: they belong to the
// task being left.
func (d *detail) resetLaneTree() {
	d.laneKids, d.laneOpen = nil, nil
}

// selectLaneRow puts the cursor on a lane row, anchored to the attempt the
// row is drawn under.
func (d *detail) selectLaneRow(lane, anchor int64) {
	d.selectedRun, d.selectedLane, d.laneAnchor = anchor, lane, anchor
}

// laneSlot is where on the timeline a group of lanes is drawn: under one
// round's tier of a fan_out step, or — round endOfStep — after the step's
// last attempt.
type laneSlot struct {
	index int
	round int
}

// endOfStep is the slot after a step's last attempt: every lane of a step
// drawn without round tiers, and a lane whose round is unknown.
const endOfStep = -1

// laneSlots places the lane rows, in merge order. A lane goes under the
// fan_out step index that spawned it; a lane without a parent_step_index —
// served by a daemon older than the field — under the first fan_out step the
// timeline shows, which is right for every workflow that fans out once.
//
// A multi-round fan_out draws its rounds as tiers (task 080), and each lane
// sits inside the round that spawned it, so a folded round hides its lanes
// with its attempts. A lane without a lane_round — an older daemon again —
// has no tier to go in and closes the step instead.
func (d *detail) laneSlots(runs []apiclient.StepRun) map[laneSlot][]apiclient.Task {
	if len(d.laneRows) == 0 {
		return nil
	}
	fanOut := map[int]bool{}
	first := -1
	for _, r := range runs {
		if r.StepType == stepTypeFanOut {
			fanOut[r.StepIndex] = true
			if first < 0 {
				first = r.StepIndex
			}
		}
	}
	tiered := loopIndexes(runs)
	latest := latestIterations(runs)
	out := map[laneSlot][]apiclient.Task{}
	for _, lane := range d.laneRows {
		index := first
		if lane.ParentStepIndex != nil {
			index = *lane.ParentStepIndex
		}
		if index < 0 || !fanOut[index] {
			continue
		}
		slot := laneSlot{index, endOfStep}
		if tiered[index] && lane.LaneRound != nil {
			if !d.tierOpen(index, *lane.LaneRound, latest[index]) {
				continue // folded with its round
			}
			slot.round = *lane.LaneRound
		}
		out[slot] = append(out[slot], lane)
	}
	return out
}

// withLaneStops splices the lanes into the attempt stops in the order they
// are drawn: a round's lanes after that round's last stop, a step's
// end-of-step lanes after its last stop, each anchored to the stop before.
func withLaneStops(
	runs []apiclient.StepRun, stops []int, lanes map[laneSlot][]apiclient.Task,
	tree func([]apiclient.Task) []laneEntry,
) []timelineStop {
	out := make([]timelineStop, 0, len(stops))
	add := func(s int, slot laneSlot) {
		for _, e := range tree(lanes[slot]) {
			out = append(out, timelineStop{run: s, lane: e.lane.ID})
		}
	}
	for p, s := range stops {
		out = append(out, timelineStop{run: s})
		r := runs[s]
		last := p+1 == len(stops)
		var next apiclient.StepRun
		if !last {
			next = runs[stops[p+1]]
		}
		if last || next.StepIndex != r.StepIndex || next.Iteration != r.Iteration {
			add(s, laneSlot{r.StepIndex, r.Iteration})
		}
		if last || next.StepIndex != r.StepIndex {
			add(s, laneSlot{r.StepIndex, endOfStep})
		}
	}
	return out
}

// laneTimelineLines are the rows a fan_out step's lanes add under it: a dim
// heading, then one row per lane in merge order. ids carries -lane.ID for
// each lane row, which is how a click names a lane rather than an attempt.
//
// indent is the depth of the rows they sit among: a lane inside a round's
// tier is indented as that tier's attempts are.
func (d *detail) laneTimelineLines(lanes []apiclient.Task, cursor int64, indent string) (lines []string, ids []int64, sel int) {
	sel = -1
	if len(lanes) == 0 {
		return nil, nil, sel
	}
	lines = append(lines, styleDim.Render(fmt.Sprintf("%s    · lanes (%d)", indent, len(lanes))))
	ids = append(ids, 0)
	for _, e := range d.laneTree(lanes) {
		lane := e.lane
		glyph := taskStateGlyph(lane.State)
		if glyph == "" {
			glyph = " "
		}
		// A lane that fanned out itself folds like a round does: ▸ shut,
		// ▾ open, with how many tasks sit below it.
		fold := "↳"
		if laneHasLanes(lane) {
			fold = "▸"
			if d.laneOpen[lane.ID] {
				fold = "▾"
			}
		}
		pad := indent + strings.Repeat("  ", e.depth)
		row := fmt.Sprintf("%s      %s %s %s  %s  #%d", pad, fold, glyph, laneName(lane), stateWords(lane.State), lane.ID)
		if step := laneStepClause(lane); step != "" {
			row += "  · " + step
		}
		if laneHasLanes(lane) {
			row += fmt.Sprintf("  · %d below", lane.Children.Total)
		}
		if lane.BlockReason != nil && *lane.BlockReason != "" {
			row += "  " + *lane.BlockReason
		}
		if d.width > 0 {
			row = ansi.Truncate(row, d.width, "…")
		}
		if lane.ID == cursor {
			sel = len(lines)
			row = styleSelected.Render(row)
		} else {
			row = laneRowStyle(lane).Render(row)
		}
		lines = append(lines, row)
		ids = append(ids, -lane.ID)
	}
	return lines, ids, sel
}

// hasFanOut reports whether the task has run a fan_out step, which is when
// its own events may mean new lanes.
func (d *detail) hasFanOut() bool {
	for _, r := range d.attempts() {
		if r.StepType == stepTypeFanOut {
			return true
		}
	}
	return false
}

// laneStepClause is where an unfinished lane has got to — `step 2/4 test` —
// from the list row's own step fields, so the row says how far a lane is
// without opening it. A finished lane says nothing: its state already did.
func laneStepClause(lane apiclient.Task) string {
	if lane.StepTotal <= 0 || lane.State == stateDone || lane.State == stateAborted {
		return ""
	}
	clause := fmt.Sprintf("step %d/%d", min(lane.CurrentStep+1, lane.StepTotal), lane.StepTotal)
	if lane.StepName != "" {
		clause += " " + oneLine(lane.StepName)
	}
	return clause
}
