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
