package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// draftedNewTask is a connected root on the new-task form with a title
// typed: a touched draft.
func draftedNewTask(t *testing.T) (*root, *newTask) {
	t.Helper()
	m := connectedRoot(t)
	m.switchNow(viewNewTask)
	n := m.views[viewNewTask].(*newTask)
	n.opened, n.touched = true, true
	n.titleIn.SetValue("half a thought")
	return m, n
}

// #1051: q on a touched new-task form asks before the draft goes; n keeps
// it, y quits.
func TestQuitAsksBeforeDroppingATaskDraft(t *testing.T) {
	m, n := draftedNewTask(t)
	if _, cmd := m.Update(key("q")); isQuit(cmd) {
		t.Fatal("q quit over a touched draft without asking")
	}
	if m.leave == nil || !strings.Contains(ansi.Strip(m.View().Content), "discard the task draft and quit? y/n") {
		t.Fatalf("q did not ask: leave %+v", m.leave)
	}
	m.Update(key("n"))
	if m.leave != nil || n.titleIn.Value() != "half a thought" || m.active != viewNewTask {
		t.Fatal("n did not keep the draft where it was")
	}
	m.Update(key("q"))
	if _, cmd := m.Update(key("y")); !isQuit(cmd) {
		t.Fatal("y did not quit")
	}
}

// #1051: a screen switch asks too, and y makes it.
func TestScreenSwitchAsksBeforeDroppingATaskDraft(t *testing.T) {
	m, _ := draftedNewTask(t)
	m.Update(selectViewMsg{id: viewIssues})
	if m.active != viewNewTask || m.leave == nil || m.leave.to != viewIssues {
		t.Fatalf("the switch went ahead: active %v, leave %+v", m.active, m.leave)
	}
	m.Update(key("esc"))
	if m.leave != nil || m.active != viewNewTask {
		t.Fatal("esc did not keep the form")
	}
	m.Update(selectViewMsg{id: viewIssues})
	m.Update(key("y"))
	if m.active != viewIssues {
		t.Fatalf("y did not switch: active %v", m.active)
	}
}

// Nothing to lose, nothing to ask: an untouched form, and one whose create
// is in flight — the create's own landing is the switch it causes.
func TestLeaveDoesNotAskWithoutADraft(t *testing.T) {
	m, n := draftedNewTask(t)
	n.submitting = true
	m.Update(selectViewMsg{id: viewIssues})
	if m.leave != nil || m.active != viewIssues {
		t.Fatal("a submitting form held the switch")
	}
	m.switchNow(viewNewTask)
	n.submitting, n.touched = false, false
	if _, cmd := m.Update(key("q")); !isQuit(cmd) {
		t.Fatal("q did not quit from an untouched form")
	}
}

// #1051: esc on a touched project form asks, as the new-task form does;
// an untouched one closes at once.
func TestProjectFormEscAsksWhenTouched(t *testing.T) {
	f := newProjectForm(nil, nil)
	if cmd := f.updateKey(key("esc")); cmd == nil {
		t.Fatal("esc on an untouched form did not close it")
	}
	f = newProjectForm(nil, nil)
	f.name.SetValue("renamed")
	if cmd := f.updateKey(key("esc")); cmd != nil || !f.confirming {
		t.Fatal("esc on a touched form closed it without asking")
	}
	if !strings.Contains(f.render(100), "discard these changes? y/n") {
		t.Errorf("the question is not on screen:\n%s", f.render(100))
	}
	if cmd := f.updateKey(key("n")); cmd != nil || f.confirming {
		t.Fatal("n did not keep the form")
	}
	f.updateKey(key("esc"))
	cmd := f.updateKey(key("y"))
	if cmd == nil {
		t.Fatal("y did not close the form")
	}
	if _, ok := cmd().(projectFormClosedMsg); !ok {
		t.Fatal("y did not close the form")
	}
}

// #1051: an edit form is touched only when what it would send changed.
func TestProjectFormDirtyIsWhatItWouldSend(t *testing.T) {
	p := testProject(1, "vincent")
	f := newProjectForm(nil, &p)
	if f.dirty() {
		t.Fatal("an edit form is dirty before a key is pressed")
	}
	f.name.SetValue("other")
	if !f.dirty() {
		t.Fatal("a renamed project is not dirty")
	}
	f.name.SetValue(p.Name)
	if f.dirty() {
		t.Fatal("a name typed back to what it was is still dirty")
	}
}

// #1051: a touched project form is not lost to q or a screen switch: the
// form captures q while open, and survives a switch.
func TestATouchedProjectFormSurvivesQAndASwitch(t *testing.T) {
	m := connectedRoot(t)
	m.Update(selectViewMsg{id: viewProjects})
	pv := m.views[viewProjects].(*projectsView)
	pv.form = newProjectForm(nil, nil)
	pv.form.name.SetValue("typed")
	if _, cmd := m.Update(key("q")); isQuit(cmd) {
		t.Fatal("q quit over an open project form")
	}
	m.Update(selectViewMsg{id: viewIssues})
	m.Update(selectViewMsg{id: viewProjects})
	if pv.form == nil || pv.form.name.Value() != "typed" {
		t.Fatal("the project form did not survive a screen switch")
	}
}
