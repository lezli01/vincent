package tui

import (
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The pane takes only the fetch for the scope it is on (task 168.6): a whole
// task's diff landing after `d` moved it to one attempt is dropped.
func TestDiffScopeDropsAFetchForTheScopeItLeft(t *testing.T) {
	p := newDiffPane()
	p.openTask(7)
	if !p.setScope(31, "verify · attempt 2") {
		t.Fatal("setScope reported no change")
	}
	p.apply(diffLoadedMsg{taskID: 7, text: "diff --git a/x b/x\n"})
	if p.loaded {
		t.Fatal("the whole task's diff landed on a pane scoped to an attempt")
	}
	p.apply(diffLoadedMsg{taskID: 7, stepRun: 31, text: "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"})
	if !p.loaded {
		t.Fatal("the attempt's own diff did not land")
	}
	if got := ansi.Strip(p.render(80, 10)); !strings.Contains(got, "verify · attempt 2") {
		t.Errorf("the summary does not name the attempt:\n%s", got)
	}
	// Another task is no scope for this one.
	p.openTask(8)
	if p.stepRun != 0 {
		t.Errorf("opening another task kept the scope %d", p.stepRun)
	}
}

// An attempt from before the range was recorded says so rather than
// reading as a failure.
func TestDiffScopeExplainsAnUnrecordedRange(t *testing.T) {
	p := newDiffPane()
	p.openTask(7)
	p.setScope(31, "verify · attempt 1")
	p.apply(diffLoadedMsg{taskID: 7, stepRun: 31, err: &apiclient.Error{
		Status: http.StatusConflict, Code: apiclient.CodeStepRangeUnrecorded, Message: "no range",
	}})
	if got := p.errorText(); !strings.Contains(got, "no recorded commit range") {
		t.Errorf("errorText = %q", got)
	}
}

// d on the Diff tab scopes it to the selected attempt and back, a new
// selection moves a scoped Diff with it, the summary names the base for the
// whole task, and the tab's marker keeps the whole task's size.
func TestDiffScopeFollowsTheSelection(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabOverview)
	v.detail.task.BaseBranch = "main"
	v.setTab(taskTabDiff)
	runs := v.detail.attempts()
	if len(runs) < 2 {
		t.Fatal("the fixture needs two attempts")
	}
	v.detail.diff.apply(diffLoadedMsg{taskID: v.detail.taskID, sections: []apiclient.DiffSection{{
		Remainder: true, Diff: "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1,2 @@\n-a\n+b\n+c\n",
	}}})
	if got := ansi.Strip(v.detail.diff.summary()); !strings.Contains(got, "whole task · against main") {
		t.Errorf("summary = %q, want it to name the base", got)
	}
	v.detail.selectedRun = runs[0].ID
	v.updateKey(registryKey(t, "d"))
	if v.detail.diff.stepRun != runs[0].ID {
		t.Fatalf("d scoped the diff to %d, want %d", v.detail.diff.stepRun, runs[0].ID)
	}
	v.detail.selectedRun = runs[1].ID
	v.update(tea.KeyPressMsg{Code: 'x', Text: "x"}) // any message runs the sync
	if v.detail.diff.stepRun != runs[1].ID {
		t.Errorf("the scoped diff stayed on %d after the selection moved to %d", v.detail.diff.stepRun, runs[1].ID)
	}
	if got := ansi.Strip(v.tabMarker(taskTabDiff)); got != "+2 −1" {
		t.Errorf("the tab marker = %q, want the whole task's +2 −1", got)
	}
}
