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
	f.press("t")
	if !f.capturing() {
		t.Fatal("t did not open the text field")
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
// answer popup the way a human does — enter on the task workspace — returning
// the view it is open on. The popup is sized by overlayPopup (§15), so this is
// the only place the form meets the width it is actually drawn at.
func openPopupWith(t *testing.T, req apiclient.InputRequest) *taskView {
	t.Helper()
	d := taskDetailFixture(t)
	d.task.State = stateAwaitingInput
	d.form = newAnswerForm(req)
	v := newTaskView(d)
	v.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !v.popup {
		t.Fatal("enter did not open the answer popup")
	}
	return v
}

// popupView is the screen the popup is drawn on, with the styling stripped.
func popupView(v *taskView) string { return ansi.Strip(v.render(120, 37)) }

// TestTaskAnswerPopup: the form never opens itself; enter opens it, its keys
// stay inside it rather than reaching the workspace's tab strip, and a
// cleared request takes the popup with it. That esc keeps the picks is
// TestAnswerPopupEscOnTheFormTabStillKeepsThePicks.
func TestTaskAnswerPopup(t *testing.T) {
	v := newTaskView(taskDetailFixture(t))
	awaiting := v.detail.task
	awaiting.State = stateAwaitingInput
	awaiting.PendingInput = []byte(`{"kind":"question","questions":[` +
		`{"text":"Which colour?","options":["teal","mauve"]}]}`)

	// The request arriving builds the form and nothing else (§15).
	v.update(detailLoadedMsg{id: 7, task: awaiting})
	if v.detail.form == nil {
		t.Fatal("fixture: the pending request built no answer form")
	}
	if v.popup {
		t.Fatal("the popup opened itself")
	}

	v.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !v.popup {
		t.Fatal("enter did not open the answer popup")
	}
	if !strings.Contains(ansi.Strip(v.render(120, 37)), "Which colour?") {
		t.Fatal("open popup does not render the question")
	}

	// Keys go to the form, not the workspace: space picks an option, and `]`
	// — next tab everywhere else — moves nothing.
	v.update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if got := v.detail.form.answers["Which colour?"]; len(got) != 1 || got[0] != "teal" {
		t.Fatalf("space picked %v, want the first option", got)
	}
	v.update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if v.tab != taskTabOverview {
		t.Fatalf("tab moved to %v while the popup was open", v.tab)
	}
	if !v.popup {
		t.Fatal("a key meant for the form closed the popup")
	}

	// A cleared request (the refetch after an answer) closes an open popup.
	answered := v.detail.task
	answered.State, answered.PendingInput = stateRunning, nil
	v.update(detailLoadedMsg{id: 7, task: answered})
	if v.detail.form != nil {
		t.Fatal("fixture: the refetch kept the answered request's form")
	}
	if v.popup {
		t.Fatal("the popup outlived its request")
	}
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

		view := popupView(openPopupWith(t, apiclient.InputRequest{
			Kind: apiclient.InputKindQuestion,
			Questions: []apiclient.InputQuestion{{
				Header:  "Approach",
				Text:    question,
				Options: []string{option, "Yes — keep its original queue position"},
			}},
		}))

		// The question and the label in full, wrap or no wrap: the tail is what
		// a truncating renderer drops, and it carries the actual choice —
		// including the "(Recommended)" an agent puts at the end of a label.
		if flat := popupText(view); !strings.Contains(flat, question) || !strings.Contains(flat, option) {
			t.Errorf("the question or the option is not on screen in full; the popup renders:\n%s", popupOf(view))
		}
	})

	t.Run("permission summary", func(t *testing.T) {
		const summary = "rm -rf ./build && ./scripts/publish.sh --channel=stable --yes"

		view := popupView(openPopupWith(t, apiclient.InputRequest{
			Kind:       apiclient.InputKindPermission,
			Permission: &apiclient.InputPermission{Tool: "Bash", Summary: summary},
		}))

		// Approving a command whose tail is hidden is approving something else.
		if !strings.Contains(popupText(view), summary) {
			t.Errorf("the end of the permission summary is not on screen; the popup renders:\n%s", popupOf(view))
		}
	})
}

// TestFormPopupIsWideEnoughToReadAQuestion: the popup used to be capped at 76
// columns on any terminal, which wrapped an ordinary §7.4 question into three
// or four lines and pushed the options it asks about off the bottom. On a
// terminal with room, a question of a length agents actually ask gets one line.
func TestFormPopupIsWideEnoughToReadAQuestion(t *testing.T) {
	const question = "Should the retry keep the original prompt, or use the one you just edited in $EDITOR?"

	v := openPopupWith(t, apiclient.InputRequest{
		Kind:      apiclient.InputKindQuestion,
		Questions: []apiclient.InputQuestion{{Text: question, Options: []string{"Edited", "Original"}}},
	})
	view := popupView(v)
	for _, line := range strings.Split(popupOf(view), "\n") {
		if strings.Contains(line, question) {
			return
		}
	}
	t.Errorf("the question wrapped at a 120-column terminal; the popup renders:\n%s", popupOf(view))
}

// TestFormPopupShowsALongTypedAnswerInFull is the other half of a readable
// popup: the answer a human types is text of a length nobody chose. It has to
// stay readable while it is being typed and after it is committed, because an
// answer whose tail is hidden is an answer nobody can check before submitting.
func TestFormPopupShowsALongTypedAnswerInFull(t *testing.T) {
	const answer = "Use the edited prompt, but keep the original as a comment at the top of the step so the diff " +
		"between what was asked and what was run stays visible in the transcript afterwards."

	v := openPopupWith(t, questionRequest())
	form := v.detail.form
	v.update(keyPress("t"))
	if !form.capturing() {
		t.Fatal("t did not open the text field")
	}
	form.paste(answer)

	if flat := popupText(popupView(v)); !strings.Contains(flat, answer) {
		t.Errorf("the answer being typed is not on screen in full; the popup renders:\n%s",
			popupOf(popupView(v)))
	}

	v.update(tea.KeyPressMsg{Code: tea.KeyEnter}) // commit the free text
	if form.capturing() {
		t.Fatal("enter did not close the text field")
	}
	if flat := popupText(popupView(v)); !strings.Contains(flat, answer) {
		t.Errorf("the committed answer is not shown back in full; the popup renders:\n%s",
			popupOf(popupView(v)))
	}
}

// popupText flattens the popup into one whitespace-normalised line. Every
// content line wraps and is indented, so an assertion about text being on
// screen must not depend on where the wrap fell.
func popupText(view string) string {
	flat := strings.ReplaceAll(popupOf(view), "│", " ")
	return strings.Join(strings.Fields(flat), " ")
}

// popupOf pulls the answer popup out of a rendered screen so a failure shows
// the box the assertion is about rather than the whole terminal. It cuts each
// row to the frame's own columns: the workspace behind the popup shows through
// on either side, and a stray tab label there would otherwise land in the
// middle of a wrapped question.
func popupOf(view string) string {
	var out []string
	left, right := -1, -1
	for _, line := range strings.Split(view, "\n") {
		switch {
		case left < 0 && strings.Contains(line, "Answer — #"):
			top := strings.Index(line, "╭")
			left = ansi.StringWidth(line[:top])
			right = ansi.StringWidth(line[:top+strings.Index(line[top:], "╮")]) + 1
			out = append(out, ansi.Cut(line, left, right))
		case left >= 0:
			row := ansi.Cut(line, left, right)
			out = append(out, row)
			if strings.HasPrefix(row, "╰") {
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

// TestAnswerFormWrapsInCells is the answer form's share of task 073
// decision 2. It wraps through wrapText and measures with the output pane's
// cols, so correcting the measurement corrected this too — and a question in
// CJK or an option carrying an emoji is exactly the case a rune count got
// wrong, by up to a factor of two.
//
// The narrow widths are new with task 135.1 decision 1, and deliberate: the
// form's old wrapPlain floored the width at 8 and left the overflow to
// frame(), so below 8 its lines were 8 wide by design. wrapText honours the
// width all the way down; 2 is the narrowest a wide rune can honour.
func TestAnswerFormWrapsInCells(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("日本語のテキスト ", 10),
		strings.Repeat("🎉 done ", 20),
		strings.Repeat("👨‍👩‍👧 family ", 15),
		strings.Repeat("éẍ ", 40),
		strings.Repeat("日", 50),
	} {
		for _, width := range []int{2, 5, 20, 40, 60} {
			for i, line := range wrapText(text, width, wrapOpts{}) {
				if w := ansi.StringWidth(line); w > width {
					t.Errorf("width %d: line %d is %d columns: %q", width, i, w, line)
				}
			}
		}
	}
}

// The chat's pinned question takes 1-9 (task 170). cardForm is that form over
// a submitter that records what it was handed.
func cardForm(req apiclient.InputRequest) (f *answerForm, key func(string) tea.Cmd, sent *[]apiclient.InputResponse) {
	f = newAnswerForm(req)
	f.card = true
	sent = &[]apiclient.InputResponse{}
	key = func(k string) tea.Cmd {
		cmd, _ := f.updateWith(keyPress(k), func(resp apiclient.InputResponse) tea.Cmd {
			*sent = append(*sent, resp)
			return func() tea.Msg { return nil }
		})
		return cmd
	}
	return f, key, sent
}

func oneQuestion(multi bool) apiclient.InputRequest {
	return apiclient.InputRequest{
		Kind: apiclient.InputKindQuestion,
		Questions: []apiclient.InputQuestion{
			{Text: "Which color?", Options: []string{"Blue", "Red", "Green"}, MultiSelect: multi},
		},
	}
}

// TestAnswerFormDigitPicksWithinTheCursorsQuestion: a digit names an option
// of the question the cursor is in, moves there, and — with a second question
// still open — submits nothing.
func TestAnswerFormDigitPicksWithinTheCursorsQuestion(t *testing.T) {
	f, key, sent := cardForm(questionRequest())
	if cmd := key("2"); cmd != nil {
		t.Error("a digit submitted a request with two questions")
	}
	if got := f.answers["Which color?"]; len(got) != 1 || got[0] != "Red" {
		t.Errorf("answers = %v, want Red", got)
	}
	if row, _ := f.currentRow(); row.kind != rowOption || row.question != 0 || row.option != 1 {
		t.Errorf("the cursor is on %+v, want the picked option", row)
	}
	// Into the second question: the same digit now names its option.
	key("j") // free text of question 0
	key("j") // a.go
	key("2")
	if got := f.answers["Which files?"]; len(got) != 1 || got[0] != "b.go" {
		t.Errorf("second question's answers = %v, want b.go", got)
	}
	if got := f.answers["Which color?"]; len(got) != 1 || got[0] != "Red" {
		t.Errorf("a digit in the second question changed the first: %v", got)
	}
	if len(*sent) != 0 {
		t.Errorf("digits submitted %d times; enter submits a form with two questions", len(*sent))
	}
	if key("enter") == nil || len(*sent) != 1 {
		t.Error("enter did not submit the answered form")
	}
}

// TestAnswerFormDigitSubmitsOneSingleSelectQuestion: there the pick is the
// whole answer, and it goes through the path enter uses.
func TestAnswerFormDigitSubmitsOneSingleSelectQuestion(t *testing.T) {
	f, key, sent := cardForm(oneQuestion(false))
	if key("3") == nil {
		t.Fatal("a digit on one single-select question did not submit")
	}
	if len(*sent) != 1 || len((*sent)[0].Answers["Which color?"]) != 1 || (*sent)[0].Answers["Which color?"][0] != "Green" {
		t.Fatalf("submitted %+v, want Green once", *sent)
	}
	if !f.submitting {
		t.Error("the form is not marked submitting, as enter leaves it")
	}
	// The same guard enter has: one answer in flight.
	if key("1") != nil || len(*sent) != 1 {
		t.Error("a second digit submitted while the first answer was in flight")
	}
}

// TestAnswerFormDigitTogglesMultiSelect: a multi-select has no last choice,
// so a digit only toggles and enter still submits.
func TestAnswerFormDigitTogglesMultiSelect(t *testing.T) {
	f, key, sent := cardForm(oneQuestion(true))
	key("1")
	key("3")
	if got := f.answers["Which color?"]; len(got) != 2 || got[0] != "Blue" || got[1] != "Green" {
		t.Fatalf("answers = %v, want Blue and Green", got)
	}
	key("1")
	if got := f.answers["Which color?"]; len(got) != 1 || got[0] != "Green" {
		t.Errorf("answers = %v, want the first toggled back off", got)
	}
	if len(*sent) != 0 {
		t.Error("a digit submitted a multi-select question")
	}
	if key("enter") == nil || len(*sent) != 1 {
		t.Error("enter did not submit")
	}
}

// TestAnswerFormDigitOutOfRangeIsIgnored: a number past the options changes
// nothing — not the answer, not the cursor.
func TestAnswerFormDigitOutOfRangeIsIgnored(t *testing.T) {
	f, key, sent := cardForm(oneQuestion(false))
	before := f.cursor
	for _, k := range []string{"4", "9", "0"} {
		if key(k) != nil {
			t.Errorf("%s submitted", k)
		}
	}
	if len(f.answers) != 0 || f.cursor != before || len(*sent) != 0 || f.dirty {
		t.Errorf("an out-of-range digit changed the form: answers %v, cursor %d", f.answers, f.cursor)
	}
}

// TestAnswerFormDigitsAreTextWhileEditing: the open field owns the keyboard.
func TestAnswerFormDigitsAreTextWhileEditing(t *testing.T) {
	f, key, sent := cardForm(oneQuestion(false))
	key("t")
	if !f.editing {
		t.Fatal("t did not open the free-text field")
	}
	key("2")
	key("1")
	if got := f.editor.Value(); got != "21" {
		t.Errorf("the field holds %q, want the digits typed", got)
	}
	if len(f.answers) != 0 || len(*sent) != 0 {
		t.Error("a digit typed into the field picked an option")
	}
}

// TestAnswerFormDigitDecidesAPermissionWithoutSubmitting: 1 and 2 are allow
// and deny, and a decision about what runs still takes its own enter.
func TestAnswerFormDigitDecidesAPermissionWithoutSubmitting(t *testing.T) {
	f, key, sent := cardForm(apiclient.InputRequest{
		Kind:       apiclient.InputKindPermission,
		Permission: &apiclient.InputPermission{Tool: "Bash", Summary: "rm -rf build"},
	})
	if key("2") != nil || f.allow == nil || *f.allow {
		t.Errorf("2 left allow = %v, want deny picked and nothing sent", f.allow)
	}
	if key("1") != nil || f.allow == nil || !*f.allow {
		t.Errorf("1 left allow = %v, want allow picked and nothing sent", f.allow)
	}
	key("3")
	if len(*sent) != 0 {
		t.Error("a digit submitted a permission decision")
	}
	if key("enter") == nil || len(*sent) != 1 {
		t.Error("enter did not submit the decision")
	}
}

// TestAnswerFormCardNumbersItsOptions: the digits are drawn, 1 to 9, and an
// option past the ninth has none.
func TestAnswerFormCardNumbersItsOptions(t *testing.T) {
	opts := []string{"o-a", "o-b", "o-c", "o-d", "o-e", "o-f", "o-g", "o-h", "o-i", "o-j"}
	f, _, _ := cardForm(apiclient.InputRequest{
		Kind:      apiclient.InputKindQuestion,
		Questions: []apiclient.InputQuestion{{Text: "Which?", Options: opts}},
	})
	out := ansi.Strip(f.render(60, 0))
	for i, o := range opts[:9] {
		if want := string(rune('1'+i)) + ". " + o; !strings.Contains(out, want) {
			t.Errorf("the card is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "10.") || strings.Contains(out, ". o-j") {
		t.Errorf("the tenth option is numbered:\n%s", out)
	}
	if !strings.Contains(out, "    o-j") {
		t.Errorf("the tenth option is not in the labels' column:\n%s", out)
	}
	if strings.Contains(out, ". "+answerCardFreeText) {
		t.Errorf("the free-text row is numbered:\n%s", out)
	}
}

// TestAnswerFormPopupIgnoresDigits: the task workspace's popup shares the
// form, and neither its keys nor its rows changed.
func TestAnswerFormPopupIgnoresDigits(t *testing.T) {
	f := newAnswerForm(oneQuestion(false))
	before, cursor := f.render(60, 0), f.cursor
	for _, k := range []string{"1", "2", "3"} {
		if cmd, exit := f.press(k); cmd != nil || exit {
			t.Errorf("%s did something in the popup", k)
		}
	}
	if len(f.answers) != 0 || f.cursor != cursor || f.submitting || f.dirty {
		t.Errorf("a digit changed the popup's form: answers %v, cursor %d", f.answers, f.cursor)
	}
	after := f.render(60, 0)
	if after != before {
		t.Errorf("the popup redrew after a digit:\n%s", after)
	}
	want := []string{
		"  ? Which color?",
		"›   ( ) Blue",
		"    ( ) Red",
		"    ( ) Green",
		"     ✎  type your own answer",
	}
	lines := strings.Split(ansi.Strip(after), "\n")
	for i, w := range want {
		if i >= len(lines) || strings.TrimRight(lines[i], " ") != w {
			t.Fatalf("the popup's rows changed; line %d is %q, want %q\n%s", i, lines[i], w, ansi.Strip(after))
		}
	}
}
