package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

func questionRequest() apiclient.InputRequest {
	return apiclient.InputRequest{
		Kind: apiclient.InputKindQuestion,
		Questions: []apiclient.InputQuestion{
			{Text: "Which color?", Options: []string{"Blue", "Red"}},
			{Text: "Which files?", Options: []string{"a.go", "b.go"}, MultiSelect: true},
		},
	}
}

// press drives one key into the form.
func (f *answerForm) press(key string) (tea.Cmd, bool) {
	return f.update(keyPress(key), nil, 1)
}

// TestFormNavigationSkipsQuestionText: a question's own line is not a choice,
// so the cursor never lands on it.
func TestFormNavigationSkipsQuestionText(t *testing.T) {
	f := newAnswerForm(questionRequest())
	for i := range len(f.rows) {
		if row, _ := f.currentRow(); row.kind == rowHeader {
			t.Fatalf("cursor landed on a header at step %d", i)
		}
		f.press("j")
	}
}

// TestSingleSelectReplacesAndMultiSelectToggles is §7.4's rule at the
// keyboard: one answer where one is allowed, several where they are.
func TestSingleSelectReplacesAndMultiSelectToggles(t *testing.T) {
	f := newAnswerForm(questionRequest())

	f.press(" ") // Blue
	f.press("j")
	f.press(" ") // Red replaces Blue
	if got := f.answers["Which color?"]; len(got) != 1 || got[0] != "Red" {
		t.Errorf("single-select answers = %v, want just Red", got)
	}

	f.press("j") // free-text row of question 1
	f.press("j") // a.go
	f.press(" ")
	f.press("j") // b.go
	f.press(" ")
	if got := f.answers["Which files?"]; len(got) != 2 {
		t.Fatalf("multi-select answers = %v, want both", got)
	}
	f.press(" ") // toggle b.go back off
	if got := f.answers["Which files?"]; len(got) != 1 || got[0] != "a.go" {
		t.Errorf("after toggling off: %v, want just a.go", got)
	}
}

// TestFreeTextIsAlwaysAccepted: options are suggestions, never an enum.
func TestFreeTextIsAlwaysAccepted(t *testing.T) {
	f := newAnswerForm(questionRequest())
	f.press("e")
	if !f.capturing() {
		t.Fatal("e did not open the text field")
	}
	for _, r := range "teal" {
		f.press(string(r))
	}
	f.press("enter")
	if f.capturing() {
		t.Error("enter did not close the text field")
	}
	if got := f.answers["Which color?"]; len(got) != 1 || got[0] != "teal" {
		t.Errorf("answers = %v, want the typed value", got)
	}
	if !strings.Contains(f.render(20), "teal") {
		t.Error("the typed answer is not shown back")
	}
}

// TestSubmitBlockedUntilEveryQuestionIsAnswered: the form says what is
// missing rather than spending a round trip to be told.
func TestSubmitBlockedUntilEveryQuestionIsAnswered(t *testing.T) {
	f := newAnswerForm(questionRequest())
	f.press(" ") // only the first question

	cmd, _ := f.press("enter")
	if cmd != nil {
		t.Fatal("an incomplete answer was submitted")
	}
	if f.err == "" || !strings.Contains(f.render(20), "⚠") {
		t.Errorf("no reason shown for the blocked submit (err %q)", f.err)
	}
	if f.submitting {
		t.Error("the form claims to be submitting")
	}
}

// TestPermissionRendersADecision: a permission request has no questions, and
// answers would be a validation error (§7.4).
func TestPermissionRendersADecision(t *testing.T) {
	f := newAnswerForm(apiclient.InputRequest{
		Kind:       apiclient.InputKindPermission,
		Permission: &apiclient.InputPermission{Tool: "Bash", Summary: "rm -rf build"},
	})
	view := f.render(10)
	if !strings.Contains(view, "Bash") || !strings.Contains(view, "allow") || !strings.Contains(view, "deny") {
		t.Fatalf("permission form = %q, want the tool and both decisions", view)
	}
	f.press(" ")
	if f.allow == nil || !*f.allow {
		t.Errorf("allow = %v, want the first choice selected", f.allow)
	}
	f.press("j")
	f.press(" ")
	if f.allow == nil || *f.allow {
		t.Errorf("allow = %v, want deny selected", f.allow)
	}
	if len(f.response().Answers) != 0 {
		t.Error("a permission decision produced answers")
	}
}

// TestEscLeavesTheFormWithoutAnswering keeps the exit honest: the task is
// still waiting, and nothing was sent.
func TestEscLeavesTheFormWithoutAnswering(t *testing.T) {
	f := newAnswerForm(questionRequest())
	cmd, exit := f.press("esc")
	if !exit || cmd != nil {
		t.Errorf("esc: exit = %v, cmd = %v; want a plain exit", exit, cmd != nil)
	}
}

// TestSameRequestSurvivesARefresh: the detail view refetches constantly, and
// rebuilding the form under a human would discard what they picked.
func TestSameRequestSurvivesARefresh(t *testing.T) {
	f := newAnswerForm(questionRequest())
	if !f.sameRequest(questionRequest()) {
		t.Error("an identical request was treated as a new one")
	}
	other := questionRequest()
	other.Questions[0].Text = "Which shade?"
	if f.sameRequest(other) {
		t.Error("a different question was treated as the same one")
	}
}
