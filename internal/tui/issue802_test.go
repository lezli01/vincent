package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// Issue #802: the workflow editor, the workflow overview and the trigger
// screens cannot show content taller or wider than their pane. Spec §15
// "Wrap or truncate, never clip" (task 135): content taller than its pane
// lives in a scrollPane, the focused row's full text is shown wrapped, and
// showing the first rows and dropping the rest is a bug.

// issue802Words is a long value whose last word is distinct, so a test can
// tell a wrapped value from one cut at the first line.
func issue802Words(n int, tail string) string {
	words := make([]string, 0, n+1)
	for i := range n {
		words = append(words, fmt.Sprintf("word%02d", i))
	}
	return strings.Join(append(words, tail), " ")
}

func issue802Plain(s string) string { return ansi.Strip(s) }

// WF-6: in a 40-row editor at 80×24 the cursor row, the error and the key
// hint all stay on screen. renderEditor clamps to the first lines, so the
// cursor on the last row, the error and the hint all fall off the bottom.
func TestIssue802EditorKeepsCursorErrorAndHintOnScreen(t *testing.T) {
	w := editorFixture(t)
	e := w.editor
	rows := make([]wfEditRow, 0, 40)
	for i := range 40 {
		rows = append(rows, wfEditRow{
			field: apiclient.WorkflowSchemaField{Name: fmt.Sprintf("field%02d", i)},
			path:  fmt.Sprintf("steps[%d].prompt", i),
			value: "v",
		})
	}
	e.rows = rows
	e.cursor = 39
	e.err = "steps[39].prompt: refused by the daemon"
	got := issue802Plain(w.renderEditor(80, 24))
	if n := len(strings.Split(got, "\n")); n > 24 {
		t.Fatalf("the editor drew %d lines into a 24-line pane", n)
	}
	for _, want := range []string{"field39", "refused by the daemon", "enter edit"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is not on screen at 80×24 with the cursor on row 40:\n%s", want, got)
		}
	}
}

// WF-7, WF-8: the cursor row's value and help wrap under the label rather
// than being cut to one line; the other rows stay one line ending in `…`.
func TestIssue802FormCursorRowWrapsValueAndHelp(t *testing.T) {
	long := issue802Words(40, "VALUETAIL")
	rows := []wfEditRow{
		{field: apiclient.WorkflowSchemaField{Name: "name"}, value: issue802Words(40, "OTHERTAIL")},
		{field: apiclient.WorkflowSchemaField{Name: "description", Help: issue802Words(20, "HELPTAIL")}, value: long},
	}
	lines := renderFormRows(rows, 1, -1, nil, nil, 80, nil)
	got := issue802Plain(strings.Join(lines, "\n"))
	for _, l := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("a form line is %d cells wide at 80: %q", w, l)
		}
	}
	for _, want := range []string{"VALUETAIL", "HELPTAIL"} {
		if !strings.Contains(got, want) {
			t.Errorf("the cursor row lost %q — its value or help was cut to one line:\n%s", want, got)
		}
	}
	if strings.Contains(got, "OTHERTAIL") {
		t.Errorf("a row off the cursor wrapped; it should stay one line ending in …:\n%s", got)
	}
}

// TR-3: a row's refusal note is on its own line, not appended after a value
// that truncation then cuts away.
func TestIssue802FormRefusalNoteSurvivesALongValue(t *testing.T) {
	rows := []wfEditRow{
		{field: apiclient.WorkflowSchemaField{Name: "if"}, value: issue802Words(40, "IFTAIL")},
		{field: apiclient.WorkflowSchemaField{Name: "match"}, value: "x"},
	}
	note := func(i int) string {
		if i == 0 {
			return styleBad.Render("⚠ refused: the template does not parse")
		}
		return ""
	}
	got := issue802Plain(strings.Join(renderFormRows(rows, 1, -1, nil, nil, 80, note), "\n"))
	if !strings.Contains(got, "refused: the template does not parse") {
		t.Errorf("the refusal note was cut off with the long value:\n%s", got)
	}
}

// TR-1: the trigger form's full-pane editor shows every line of a
// multi-line value, not just the first.
func TestIssue802TriggerFullPaneEditorShowsEveryLine(t *testing.T) {
	v := triggersFormFixture()
	pane, _ := newWFEditorPane("first line\nsecond line\nthird line\nfourth line", nil)
	v.form.overlay = pane
	got := issue802Plain(v.renderForm(100, 30))
	for _, want := range []string{"first line", "second line", "third line", "fourth line"} {
		if !strings.Contains(got, want) {
			t.Errorf("the full-pane editor dropped %q:\n%s", want, got)
		}
	}
}

// WF-13: the workflow overview's hint follows a rebound keymap.Editor rather
// than naming the default `e`.
func TestIssue802WorkflowOverviewHintFollowsRebinding(t *testing.T) {
	withKeymap(t, map[string]string{string(keymap.Editor): "ctrl+e"})
	w := newWorkflowsView()
	w.client = offlineClient()
	w.width, w.height = 130, 40
	loadedWorkflows(w, globalEntry("review"))
	line, ok := w.currentLine()
	if !ok {
		t.Fatal("no workflow line to render")
	}
	got := issue802Plain(w.renderWorkflowOverview(line, 100, 40))
	if strings.Contains(got, "· e edit") {
		t.Errorf("the overview hint still names the default e after rebinding editor:\n%s", got)
	}
	if !strings.Contains(got, "ctrl+e") {
		t.Errorf("the overview hint does not name the rebound ctrl+e:\n%s", got)
	}
}

// TR-7, TR-8: at 80 columns POLL status is visible and no trigger-screen line
// is wider than the screen.
func TestIssue802TriggerListShowsPollAt80Columns(t *testing.T) {
	v := triggersFixture()
	at := "2026-10-20T10:00:00Z"
	v.list.Triggers = []apiclient.TriggerSummary{{
		ID: "github-issue-triage", File: "/cfg/triggers/github-issue-triage.yaml", Version: "v1",
		ProjectID: 1, SourceType: "command", ActionType: "create", Valid: true, Enabled: true,
		Armed: true, Poll: apiclient.TriggerPoll{LastPollAt: &at, OK: false},
	}}
	v.list.Enabled = false
	v.restoreSelection()
	v.width, v.height = 80, 24
	got := issue802Plain(v.render(80, 24))
	if !strings.Contains(got, "failing") {
		t.Errorf("POLL status is not visible at 80 columns:\n%s", got)
	}
	for _, l := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("a trigger-screen line is %d cells wide at 80: %q", w, l)
		}
	}
}

// WF-12: while the step modal is open it owns the mouse. A click never
// reaches the graph behind it, and the wheel scrolls the modal rather than
// the canvas.
func TestIssue802StepModalOwnsTheMouse(t *testing.T) {
	w := graphFixture(t)
	w.render(100, 12)
	w.openStepModal()
	m := w.graph.modal
	if m == nil {
		t.Fatal("the step modal did not open")
	}
	m.vp.SetHeight(2)
	m.vp.SetContent(strings.Repeat("line\n", 40))
	selected := w.graph.graph.Selected()
	canvas := w.graph.graph.ScrollPercent()
	w.updateGraphMouse(tea.MouseClickMsg{Button: tea.MouseLeft, X: graphPaneX + 2, Y: graphPaneY + 7})
	if w.graph.modal != m || w.graph.graph.Selected() != selected {
		t.Error("a click behind the open modal reached the graph")
	}
	w.updateGraphMouse(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.vp.YOffset() == 0 {
		t.Error("the wheel did not scroll the open modal")
	}
	if w.graph.graph.ScrollPercent() != canvas {
		t.Error("the wheel scrolled the canvas behind the open modal")
	}
}
