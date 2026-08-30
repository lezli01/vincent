package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestRepairPopupTaskDetailsTabLive drives task 059 against the real API
// handlers: a task the daemon actually blocked, its repair popup, and the
// details tab inside it. What the unit tests cannot prove is that the popup's
// inspector reads the same document the workspace's Task Details tab reads —
// the sections come from a live task detail, not a fixture — and that a draft
// typed into a live form survives the round trip.
func TestRepairPopupTaskDetailsTabLive(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newActionLiveHarness(t)
	task := h.createTask(t, "inspectable")
	h.blockTask(t, task.ID, "check_failed")

	_, cmd := h.m.Update(selectTaskMsg{id: task.ID})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the detail view to open the task", func() bool {
		return detailOf(h.m).taskID == task.ID && detailOf(h.m).loaded
	})
	h.p.until(30*time.Second, "the daemon to offer repair", func() bool {
		return detailOf(h.m).target().has("repair")
	})

	h.press(t, "R")
	v := h.m.views[viewTask].(*taskView)
	form := detailOf(h.m).repair
	if form == nil || !v.popup {
		t.Fatal("R did not raise the repair popup")
	}

	// A half-written prompt: the draft this feature exists to protect.
	const draft = "the check needs the fixture committed"
	h.press(t, "enter")
	h.typeText(t, draft)

	h.pressCtrlT(t)
	if v.popupTab != popupTabDetails {
		t.Fatal("ctrl+t did not reach the popup's Task details tab")
	}
	screen := ansi.Strip(content(h.m))
	if !strings.Contains(screen, "Task details") || !strings.Contains(screen, "Repair") {
		t.Fatalf("the popup does not name both tabs:\n%s", screen)
	}
	popupSections := append([]string(nil), v.popupDetails.sections...)
	if len(popupSections) == 0 {
		t.Fatalf("the popup's details tab found no sections:\n%s", screen)
	}

	// The same document the workspace tab reads. Rendering the workspace pane
	// directly is what keeps the popup open — closing it to look would throw
	// the draft away, which is the whole complaint.
	v.renderDetails(v.width, max(v.height-3, 1))
	if got, want := strings.Join(popupSections, ","), strings.Join(v.details.sections, ","); got != want {
		t.Fatalf("the popup shows sections %q, the workspace tab %q", got, want)
	}
	for _, want := range []string{"Description", "Overview", "Lifecycle"} {
		if !strings.Contains(strings.Join(popupSections, ","), want) {
			t.Errorf("the popup's sidebar has no %q section: %v", want, popupSections)
		}
	}
	if !strings.Contains(screen, "inspectable") {
		t.Errorf("the popup's details body does not name the task:\n%s", screen)
	}

	// Walk to another section, then back to the form: the draft is intact and
	// the popup never closed.
	h.press(t, "j")
	if v.popupDetails.section == popupSections[0] {
		t.Fatalf("j did not move off %q", popupSections[0])
	}
	h.pressCtrlT(t)
	if v.popupTab != popupTabForm || !v.popup || detailOf(h.m).repair != form {
		t.Fatal("the round trip did not come back to the same open repair form")
	}
	if got := form.editor.Value(); got != draft {
		t.Fatalf("the repair draft came back as %q, want %q", got, draft)
	}
}

// pressCtrlT sends the popup's tab-switch chord.
func (h *actionLiveHarness) pressCtrlT(t *testing.T) {
	t.Helper()
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	h.p.push(cmd)
}
