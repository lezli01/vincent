package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// #1062: while a question is pending, enter answers it on every tab, so no
// tab advertises its own enter and the Overview's line says so.
func TestPendingQuestionTakesEnter(t *testing.T) {
	task := apiclient.TaskDetail{Task: apiclient.Task{
		ID: 4, Title: "asking", State: stateAwaitingInput, StepTotal: 1,
		AvailableActions: []string{apiclient.ActionAnswer, apiclient.ActionCancel},
	}}
	v := cardView(t, task)
	before := v.liveBindings(bindingsFor(ctxOutput))
	if !hasKey(before, "enter") {
		t.Fatal("the fixture's Output tab has no enter row to hide")
	}
	v.detail.form = newAnswerForm(questionRequest())
	for _, ctx := range []bindingContext{ctxOutput, ctxTaskOverview, ctxTimeline} {
		if hasKey(v.liveBindings(bindingsFor(ctx)), "enter") {
			t.Errorf("%s still advertises its own enter while a question is pending", ctx)
		}
	}
	if got := ansi.Strip(v.render(100, 40)); !strings.Contains(got, "enter answer the question") {
		t.Errorf("the Overview does not say enter answers:\n%s", got)
	}
}

func hasKey(rows []binding, key string) bool {
	for _, b := range rows {
		if b.key == key {
			return true
		}
	}
	return false
}

// #1141: the ? sheet agrees with the footer. While a question is pending,
// the workspace's sheet gives enter one meaning — the answer, among the
// actions now — and no tab's own enter row stands beside it.
func TestHelpGivesEnterOneMeaningWhileAQuestionIsPending(t *testing.T) {
	m, v := runningWorkspace(t, "")
	v.detail.task.State = stateAwaitingInput
	v.detail.task.AvailableActions = []string{apiclient.ActionAnswer, apiclient.ActionCancel}
	for _, tab := range v.tabs() {
		v.tab = tab
		v.detail.form = nil
		if tab == taskTabOutput && len(enterRows(m.helpSheet())) == 0 {
			t.Fatal("the fixture's Output sheet has no enter row to replace")
		}
		v.detail.form = newAnswerForm(questionRequest())
		sheet := m.helpSheet()
		if rows := enterRows(sheet); len(rows) != 1 || !strings.HasSuffix(rows[0], " "+answerBinding.label) {
			t.Errorf("%s: the sheet's enter rows outside the popups' sections are %q, want only the answer",
				taskTabNames[tab], rows)
		}
	}
}

// enterRows counts the sheet's enter rows before the popups' sections,
// which describe the popups rather than the surface under them.
func enterRows(sheet string) []string {
	plain, _, _ := strings.Cut(ansi.Strip(sheet), "ANSWER FORM")
	var rows []string
	for line := range strings.SplitSeq(plain, "\n") {
		if strings.HasPrefix(line, "  enter ") {
			rows = append(rows, line)
		}
	}
	return rows
}
