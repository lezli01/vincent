package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// workspaceWithPopup is a connected root on the task workspace with one of
// its form popups open.
func workspaceWithPopup(t *testing.T, open func(tv *taskView)) (*root, *taskView) {
	t.Helper()
	m := connectedRoot(t)
	m.switchNow(viewTask)
	tv := m.views[viewTask].(*taskView)
	tv.detail.taskID = 7
	open(tv)
	tv.popup = true
	return m, tv
}

// #1133: a typed repair, follow-up or pull request draft asks before a
// screen switch drops it, through switchTo, as the new-task form does
// (#1051); n keeps it, y switches.
func TestScreenSwitchAsksBeforeDroppingAWorkspaceDraft(t *testing.T) {
	cases := []struct {
		name, draft string
		open        func(tv *taskView)
	}{
		{"repair", "repair", func(tv *taskView) {
			f := newRepairForm(7, "check_failed", "build")
			f.prompt, f.dirty = "fix the test", true
			tv.detail.repair = f
		}},
		{"follow-up", "follow-up", func(tv *taskView) {
			f := newFollowUpForm(7, 1, "done")
			f.setBody("rerun it")
			f.dirty = true
			tv.detail.followUp = f
		}},
		{"pull request", "pull request draft", func(tv *taskView) {
			f := createPRFixture(t)
			f.setRow(cprBody, "edited")
			f.dirty = true
			tv.createPR = f
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := workspaceWithPopup(t, c.open)
			m.Update(selectViewMsg{id: viewIssues})
			if m.active != viewTask || m.leave == nil || m.leave.to != viewIssues {
				t.Fatalf("the switch went ahead: active %v, leave %+v", m.active, m.leave)
			}
			if want := "discard the " + c.draft + " and leave? y/n"; !strings.Contains(ansi.Strip(m.View().Content), want) {
				t.Errorf("the question %q is not on screen", want)
			}
			m.Update(key("n"))
			if m.leave != nil || m.active != viewTask {
				t.Fatal("n did not keep the workspace")
			}
			m.Update(selectViewMsg{id: viewIssues})
			m.Update(key("y"))
			if m.active != viewIssues {
				t.Fatalf("y did not switch: active %v", m.active)
			}
		})
	}
}

// #1133: ctrl+p is reachable from inside the popup, and the palette's
// screen entries switch through switchTo too.
func TestPaletteFromAWorkspacePopupAsks(t *testing.T) {
	m, _ := workspaceWithPopup(t, func(tv *taskView) {
		f := newRepairForm(7, "check_failed", "build")
		f.prompt, f.dirty = "fix the test", true
		tv.detail.repair = f
	})
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.palette == nil {
		t.Fatal("ctrl+p did not open the palette over the popup")
	}
	m.palette = nil
	m.Update(selectViewMsg{id: viewHome})
	if m.leave == nil || m.active != viewTask {
		t.Fatalf("the palette's switch went ahead: active %v", m.active)
	}
}

// An untouched popup, and one whose request is in flight, have nothing to
// ask about.
func TestWorkspaceLeaveDoesNotAskWithoutADraft(t *testing.T) {
	m, tv := workspaceWithPopup(t, func(tv *taskView) {
		tv.detail.repair = newRepairForm(7, "check_failed", "build")
	})
	m.Update(selectViewMsg{id: viewIssues})
	if m.leave != nil || m.active != viewIssues {
		t.Fatal("an untouched repair form held the switch")
	}
	m.switchNow(viewTask)
	tv.detail.repair.prompt, tv.detail.repair.dirty, tv.detail.repair.submitting = "x", true, true
	tv.popup = true
	m.Update(selectViewMsg{id: viewIssues})
	if m.leave != nil || m.active != viewIssues {
		t.Fatal("a submitting repair form held the switch")
	}
}

// #1133: ctrl+s means one thing in the pull request form — push and create —
// from the body editor too, which keeps the text first, as the new-task
// (#797), project and issue forms do.
func TestCreatePRFormCtrlSInTheEditorKeepsAndSends(t *testing.T) {
	withFakeOpener(t, nil)
	f := createPRFixture(t)
	var sentBody string
	submitted := 0
	f.submit = func(_, body string, _ bool) tea.Cmd {
		submitted++
		sentBody = body
		return nil
	}
	f.update(key("j"))
	f.update(key("enter"))
	if !f.editing {
		t.Fatal("enter did not open the body editor")
	}
	f.editor.SetValue("typed in the editor")
	f.update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if f.editing {
		t.Error("ctrl+s left the editor open")
	}
	if submitted != 1 || sentBody != "typed in the editor" {
		t.Fatalf("ctrl+s in the editor sent %d time(s) with body %q, want once with the typed text", submitted, sentBody)
	}
}
