package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// thirteenLines is a prompt one line longer than twice the old six-line cap,
// the count issue #800's acceptance names.
const thirteenLines = 13

// typeLines types n lines of text into an open popup editor the way a
// terminal does — one key per rune, enter between lines.
func typeLines(v *taskView, n int) {
	for i := range n {
		if i > 0 {
			v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		typeInto(v, "line")
	}
}

// TestFollowUpEditorTakesAThirteenthLine is issue #800 W3: enter is a
// newline in the follow-up prompt, and the textarea's MaxHeight capped
// insertion at editorLines, so the seventh enter was silently swallowed.
func TestFollowUpEditorTakesAThirteenthLine(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.followUp = newFollowUpForm(7, 3, "done") })
	f := v.detail.followUp
	v.updateKey(keyPress("j")) // the body row
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !f.editing {
		t.Fatal("enter on the body row did not open the prompt field")
	}
	typeLines(v, thirteenLines)
	if got := f.editor.LineCount(); got != thirteenLines {
		t.Fatalf("follow-up prompt holds %d lines after typing %d", got, thirteenLines)
	}
}

// TestRepairEditorTakesAThirteenthLine is W3 on the repair popup.
func TestRepairEditorTakesAThirteenthLine(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.repair = newRepairForm(7, "check_failed", "verify") })
	f := v.detail.repair
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // the prompt row
	if !f.editing {
		t.Fatal("enter on the prompt row did not open the prompt field")
	}
	typeLines(v, thirteenLines)
	if got := f.editor.LineCount(); got != thirteenLines {
		t.Fatalf("repair prompt holds %d lines after typing %d", got, thirteenLines)
	}
}

// TestFollowUpConsequenceIsReadableAt80Columns is W7: the subject line says
// the task returns to its state afterwards — the clause the code comment
// says people get wrong — and at 80 columns it was cut with an ellipsis.
func TestFollowUpConsequenceIsReadableAt80Columns(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.followUp = newFollowUpForm(42, 3, "done") })
	screen := ansi.Strip(v.render(80, 24))
	flat := strings.Join(strings.Fields(strings.ReplaceAll(screen, "│", " ")), " ")
	if !strings.Contains(flat, "the task returns to done afterwards") {
		t.Fatalf("the follow-up consequence is not readable at 80×24:\n%s", screen)
	}
}

// TestFooterListsNoTaskActionsUnderTheAnswerPopup is W5: while a popup owns
// the keyboard the form ignores the task's action keys, so the footer must
// not advertise them.
func TestFooterListsNoTaskActionsUnderTheAnswerPopup(t *testing.T) {
	m := connectedRoot(t)
	v := m.views[viewTask].(*taskView)
	d := v.detail
	d.taskID = 4
	waiting := apiclient.TaskDetail{Task: apiclient.Task{
		ID: 4, Title: "waiting", State: stateAwaitingInput, StepTotal: 1,
		AvailableActions: []string{
			apiclient.ActionAnswer, apiclient.ActionPause, apiclient.ActionRetry, apiclient.ActionCancel,
		},
	}}
	waiting.PendingInput = []byte(`{"kind":"question","questions":[{"text":"Which colour?","options":["teal","mauve"]}]}`)
	d.applyLoaded(detailLoadedMsg{id: 4, task: waiting})
	m.active = viewTask
	if d.form == nil {
		t.Fatal("fixture: the pending request built no answer form")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !v.popup {
		t.Fatal("enter did not open the answer popup")
	}
	footer := ansi.Strip(m.footerLine())
	for _, action := range []string{"pause", "retry", "cancel"} {
		if strings.Contains(footer, action) {
			t.Errorf("footer advertises %q while the answer popup has the keyboard: %q", action, footer)
		}
	}
}
