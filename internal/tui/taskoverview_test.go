package tui

import (
	"bytes"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Every §6 state picks a frame, awaiting_children among the "what is it
// doing" ones (task 129.12 decision 4).
func TestOverviewFrameForEveryState(t *testing.T) {
	want := map[string]overviewFrame{
		stateQueued:           frameProgress,
		stateRunning:          frameProgress,
		statePaused:           frameProgress,
		stateAwaitingChildren: frameProgress,
		stateBlocked:          frameAttention,
		stateAwaitingInput:    frameAttention,
		stateAwaitingGate:     frameAttention,
		stateAborted:          frameAttention,
		stateDone:             frameOutcome,
		stateArchived:         frameOutcome,
	}
	for state, frame := range want {
		if got := overviewFrameFor(state); got != frame {
			t.Errorf("%s: frame %d, want %d", state, got, frame)
		}
	}
}

// A fresh open lands on the Overview, whatever tab the workspace was on.
func TestSelectTaskLandsOnOverview(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabDetails)
	v.update(selectTaskMsg{id: 9, state: stateBlocked})
	if v.tab != taskTabOverview {
		t.Fatalf("a fresh open landed on %v, want %v", v.tab, taskTabOverview)
	}
}

// `0` reaches the Overview from every tab, and every other digit still means
// the tab it meant before (decision 2: digits bind to tabs).
func TestDigitsBindToTabs(t *testing.T) {
	v := pullTabFixture(t)
	want := map[string]taskViewTab{
		"0": taskTabOverview, "1": taskTabSteps, "2": taskTabDetails, "3": taskTabOutput,
		"4": taskTabDiff, "5": taskTabWorkflow, "6": taskTabStepDetails, "7": taskTabPull,
	}
	for key, tab := range want {
		v.tab = taskTabDetails
		if tab == taskTabDetails {
			v.tab = taskTabSteps
		}
		v.updateKey(synthKey(key))
		if v.tab != tab {
			t.Errorf("%s moved to %v, want %v", key, v.tab, tab)
		}
	}
	// 7 without a pull request does nothing.
	v = tabbedTaskFixture(t, taskTabOverview)
	v.updateKey(synthKey("7"))
	if v.tab != taskTabOverview {
		t.Errorf("7 with no pull request moved to %v", v.tab)
	}
}

// A pop restores the tab the reader left that task on; a jump still lands
// the task it opens on the Overview.
func TestPopRestoresTheTabLeft(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabDiff)
	v.pushTask(4)
	v.update(selectTaskMsg{id: 42})
	if v.tab != taskTabOverview {
		t.Fatalf("the jump landed on %v, want %v", v.tab, taskTabOverview)
	}
	msg := v.applyPop(navPopMsg{id: 4, ok: true})()
	v.update(msg)
	if v.tab != taskTabDiff {
		t.Fatalf("the pop landed on %v, want the %v tab it left", v.tab, taskTabDiff)
	}
}

// The pull requests takeover's create route lands where its form belongs.
func TestCreatePullRouteLandsOnThePullRequestTab(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabSteps)
	v.update(selectTaskMsg{id: 9, openPR: true})
	if v.tab != taskTabPull {
		t.Fatalf("the create route landed on %v, want %v", v.tab, taskTabPull)
	}
	if !v.pullFormPending {
		t.Fatal("the create intent was dropped")
	}
}

// A link moves the shared attempt cursor to the attempt the frame is about,
// then switches: on a blocked task that is the newest attempt, the one the
// block is on, whatever the reader had selected before.
func TestOverviewLinkMovesTheCursorThenTheTab(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabOverview)
	v.detail.task.State = stateBlocked
	v.detail.selectedRun = 1
	v.updateKey(synthKey("3"))
	if v.tab != taskTabOutput || v.detail.selectedRun != 2 {
		t.Fatalf("3 left tab %v, cursor %d; want %v, 2", v.tab, v.detail.selectedRun, taskTabOutput)
	}
	// The same digit elsewhere only switches.
	v.tab = taskTabDetails
	v.detail.selectedRun = 1
	v.updateKey(synthKey("3"))
	if v.tab != taskTabOutput || v.detail.selectedRun != 1 {
		t.Fatalf("3 from %v moved the cursor to %d", taskTabDetails, v.detail.selectedRun)
	}
}

// A state change while the Overview is open swaps the frame and leaves the
// attempt cursor where the reader put it.
func TestOverviewStateChangeSwapsFrameNotCursor(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabOverview)
	v.detail.task.State = stateRunning
	v.detail.selectedRun = 1
	if got := ansi.Strip(v.render(100, 30)); !strings.Contains(got, "3 full output") {
		t.Fatalf("running overview misses its link:\n%s", got)
	}
	reason := "check_failed"
	v.detail.task.State = stateBlocked
	v.detail.task.BlockReason = &reason
	v.detail.task.AvailableActions = []string{apiclient.ActionRetry, apiclient.ActionCancel}
	got := ansi.Strip(v.render(100, 30))
	for _, want := range []string{"Blocked", "check failed (check_failed)", "retry", "3 output of attempt 2", "6 what it was given"} {
		if !strings.Contains(got, want) {
			t.Errorf("blocked overview misses %q:\n%s", want, got)
		}
	}
	if v.detail.selectedRun != 1 {
		t.Fatalf("the state change moved the cursor to %d", v.detail.selectedRun)
	}
	v.detail.task.State = stateDone
	got = ansi.Strip(v.render(100, 30))
	for _, want := range []string{"Done", "cost", "branch", "4 diff"} {
		if !strings.Contains(got, want) {
			t.Errorf("done overview misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "7 PR") {
		t.Errorf("done overview links a pull request that is not linked:\n%s", got)
	}
}

// The footer lists exactly the links the frame draws.
func TestOverviewFooterFollowsTheFrame(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabOverview)
	hints := func() string {
		var out []string
		for _, b := range v.liveBindings(bindingsFor(ctxTaskOverview)) {
			out = append(out, b.hint)
		}
		return strings.Join(out, "|")
	}
	v.detail.task.State = stateRunning
	if got := hints(); !strings.Contains(got, "3 full output") || strings.Contains(got, "4 diff") {
		t.Errorf("running hints = %q", got)
	}
	v.detail.task.State = stateDone
	if got := hints(); !strings.Contains(got, "4 diff") || strings.Contains(got, "3 ") || strings.Contains(got, "7 PR") {
		t.Errorf("done hints = %q", got)
	}
}

// The strip fits 80 columns with every tab on it, never abbreviates the
// primary group, and keeps its two groups apart without colour.
func TestTabStripFitsAndReadsWithoutColour(t *testing.T) {
	v := pullTabFixture(t)
	v.tab = taskTabOverview
	v.width = 78 // an 80-column terminal inside the root frame
	strip := v.renderTabs()
	if w := ansi.StringWidth(strip); w > 78 {
		t.Fatalf("strip is %d wide at 80 columns:\n%s", w, ansi.Strip(strip))
	}
	plain := ansi.Strip(strip)
	for _, want := range []string{"0 Overview", "3 Output", "4 Diff", "7 "} {
		if !strings.Contains(plain, want) {
			t.Errorf("strip misses %q: %s", want, plain)
		}
	}
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ASCII}
	if _, err := w.Write([]byte(strip)); err != nil {
		t.Fatal(err)
	}
	ascii := ansi.Strip(buf.String())
	primary, secondary, ok := strings.Cut(ascii, strings.TrimSpace(tabSepGroups))
	if !ok || !strings.Contains(primary, "4 Diff") || !strings.Contains(secondary, "1 ") {
		t.Errorf("the groups are not told apart without colour: %q", ascii)
	}
	// The hit map follows the drawn geometry: a click on each box selects it.
	for _, hit := range v.tabHits {
		v.tab = taskTabDetails
		v.updateClick(tea.MouseClickMsg{X: hit.x0, Y: 1})
		if v.tab != hit.tab {
			t.Errorf("a click at column %d selected %v, want %v", hit.x0, v.tab, hit.tab)
		}
	}
}
