package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// `N` — the in-task next-failure key (task 129.18).
//
// A task that failed more than once, in several places, used to be walked by
// hand: ↑/↓ down the timeline, reading each row's glyph, then 3. `N` is that
// walk as one key. Its stops are derived here, client-side, from rows the
// workspace already holds — the attempts, their iterations, and the lane rows
// — so the key adds no API (decision 4).

// failureTarget is one stop of the cycle. run is the attempt it lands on; a
// lane target carries the blocked lane to open instead, and run is then the
// fan_out attempt the lane sits at, which is what places it in the order.
type failureTarget struct {
	run  apiclient.StepRun
	lane int64
}

// failedStates are the attempt states that are a failure of the step. A
// skipped, stopped or approved attempt is not one, and neither is a live one.
var failedStates = map[string]bool{"failed": true, "interrupted": true, "rejected": true}

// failureTargets are the task's failures in execution order (decision 3):
// each step's — or each loop iteration's — newest attempt when that attempt
// did not succeed, so a step that failed and then passed on retry is not one.
// A blocked lane sits at its fan_out step's position, one stop per lane in
// merge order; a fan_out with a blocked lane is reached through the lane, not
// through its own attempt.
func (d *detail) failureTargets() []failureTarget {
	runs := d.attempts()
	newest := make(map[string]int, len(runs))
	for i, r := range runs {
		k := stepRunKey(r)
		if j, ok := newest[k]; !ok || r.Attempt > runs[j].Attempt ||
			(r.Attempt == runs[j].Attempt && r.ID > runs[j].ID) {
			newest[k] = i
		}
	}
	fanOut, hasFanOut := d.newestFanOutRun()
	var out []failureTarget
	for i, r := range runs {
		if newest[stepRunKey(r)] != i {
			continue
		}
		if hasFanOut && r.ID == fanOut.ID {
			if lanes := d.blockedLanes(); len(lanes) > 0 {
				for _, id := range lanes {
					out = append(out, failureTarget{run: r, lane: id})
				}
				continue
			}
		}
		if r.FinishedAt != nil && failedStates[r.State] {
			out = append(out, failureTarget{run: r})
		}
	}
	return out
}

// blockedLanes are the lanes, in merge order, that stopped on a human.
func (d *detail) blockedLanes() []int64 {
	var out []int64
	for _, lane := range d.laneRows {
		if lane.State == stateBlocked {
			out = append(out, lane.ID)
		}
	}
	return out
}

// nextFailureTarget is the stop after the shared attempt cursor (088
// decision 7), wrapping to the first. ok=false when the task has no failures.
func (d *detail) nextFailureTarget() (failureTarget, bool) {
	targets := d.failureTargets()
	if len(targets) == 0 {
		return failureTarget{}, false
	}
	cursor := d.runIndex(d.selectedRun)
	for _, tg := range targets {
		if d.runIndex(tg.run.ID) > cursor {
			return tg, true
		}
	}
	return targets[0], true
}

// nextFailure is `N`: land on the next failure, or say there is none.
func (t *taskView) nextFailure() tea.Cmd {
	d := t.detail
	if !d.loaded {
		return nil
	}
	tg, ok := d.nextFailureTarget()
	if !ok {
		d.actions.setStatus("no failures in this task", false)
		return nil
	}
	if tg.lane != 0 {
		// Open the lane the way `l` does — the back stack is pushed, so esc
		// and U return here — and land on the lane's own first failure once
		// it has loaded (taskView.pendingFailure).
		from, id, pid := d.taskID, tg.lane, d.task.ProjectID
		return func() tea.Msg {
			return openTaskMsg{id: id, state: stateBlocked, from: from, failure: true, projectID: pid}
		}
	}
	return t.landOnFailure(tg.run)
}

// landOnFailure selects a failed attempt and shows it on the Output tab where
// it failed: the first check line for a check_failed attempt, the end for any
// other (#597's landAtFailure). A folded iteration or round is unfolded first,
// the way enter and → do, so the timeline shows what the cursor is on.
// Following is never set (T3.3).
//
// It is the one path to a failure: `N` and the Overview failure card's `3`
// link both come through here.
func (t *taskView) landOnFailure(run apiclient.StepRun) tea.Cmd {
	d := t.detail
	if loopIndexes(d.attempts())[run.StepIndex] {
		d.setFold(tierKey{run.StepIndex, run.Iteration}, true)
	}
	var cmds []tea.Cmd
	if t.laneSel != -1 {
		// The Output pane is the task's own again: a failure of this task is
		// not in a lane's transcript.
		t.laneSel = -1
		cmds = append(cmds, t.syncLaneDetail())
	}
	d.selectedRun = run.ID
	if run.ID == d.displayRun {
		// Already on screen: nothing will be fetched, so re-land here.
		d.following = false
		d.land = true
		d.outputDirty = true
	} else {
		cmds = append(cmds, d.syncOutput())
	}
	d.focus = focusOutput
	cmds = append(cmds, t.setTab(taskTabOutput))
	return tea.Batch(cmds...)
}

// landPendingFailure finishes a lane jump: once the lane `N` opened has
// loaded, land on its first failure. Called after every message; it does
// nothing until the pending lane is the loaded one.
func (t *taskView) landPendingFailure() tea.Cmd {
	if t.pendingFailure == 0 || t.detail.taskID != t.pendingFailure || !t.detail.loaded {
		return nil
	}
	t.pendingFailure = 0
	targets := t.detail.failureTargets()
	if len(targets) == 0 || targets[0].lane != 0 {
		// A lane that fanned out again is left at its Overview, whose
		// failure card names the deeper lane; N from there goes on.
		return nil
	}
	return t.landOnFailure(targets[0].run)
}
