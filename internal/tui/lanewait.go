package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/reasons"
)

// What a parent waiting on its lanes is waiting on (#1067). The lane list
// says each lane's state and step but not its attempt, and "it seems to
// hang" was a lane on its sixth attempt at a step whose check had failed the
// same way since the second. So the parent's Overview reads each unsettled
// lane's own attempts, once per change of the lane, and says which step and
// attempt it is on and when that attempt repeats a failure.

// maxWaitLanes bounds the lane reads one parent's Overview issues: past it
// the section names the rest by state alone.
const maxWaitLanes = 8

// waitLanesShown is how many working lanes "Waiting on" names before it
// counts the rest: the section is there to say what a parent waits on, and
// a 30-lane fan-out listing all of them pushed the frame's link off screen.
const waitLanesShown = 4

// repeatedFailure describes the run of failures behind run at its step: the
// earlier attempts at the same step (and loop iteration) that failed, newest
// back, for as long as they failed with the same reason. It is empty unless
// that reason has now been seen twice — counting run itself when it failed.
// For a failed run it reads "same failure as attempts 2–5"; for one still
// going, "attempts 2–5 each failed: check failed".
func repeatedFailure(steps []apiclient.StepRun, run apiclient.StepRun) string {
	var earlier []apiclient.StepRun
	for _, r := range steps {
		if r.ID != run.ID && r.StepIndex == run.StepIndex && r.StepID == run.StepID &&
			r.Iteration == run.Iteration && r.Attempt < run.Attempt {
			earlier = append(earlier, r)
		}
	}
	sort.Slice(earlier, func(i, j int) bool { return earlier[i].Attempt > earlier[j].Attempt })
	reasonOf := func(r apiclient.StepRun) string {
		if r.State == "failed" && r.FailureReason != nil {
			return *r.FailureReason
		}
		return ""
	}
	reason := reasonOf(run)
	failed := reason != ""
	if !failed && len(earlier) > 0 {
		reason = reasonOf(earlier[0])
	}
	if reason == "" {
		return ""
	}
	var same []int
	for _, r := range earlier {
		if reasonOf(r) != reason {
			break
		}
		same = append(same, r.Attempt)
	}
	total := len(same)
	if failed {
		total++
	}
	if total < 2 || len(same) == 0 {
		return ""
	}
	first, last := same[len(same)-1], same[0]
	span := "attempt " + strconv.Itoa(first)
	if first != last {
		span = fmt.Sprintf("attempts %d–%d", first, last)
	}
	if failed {
		return "same failure as " + span
	}
	if first == last {
		return span + " failed: " + reasons.Explain(reason).Title
	}
	return span + " each failed: " + reasons.Explain(reason).Title
}

// laneAt is what one lane's own read says about where it is.
type laneAt struct {
	updated time.Time
	run     apiclient.StepRun
	hasRun  bool
	repeat  string
}

// laneAtMsg carries one lane's read.
type laneAtMsg struct {
	parentID, laneID int64
	at               laneAt
	err              error
}

// laneFinished is a lane that will never run again: unlike laneSettled it
// leaves a blocked lane in, which is still work the parent waits on.
func laneFinished(state string) bool {
	return state == stateDone || state == stateAborted || state == stateArchived
}

// syncLaneAts reads, for a parent waiting on its lanes, every unsettled
// lane whose row changed since it was last read — at most maxWaitLanes of
// them. It runs after the lane list lands.
func (t *taskView) syncLaneAts() tea.Cmd {
	client, parent := t.detail.client, t.detail.taskID
	if client == nil || !t.detail.loaded || t.detail.task.State != stateAwaitingChildren {
		return nil
	}
	if t.laneAts == nil || t.laneAtsParent != parent {
		t.laneAts, t.laneAtsParent = map[int64]laneAt{}, parent
	}
	var cmds []tea.Cmd
	read := 0
	for _, lane := range t.lanes {
		if laneFinished(lane.State) {
			continue
		}
		if read++; read > maxWaitLanes {
			break
		}
		if at, ok := t.laneAts[lane.ID]; ok && at.updated.Equal(lane.UpdatedAt) {
			continue
		}
		// Recorded before the read lands, so a burst of lane events reads it
		// once.
		t.laneAts[lane.ID] = laneAt{updated: lane.UpdatedAt}
		id, updated := lane.ID, lane.UpdatedAt
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			d, err := client.GetTask(ctx, id)
			if err != nil {
				return laneAtMsg{parentID: parent, laneID: id, err: err}
			}
			at := laneAt{updated: updated}
			at.run, at.hasRun = cardRun(d)
			if at.hasRun {
				at.repeat = repeatedFailure(d.Steps, at.run)
			}
			return laneAtMsg{parentID: parent, laneID: id, at: at}
		})
	}
	return tea.Batch(cmds...)
}

func (t *taskView) applyLaneAt(msg laneAtMsg) {
	if msg.parentID != t.laneAtsParent || t.laneAts == nil || msg.err != nil {
		return
	}
	if cur, ok := t.laneAts[msg.laneID]; ok && !cur.updated.Equal(msg.at.updated) {
		return // a newer read is on its way
	}
	t.laneAts[msg.laneID] = msg.at
}

// laneAtText is where a lane is: `step 2 unit-implement · attempt 6 ·
// attempts 2–5 each failed: check failed`, or its step alone before its
// attempts have been read.
func (t *taskView) laneAtText(lane apiclient.Task) string {
	at, ok := t.laneAts[lane.ID]
	if ok && at.hasRun {
		s := fmt.Sprintf("step %d %s · attempt %d", at.run.StepIndex+1, stepLabel(at.run), at.run.Attempt)
		if at.repeat != "" {
			s += " · " + at.repeat
		}
		return s
	}
	if k, _, ok := lane.StepDisplay(); ok {
		s := "step " + strconv.Itoa(k)
		if lane.StepName != "" {
			s += " " + lane.StepName
		}
		return s
	}
	return ""
}

// waitingOnLines are a parent's lanes still at work and not waiting on a
// human — the ones "Lanes that need you" does not list — with where each
// is. Empty when every unsettled lane needs a human, or there is none.
func (t *taskView) waitingOnLines(width int) []string {
	var rows []apiclient.Task
	for _, lane := range t.lanes {
		if laneFinished(lane.State) || needsAttention(lane.State) {
			continue
		}
		rows = append(rows, lane)
	}
	if len(rows) == 0 {
		return nil
	}
	out := []string{"", section("Waiting on"), ""}
	more := 0
	if len(rows) > waitLanesShown {
		rows, more = rows[:waitLanesShown], len(rows)-waitLanesShown
	}
	for _, lane := range rows {
		line := "  " + toneCyan.Render(taskStateGlyph(lane.State)+" "+laneName(lane)) + "  " +
			styleDim.Render(fmt.Sprintf("#%d · %s", lane.ID, stateWords(lane.State)))
		if at := t.laneAtText(lane); at != "" {
			line += styleDim.Render(" · ") + at
		}
		out = append(out, ansi.Truncate(line, max(width, 1), "…"))
	}
	if more > 0 {
		out = append(out, styleDim.Render("  … "+plural(more, "more lane", "more lanes")+" · the Steps tab lists every lane"))
	}
	return out
}
