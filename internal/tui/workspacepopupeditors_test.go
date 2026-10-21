package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// typeLinesWith types n lines through a form's own update, enter between
// them, the way typeLines does through a task view.
func typeLinesWith(update func(tea.KeyPressMsg), n int) {
	for i := range n {
		if i > 0 {
			update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		for _, r := range "line" {
			update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
}

// TestCreatePREditorTakesAThirteenthLine is issue #800 W3 on the pull-request
// body: enter is a newline there too, and the field must keep taking them
// while the form stays at editorLines rows of it.
func TestCreatePREditorTakesAThirteenthLine(t *testing.T) {
	f, err := newCreatePRForm(7, "https://github.com/o/r/compare/main...vincent/7-x", "vincent/7-x")
	if err != nil {
		t.Fatal(err)
	}
	f.startEdit(cprBody)
	before := len(f.lines(80))
	typeLinesWith(func(k tea.KeyPressMsg) { f.update(k) }, thirteenLines)
	if got := f.editor.LineCount(); got != thirteenLines {
		t.Fatalf("pull-request body holds %d lines after typing %d", got, thirteenLines)
	}
	if grown := len(f.lines(80)) - before; grown > editorLines {
		t.Errorf("the form grew %d lines for a %d-line body; the field should scroll past %d", grown, thirteenLines, editorLines)
	}
}

// TestPullCommentEditorTakesAThirteenthLine is W3 on the pull-request
// comment popup.
func TestPullCommentEditorTakesAThirteenthLine(t *testing.T) {
	f := newPullCommentForm(7, "#12")
	typeLinesWith(func(k tea.KeyPressMsg) { f.update(k) }, thirteenLines)
	if got := f.editor.LineCount(); got != thirteenLines {
		t.Fatalf("comment holds %d lines after typing %d", got, thirteenLines)
	}
	// The subject, its blank, the field, a blank and the hint.
	if h, most := f.height(80), 4+editorLines; h > most {
		t.Errorf("the comment popup is %d lines for a %d-line body, want at most %d", h, thirteenLines, most)
	}
}

// TestFollowUpPopupStaysBoundedAtThirteenLines is the other half of W3: the
// field takes the lines, and the popup scrolls them rather than growing.
func TestFollowUpPopupStaysBoundedAtThirteenLines(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.followUp = newFollowUpForm(7, 3, "done") })
	f := v.detail.followUp
	v.updateKey(keyPress("j"))
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	before := f.height(80)
	typeLines(v, thirteenLines)
	if grown := f.height(80) - before; grown > editorLines {
		t.Errorf("the follow-up form grew %d lines for a %d-line prompt", grown, thirteenLines)
	}
}

// manyOptions is one question with n options, more than a popup at 80×24
// can show at once.
func manyOptions(n int) apiclient.InputRequest {
	opts := make([]string, n)
	for i := range opts {
		opts[i] = fmt.Sprintf("option %02d", i+1)
	}
	return apiclient.InputRequest{
		Kind:      apiclient.InputKindQuestion,
		Questions: []apiclient.InputQuestion{{Text: "Which option ships first?", Options: opts}},
	}
}

// TestAnswerPopupKeepsTheQuestionInViewAt80x24 is W6: a long option list
// windowed around the cursor scrolled the question off the top, leaving a
// column of radios with nothing saying what they answer.
func TestAnswerPopupKeepsTheQuestionInViewAt80x24(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.form = newAnswerForm(manyOptions(15)) })
	screen := ansi.Strip(v.render(80, 24))
	if !strings.Contains(screen, "? Which option ships first?") {
		t.Fatalf("the question's first line is not on screen at 80×24:\n%s", screen)
	}
	if !strings.Contains(screen, "↓ more") {
		t.Errorf("a window cut below says nothing about it:\n%s", screen)
	}
	// Further down, the window has to move, and says so above.
	for range 10 {
		v.updateKey(keyPress("j"))
	}
	screen = ansi.Strip(v.render(80, 24))
	if !strings.Contains(screen, "› ") || !strings.Contains(screen, "option 11") {
		t.Fatalf("the focused option is not on screen:\n%s", screen)
	}
	if !strings.Contains(screen, "? Which option ships first?") && !strings.Contains(screen, "↑ more") {
		t.Errorf("the question scrolled off with no cue:\n%s", screen)
	}
}

// TestAnswerPopupHintIsPinnedAt120x40 is W6's other half: the status line was
// the form's last content line, visible only after scrolling to the bottom.
func TestAnswerPopupHintIsPinnedAt120x40(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.form = newAnswerForm(manyOptions(40)) })
	screen := ansi.Strip(v.render(120, 40))
	if !strings.Contains(screen, "enter submit") {
		t.Fatalf("the answer popup's keys are not on screen at 120×40:\n%s", screen)
	}
	if !strings.Contains(screen, "ctrl+t task details") {
		t.Errorf("the task popup's hint lost its details key:\n%s", screen)
	}
}
