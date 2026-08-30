package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Task 059: the three §6/§7.4 form popups carry their own two-tab strip, so
// the context needed to answer a question is reachable without closing the
// question. What every test here is really guarding is the draft: leaving to
// look something up used to cost the repair and follow-up prompts outright.

// ctrlT is the tab-switch chord, taken by taskView before the form sees it.
func ctrlT() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl} }

// popupTaskView raises one of the three popups on a task workspace sized to a
// real terminal, and returns it ready for keys.
func popupTaskView(t *testing.T, open func(*detail)) *taskView {
	t.Helper()
	v := newTaskView(taskDetailFixture(t))
	open(v.detail)
	v.openPopup()
	v.width, v.height = 100, 30
	return v
}

// typeInto sends one key per rune, the way a terminal delivers typing.
func typeInto(v *taskView, text string) {
	for _, r := range text {
		v.updateKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// TestAnswerPopupTabKeepsThePicksAndTheTypedAnswer is the feature's point at
// its most expensive surface: a §7.4 question whose options are picked and
// whose free text is committed must survive the round trip through the
// details tab untouched.
func TestAnswerPopupTabKeepsThePicksAndTheTypedAnswer(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.form = newAnswerForm(questionRequest()) })
	f := v.detail.form

	v.updateKey(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}) // pick Blue
	v.updateKey(tea.KeyPressMsg{Code: 'e', Text: "e"})          // free text
	typeInto(v, "teal")
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // commit it
	before := f.typedAnswers(f.req.Questions[0])
	if before == "" || len(f.answers) == 0 {
		t.Fatalf("the fixture never built a draft: answers=%v typed=%q", f.answers, before)
	}

	v.updateKey(ctrlT())
	if v.popupTab != popupTabDetails {
		t.Fatal("ctrl+t did not reach the Task details tab")
	}
	strip := ansi.Strip(v.render(v.width, v.height))
	if !strings.Contains(strip, "Task details") || !strings.Contains(strip, "Question") {
		t.Fatalf("the popup does not name both tabs:\n%s", strip)
	}
	// Read around on the details tab, then come back.
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyDown})
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	v.updateKey(ctrlT())

	if v.popupTab != popupTabForm {
		t.Fatal("ctrl+t did not come back to the Question tab")
	}
	if !v.popup || v.detail.form != f {
		t.Fatal("the round trip closed the popup or replaced the form")
	}
	if got := f.typedAnswers(f.req.Questions[0]); got != before {
		t.Fatalf("the typed answer became %q, want %q", got, before)
	}
	if len(f.answers) == 0 {
		t.Fatal("the picked options were lost across the tab switch")
	}
	if f.err != "" {
		t.Fatalf("the tab switch reached the form as an error: %q", f.err)
	}
}

// TestPopupTabReachesTheStripThroughAFocusedEditor: ctrl+t is intercepted
// before the form sees it (decision 4), which is the only reason it works
// while a textarea has the keyboard. It must also insert nothing.
func TestPopupTabReachesTheStripThroughAFocusedEditor(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.repair = repairFormFixture() })
	f := v.detail.repair

	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // open the prompt field
	if !f.editing {
		t.Fatal("enter did not open the prompt field")
	}
	typeInto(v, "half a thought")
	v.updateKey(ctrlT())
	if v.popupTab != popupTabDetails {
		t.Fatal("ctrl+t did not switch tab from inside the prompt editor")
	}
	if got := f.editor.Value(); got != "half a thought" {
		t.Fatalf("the editor holds %q — ctrl+t typed into it", got)
	}
	// Keys pressed on the details tab must not reach the editor either.
	v.updateKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	v.updateKey(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if got := f.editor.Value(); got != "half a thought" {
		t.Fatalf("the editor holds %q — the details tab leaked keys into it", got)
	}

	v.updateKey(ctrlT())
	if !f.editing {
		t.Fatal("the prompt field lost focus across the round trip")
	}
	if got := f.editor.Value(); got != "half a thought" {
		t.Fatalf("the draft came back as %q", got)
	}
}

// TestPopupTabReachesTheStripThroughAnOpenPicker: the same seam, through the
// other sub-mode these popups have.
func TestPopupTabReachesTheStripThroughAnOpenPicker(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.followUp = newFollowUpForm(7, 3, "done") })
	f := v.detail.followUp

	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // the run-form chooser
	if f.picker == nil {
		t.Fatal("enter did not open the run-form list")
	}
	v.updateKey(ctrlT())
	if v.popupTab != popupTabDetails {
		t.Fatal("ctrl+t did not switch tab with a picker open")
	}
	v.updateKey(ctrlT())
	if f.picker == nil {
		t.Fatal("the open picker did not survive the round trip")
	}
}

// TestFollowUpPopupTabKeepsTheRunFormAndItsBody: the follow-up's draft is the
// chooser's answer plus the text under it, and both must come back.
func TestFollowUpPopupTabKeepsTheRunFormAndItsBody(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.followUp = newFollowUpForm(7, 3, "done") })
	f := v.detail.followUp
	f.form = "command"
	f.cursor = fuBody

	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	typeInto(v, "go test ./...")
	v.updateKey(ctrlT())
	v.updateKey(ctrlT())

	if f.form != "command" {
		t.Fatalf("the run form became %q, want command", f.form)
	}
	if got := f.editor.Value(); got != "go test ./..." {
		t.Fatalf("the command draft came back as %q", got)
	}
}

// TestPopupDetailsTabIsReadOnly is decision 6: inside the popup the details
// tab forwards nothing. No task action is posted, and neither of the
// pull-request keys is offered — a popup that can raise a second popup is not
// a reference surface.
func TestPopupDetailsTabIsReadOnly(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.form = newAnswerForm(questionRequest()) })
	// Make `P` offerable on the workspace tab, so the test proves the popup
	// withholds it rather than that the task never had it.
	v.pull.CompareURL = "https://github.com/o/r/compare/main...vincent/7-task-workspace"
	v.pullLoaded = true
	v.updateKey(ctrlT())

	for _, key := range []string{"o", "P", "p", "c", "A", "r", "s", "R", "F", "E"} {
		if cmd := v.updateKey(tea.KeyPressMsg{Code: rune(key[0]), Text: key}); cmd != nil {
			t.Errorf("%q posted a command from the popup's details tab", key)
		}
	}
	if v.createPR != nil {
		t.Fatal("P opened the create-PR form from inside a popup")
	}
	if v.detail.repair != nil || v.detail.followUp != nil {
		t.Fatal("a popup key raised a second popup")
	}
	if v.pullNote != "" {
		t.Fatalf("a pull-request key spoke from the popup: %q", v.pullNote)
	}
	if v.popupTab != popupTabDetails || !v.popup {
		t.Fatal("a read-only key changed tab or closed the popup")
	}
}

// TestPopupDetailsTabScrollsAndSelectsSections: the pane inside the popup is
// the pane on the workspace tab, so it navigates the same way — and a
// document taller than the popup still reaches its last section.
func TestPopupDetailsTabScrollsAndSelectsSections(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.repair = repairFormFixture() })
	v.updateKey(ctrlT())
	v.render(v.width, v.height)

	first := v.popupDetails.section
	if first == "" {
		t.Fatal("the popup's details tab selected no section")
	}
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if v.popupDetails.section == first {
		t.Fatalf("↓ did not change section (still %q)", first)
	}

	// A section long enough to scroll: pgdown must move the content window.
	v.detail.task.Description = strings.TrimSpace(strings.Repeat("a long description line\n", 200))
	v.popupDetails.selectSection(0)
	v.render(v.width, v.height)
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if v.popupDetails.top == 0 {
		t.Fatal("pgdown did not scroll the popup's details body")
	}

	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	got := ansi.Strip(v.render(v.width, v.height))
	last := v.popupDetails.sections[len(v.popupDetails.sections)-1]
	if v.popupDetails.section != last {
		t.Fatalf("end selected %q, want the last section %q", v.popupDetails.section, last)
	}
	if !strings.Contains(got, last) {
		t.Fatalf("the last section is not on screen:\n%s", got)
	}
	// The workspace tab behind the popup never moved: the popup owns its own
	// reading position (task 059).
	if v.details.section != "" || v.details.top != 0 {
		t.Fatalf("the popup moved the workspace's details tab to %q@%d",
			v.details.section, v.details.top)
	}
}

// TestPopupEscOnDetailsReturnsToTheForm is decision 5: esc closes one layer,
// and inside a popup the innermost layer is the tab — not the popup, and
// never the draft.
func TestPopupEscOnDetailsReturnsToTheForm(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.followUp = newFollowUpForm(7, 3, "done") })
	f := v.detail.followUp
	f.cursor = fuBody
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	typeInto(v, "retry the flaky test")
	v.updateKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}) // commit the text

	v.updateKey(ctrlT())
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if v.popupTab != popupTabForm {
		t.Fatal("esc on the details tab did not return to the form tab")
	}
	if !v.popup || v.detail.followUp == nil {
		t.Fatal("esc on the details tab closed the popup")
	}
	if f.prompt != "retry the flaky test" {
		t.Fatalf("esc discarded the draft: prompt = %q", f.prompt)
	}

	// esc on the form tab keeps its own meaning: the follow-up closes.
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if v.popup || v.detail.followUp != nil {
		t.Fatal("esc on the form tab did not close the follow-up popup")
	}
}

// TestAnswerPopupEscOnTheFormTabStillKeepsThePicks: the answer form's esc is
// the one that keeps what was picked, and the inner layer must not have
// changed that.
func TestAnswerPopupEscOnTheFormTabStillKeepsThePicks(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.form = newAnswerForm(questionRequest()) })
	f := v.detail.form
	v.updateKey(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})

	v.updateKey(ctrlT())
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape}) // back to the form
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape}) // close the popup
	if v.popup {
		t.Fatal("esc on the form tab did not close the answer popup")
	}
	if v.detail.form != f || len(f.answers) == 0 {
		t.Fatal("closing the answer popup discarded what was picked")
	}
}

// TestPopupFrameIsTheSameHeightOnBothTabs is decision 3: the popup takes the
// whole height budget whenever it has tabs, so nothing resizes under the
// reader on a ctrl+t.
func TestPopupFrameIsTheSameHeightOnBothTabs(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.form = newAnswerForm(questionRequest()) })

	before := popupFrameRows(t, ansi.Strip(v.render(v.width, v.height)))
	v.updateKey(ctrlT())
	after := popupFrameRows(t, ansi.Strip(v.render(v.width, v.height)))
	if before != after {
		t.Fatalf("the popup frame is %d rows on the form tab and %d on the details tab",
			before, after)
	}
	if before < v.height-6 {
		t.Fatalf("the popup is %d rows tall in a %d-row body — decision 3 asks for the "+
			"whole budget", before, v.height)
	}
}

// popupFrameRows counts the rows between the popup's top and bottom border.
func popupFrameRows(t *testing.T, screen string) int {
	t.Helper()
	top, bottom := -1, -1
	for i, line := range strings.Split(screen, "\n") {
		switch {
		case top < 0 && strings.Contains(line, "┌─"):
			top = i
		case top >= 0 && strings.Contains(line, "└─"):
			bottom = i
		}
	}
	if top < 0 || bottom < 0 {
		t.Fatalf("no popup frame on screen:\n%s", screen)
	}
	return bottom - top + 1
}

// TestPopupOwnsTheBindingContext closes the gap task 059 found on the way: a
// popup that owns the keyboard must own the footer and the ? sheet too, or
// the rows tasks 025 and 027 registered are unreachable from either.
func TestPopupOwnsTheBindingContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(*detail)
		want bindingContext
	}{
		{"answer", func(d *detail) { d.form = newAnswerForm(questionRequest()) }, ctxForm},
		{"repair", func(d *detail) { d.repair = repairFormFixture() }, ctxRepairForm},
		{"follow-up", func(d *detail) { d.followUp = newFollowUpForm(7, 3, "done") }, ctxFollowUpForm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := popupTaskView(t, tc.open)
			if got := v.bindingContext(); got != tc.want {
				t.Fatalf("binding context = %q, want %q", got, tc.want)
			}
			sheet := ansi.Strip(helpText(v.bindingContext(), true))
			if !strings.Contains(sheet, "ctrl+t") {
				t.Fatalf("? does not print ctrl+t for %s:\n%s", tc.want, sheet)
			}
		})
	}
}

// TestHelpPrintsAllThreePopupSections: the sheet printed the answer and
// repair popups and silently omitted the follow-up one (task 059).
func TestHelpPrintsAllThreePopupSections(t *testing.T) {
	sheet := strings.ToLower(ansi.Strip(helpText(ctxTimeline, true)))
	for _, want := range []string{"answer form", "repair form", "follow-up form"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("the ? sheet has no %q section:\n%s", want, sheet)
		}
	}
}

// TestCreatePRPopupHasNoTabs: decision 1 leaves the compare-URL editor out of
// this change, and it must keep sizing itself to its own content.
func TestCreatePRPopupHasNoTabs(t *testing.T) {
	v := newTaskView(taskDetailFixture(t))
	v.width, v.height = 100, 30
	v.pull.CompareURL = "https://github.com/o/r/compare/main...vincent/7-task-workspace"
	v.tab = taskTabDetails
	if cmd := v.updateKey(tea.KeyPressMsg{Code: 'P', Text: "P"}); cmd != nil {
		t.Fatalf("P posted a command: %v", cmd)
	}
	if v.createPR == nil {
		t.Fatalf("P did not open the create-PR form: %q", v.pullNote)
	}
	screen := ansi.Strip(v.render(v.width, v.height))
	if strings.Contains(screen, "Task details │") || strings.Contains(screen, "ctrl+t") {
		t.Fatalf("the compare-URL editor grew a tab strip:\n%s", screen)
	}
	if rows := popupFrameRows(t, screen); rows >= v.height-4 {
		t.Fatalf("the compare-URL editor is %d rows tall — it should size to its content", rows)
	}
	v.updateKey(ctrlT())
	if v.popupTab != popupTabForm {
		t.Fatal("ctrl+t switched a tab the compare-URL editor does not have")
	}
}
