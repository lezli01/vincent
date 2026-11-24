package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// #1064: in the narrow layout a click on a row selects it, through the
// root's frame and chrome, and the wheel moves the selection.
func TestPullRequestsNarrowClickAndWheel(t *testing.T) {
	v := pullRequestsFixture(testPull(11, "first change"), testPull(12, "second change"), testPull(13, "third change"))
	v.loaded = true
	m := connectedRoot(t) // 120 columns: under the wide layout's floor
	m.views[viewPullRequests] = v
	m.switchNow(viewPullRequests)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if v.wide() {
		t.Fatal("the fixture is wide; the test is about the narrow table")
	}
	screen := strings.Split(ansi.Strip(m.View().Content), "\n")
	y, x := -1, -1
	for i, line := range screen {
		if at := strings.Index(line, "third change"); at >= 0 {
			y, x = i, ansi.StringWidth(line[:at])
			break
		}
	}
	if y < 0 {
		t.Fatalf("no third row on screen:\n%s", strings.Join(screen, "\n"))
	}
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if got := v.currentKey(); got != "octo/api#13" {
		t.Fatalf("a click on the third row selected %s", got)
	}
	m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelUp})
	if got := v.currentKey(); got != "octo/api#12" {
		t.Fatalf("the wheel moved the selection to %s, want #12", got)
	}
}

// #1064: enter on an unclaimed pull request says which keys give it a task,
// and a picker keeps the wide layout.
func TestPullRequestsEnterOnUnclaimedAndWidePicker(t *testing.T) {
	v := pullRequestsFixture(testPull(12, "unclaimed"))
	v.loaded = true
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(v.note, "creates one from it") || !strings.Contains(v.note, "l links") {
		t.Fatalf("enter on an unclaimed pull request said %q", v.note)
	}
	v.termW = 200
	if !v.wide() {
		t.Fatal("the fixture is not wide at 200 columns")
	}
	v.openLinkPicker()
	if v.picker == nil {
		t.Fatal("l opened no picker")
	}
	if !v.wide() {
		t.Error("the picker flipped the screen to the narrow table")
	}
	if got := ansi.Strip(v.render(200, 40)); !strings.Contains(got, "enter link") {
		t.Errorf("the wide layout does not draw the picker:\n%s", got)
	}
}
