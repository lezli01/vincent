package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

// The strip has no digits (task 168 decision 3): a digit moves nowhere, and
// `i` opens the linked issue from every tab but Pull request's.
func TestDigitsNoLongerSwitchTabs(t *testing.T) {
	v := pullTabFixture(t)
	for _, key := range []string{"0", "1", "2", "3", "4", "5", "6", "7"} {
		v.tab = taskTabWorkflow
		v.updateKey(synthKey(key))
		if v.tab != taskTabWorkflow {
			t.Errorf("%s moved to %v", key, v.tab)
		}
	}
}

func TestIssueKeyOpensTheLinkedIssue(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabOutput)
	if cmd := v.updateKey(synthKey("i")); cmd != nil {
		if _, ok := cmd().(openIssueMsg); ok {
			t.Fatal("i opened an issue on a task with none")
		}
	}
	v.detail.task.Issue = &apiclient.TaskIssue{ID: 8, Title: "flicker", State: "open"}
	cmd := v.updateKey(synthKey("i"))
	if cmd == nil {
		t.Fatal("i did nothing on a task with an issue")
	}
	msg, ok := cmd().(openIssueMsg)
	if !ok || msg.id != 8 || msg.back != viewTask {
		t.Fatalf("i sent %#v, want issue 8 with the way back", cmd())
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
// then switches: on a blocked task that is the attempt the failure card
// names — the newest one at the current step that did not succeed —
// whatever the reader had selected before.
func TestOverviewLinkMovesTheCursorThenTheTab(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabOverview)
	v.detail.task.State = stateBlocked
	v.detail.selectedRun = 2
	v.updateKey(synthKey("enter"))
	if v.tab != taskTabOutput || v.detail.selectedRun != 1 {
		t.Fatalf("enter left tab %v, cursor %d; want %v, 1", v.tab, v.detail.selectedRun, taskTabOutput)
	}
}

// A state change while the Overview is open swaps the frame and leaves the
// attempt cursor where the reader put it.
func TestOverviewStateChangeSwapsFrameNotCursor(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabOverview)
	v.detail.task.State = stateRunning
	v.detail.selectedRun = 1
	if got := ansi.Strip(v.render(100, 30)); !strings.Contains(got, "enter full output") {
		t.Fatalf("running overview misses its link:\n%s", got)
	}
	reason := "check_failed"
	v.detail.task.State = stateBlocked
	v.detail.task.BlockReason = &reason
	v.detail.task.AvailableActions = []string{apiclient.ActionRetry, apiclient.ActionCancel}
	got := ansi.Strip(v.render(100, 30))
	for _, want := range []string{"Blocked", "check failed  check_failed", "retry", "enter output of attempt 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("blocked overview misses %q:\n%s", want, got)
		}
	}
	if v.detail.selectedRun != 1 {
		t.Fatalf("the state change moved the cursor to %d", v.detail.selectedRun)
	}
	v.detail.task.State = stateDone
	got = ansi.Strip(v.render(100, 30))
	for _, want := range []string{"Done", "Outcome", "cost", "enter diff"} {
		if !strings.Contains(got, want) {
			t.Errorf("done overview misses %q:\n%s", want, got)
		}
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
	if got := hints(); !strings.Contains(got, "enter full output") || strings.Contains(got, "enter diff") {
		t.Errorf("running hints = %q", got)
	}
	v.detail.task.State = stateDone
	if got := hints(); !strings.Contains(got, "enter diff") || strings.Contains(got, "full output") {
		t.Errorf("done hints = %q", got)
	}
}

// The strip fits 80 columns with every tab on it, names each tab without a
// digit, and marks the tab on screen without colour (an underline and bold
// survive the ASCII profile's loss of colour only as text, so the test reads
// the names).
func TestTabStripFitsAndReadsWithoutColour(t *testing.T) {
	v := pullTabFixture(t)
	v.tab = taskTabOverview
	v.width = 78 // an 80-column terminal inside the root frame
	strip := v.renderTabs()
	if w := ansi.StringWidth(strip); w > 78 {
		t.Fatalf("strip is %d wide at 80 columns:\n%s", w, ansi.Strip(strip))
	}
	plain := ansi.Strip(strip)
	for _, want := range []string{"Overview", "Output", "Diff", "Pull request"} {
		if !strings.Contains(plain, want) {
			t.Errorf("strip misses %q: %s", want, plain)
		}
	}
	for _, digit := range []string{"0 ", "3 ", "4 ", "7 "} {
		if strings.Contains(plain, digit+"O") || strings.Contains(plain, digit+"D") {
			t.Errorf("strip still carries the digit %q: %s", digit, plain)
		}
	}
	// The hit map follows the drawn geometry: a click on each tab selects it.
	for _, hit := range v.tabHits {
		v.tab = taskTabWorkflow
		v.updateClick(tea.MouseClickMsg{X: hit.x0, Y: v.tabY})
		if v.tab != hit.tab {
			t.Errorf("a click at column %d selected %v, want %v", hit.x0, v.tab, hit.tab)
		}
	}
}

// A running task's Overview pairs what it is doing now with what it is for,
// and its Linked rows open with ↑/↓ and enter (task 168.7).
func TestOverviewNowAndLinkedCards(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabOverview)
	v.detail.task.State = stateRunning
	v.detail.task.Description = "Return a JSON body with every 429."
	v.detail.task.Issue = &apiclient.TaskIssue{ID: 8, Title: "429 has no body", State: "open"}
	got := ansi.Strip(v.render(140, 60))
	for _, want := range []string{"Now", "The task", "Return a JSON body", "Linked", "Usage", "#8 429 has no body"} {
		if !strings.Contains(got, want) {
			t.Errorf("the Overview misses %q:\n%s", want, got)
		}
	}
	v.updateKey(synthKey("down"))
	if !strings.Contains(ansi.Strip(v.render(140, 60)), "› ◇ issue") {
		t.Errorf("the Linked cursor is not drawn:\n%s", ansi.Strip(v.render(140, 60)))
	}
	v.updateKey(synthKey("up"))
	if v.linkedSel != 0 {
		t.Errorf("up from the first row left the cursor on %d, want off the card", v.linkedSel)
	}
	if v.updateKey(synthKey("enter")); v.tab == taskTabOverview {
		t.Errorf("enter off the card did not follow the frame's link (tab %v)", v.tab)
	}
}
