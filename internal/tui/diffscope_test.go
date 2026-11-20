package tui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The pane takes only the fetch for the scope it is on (task 164.6): a whole
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
