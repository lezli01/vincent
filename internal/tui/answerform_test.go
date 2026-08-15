package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
	if !strings.Contains(f.render(74, 20), "teal") {
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
	if f.err == "" || !strings.Contains(f.render(74, 20), "⚠") {
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
	view := f.render(74, 10)
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

// openPopupWith puts req on a task that is waiting on input and opens the
// answer popup the way a human does, returning the rendered screen with the
// styling stripped. The popup is sized by the shell (§15), so this is the
// only place the form meets the width it is actually drawn at.
func openPopupWith(t *testing.T, req apiclient.InputRequest) string {
	t.Helper()
	s, _ := newShellFixture(t, task(3, stateAwaitingInput))
	s.settle()
	s.detail.form = newAnswerForm(req)
	s.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !s.popup {
		t.Fatal("enter did not open the answer popup")
	}
	return ansi.Strip(s.render(120, 37))
}

// TestFormPopupShowsLongTextInFull is issue #83: the popup is capped at 76
// columns and every content line is truncated to fit it, so a question, an
// option label or a permission summary longer than the inner width loses its
// tail — and there is no wrap, scroll or expand to get it back. §7.4 and §15
// describe the form as the surface a human answers from; text that cannot be
// read cannot be answered.
func TestFormPopupShowsLongTextInFull(t *testing.T) {
	t.Run("question and option", func(t *testing.T) {
		const question = "Should the scheduler admit the re-queued step ahead of newly created tasks, or hold it behind them for fairness?"
		const option = "No — re-queue at the tail so one project's quota stop cannot starve the others (Recommended)"

		view := openPopupWith(t, apiclient.InputRequest{
			Kind: apiclient.InputKindQuestion,
			Questions: []apiclient.InputQuestion{{
				Header:  "Approach",
				Text:    question,
				Options: []string{option, "Yes — keep its original queue position"},
			}},
		})

		// The tail of the question: the last words are what a 76-column popup
		// drops first, and they carry the actual choice being asked about.
		if !strings.Contains(view, "hold it behind them for fairness?") {
			t.Errorf("the end of the question is not on screen; the popup renders:\n%s", popupOf(view))
		}
		// An agent puts "(Recommended)" at the end of a label, which is exactly
		// what a truncating renderer removes.
		if !strings.Contains(view, "(Recommended)") {
			t.Errorf("the end of the option label is not on screen; the popup renders:\n%s", popupOf(view))
		}
	})

	t.Run("permission summary", func(t *testing.T) {
		const summary = "rm -rf ./build && ./scripts/publish.sh --channel=stable --yes"

		view := openPopupWith(t, apiclient.InputRequest{
			Kind:       apiclient.InputKindPermission,
			Permission: &apiclient.InputPermission{Tool: "Bash", Summary: summary},
		})

		// Approving a command whose tail is hidden is approving something else.
		if !strings.Contains(view, "--channel=stable --yes") {
			t.Errorf("the end of the permission summary is not on screen; the popup renders:\n%s", popupOf(view))
		}
	})
}

// popupOf pulls the answer popup out of a rendered screen so a failure shows
// the box the assertion is about rather than the whole terminal.
func popupOf(view string) string {
	var out []string
	in := false
	for _, line := range strings.Split(view, "\n") {
		switch {
		case strings.Contains(line, "Answer — #"):
			in = true
			out = append(out, line)
		case in:
			out = append(out, line)
			if strings.Contains(line, "└") {
				return strings.Join(out, "\n")
			}
		}
	}
	return strings.Join(out, "\n")
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
