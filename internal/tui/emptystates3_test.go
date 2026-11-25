package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// #1161: the add form @ opens with nothing registered fetches its workflow
// registry the way `a` does, or the picker offers only "(none)" on the very
// path a new user takes.
func TestProjectKeyAddFormLoadsItsWorkflows(t *testing.T) {
	p := newProjectsView()
	p.client = offlineClient()
	_, cmd := p.update(openProjectAddMsg{})
	if p.form == nil {
		t.Fatal("openProjectAddMsg did not open the add form")
	}
	if cmd == nil {
		t.Fatal("the add form opened by @ asked for no workflows")
	}
	if msg := runCmd(t, cmd, 10*time.Second); msg == nil {
		t.Fatal("the load returned nothing")
	} else if _, ok := msg.(pfWorkflowsMsg); !ok {
		t.Fatalf("the add form's load = %T, want pfWorkflowsMsg", msg)
	}
}

// #1161: a filter that hides every issue says so once, with how to clear
// it — not "No issues yet" over columns that each say "none match".
func TestIssuesFilterHidingEverythingSaysSoOnce(t *testing.T) {
	v := issuesFixture()
	v.filter.SetValue("zzz")
	v.clampCursor()
	out := ansi.Strip(v.render(120, 30))
	if strings.Contains(out, "No issues yet") {
		t.Errorf("a project with issues reads as having none:\n%s", out)
	}
	if n := strings.Count(out, "none match"); n != 1 {
		t.Errorf(`"none match" printed %d times, want once:\n%s`, n, out)
	}
	if !strings.Contains(out, `none match "zzz"`) || !strings.Contains(out, "esc clears") {
		t.Errorf("the message names neither the filter nor how to clear it:\n%s", out)
	}
}

// #1161: a project with no issues at all says "No issues yet" whether or not
// a filter is typed, and its columns do not repeat it as "none match".
func TestIssuesEmptyProjectSaysNoIssuesOnceUnderAFilter(t *testing.T) {
	v := issuesFixture()
	v.issues = nil
	v.filter.SetValue("zzz")
	v.clampCursor()
	out := ansi.Strip(v.render(120, 30))
	if !strings.Contains(out, "No issues yet") {
		t.Errorf("an empty project does not say so:\n%s", out)
	}
	if strings.Contains(out, "none match") {
		t.Errorf("an empty project also says none match:\n%s", out)
	}
}
