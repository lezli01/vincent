package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The tests in this file hold formBody and layoutFormRows, which lay out the
// workflow editor and the triggers form, at heights a tall cursor row or a
// long error does not fit (review F8, F9).

func numberedWords(n int) string {
	words := make([]string, n)
	for i := range words {
		words[i] = fmt.Sprintf("w%d", i)
	}
	return strings.Join(words, " ")
}

func tallFormRows(value, help string) []wfEditRow {
	return []wfEditRow{
		{field: apiclient.WorkflowSchemaField{Name: "name"}, value: "short"},
		{field: apiclient.WorkflowSchemaField{Name: "prompt", Help: help}, value: value},
		{field: apiclient.WorkflowSchemaField{Name: "model"}, value: "opus"},
	}
}

// A field typed into whose wrapped value is taller than the room keeps the
// caret's row on screen, not the field's first rows.
func TestFormBodyKeepsTheCaretOfATallField(t *testing.T) {
	value := numberedWords(200)
	input := newTextField()
	input.SetValue(value)
	input.Focus()
	input.CursorEnd()
	body, from, to, focus := layoutFormRows(tallFormRows(value, ""), 1, 1, &input, nil, 80, nil)
	const height = 12
	head := []string{"  editing", ""}
	foot := []string{"", "  enter commit · esc cancel"}
	if to-from <= height-len(head)-len(foot) {
		t.Fatalf("the fixture's span %d fits the room", to-from)
	}
	out := ansi.Strip(formBody(head, body, from, to, focus, foot, height))
	if n := len(strings.Split(out, "\n")); n != height {
		t.Errorf("formBody drew %d lines in %d", n, height)
	}
	if !strings.Contains(out, "w199") {
		t.Errorf("the caret's row, at the value's end, is off screen:\n%s", out)
	}
}

// A cursor row, not being edited, whose value and help are taller than the
// room still shows the help under it.
func TestFormBodyShowsTheHelpOfATallRow(t *testing.T) {
	value := numberedWords(300)
	body, from, to, focus := layoutFormRows(tallFormRows(value, "helpmarker says what the field is"), 1, -1, nil, nil, 80, nil)
	const height = 12
	head := []string{"  editing", ""}
	foot := []string{"", "  enter edit · esc back"}
	out := ansi.Strip(formBody(head, body, from, to, focus, foot, height))
	if n := len(strings.Split(out, "\n")); n != height {
		t.Errorf("formBody drew %d lines in %d", n, height)
	}
	if !strings.Contains(out, "helpmarker") {
		t.Errorf("the cursor row's help is off screen:\n%s", out)
	}
}

// A wrapped error taller than the pane is cut, and the key line under it and
// one row of the form survive.
func TestFormBodyKeepsTheKeysUnderALongError(t *testing.T) {
	body, from, to, focus := layoutFormRows(tallFormRows("v", ""), 0, -1, nil, nil, 80, nil)
	head := []string{"  editing", ""}
	foot := []string{""}
	for _, l := range wrapText(numberedWords(80), 78, wrapOpts{}) {
		foot = append(foot, "  "+l)
	}
	foot = append(foot, "", "  enter edit · esc back")
	for _, height := range []int{3, 5, 8} {
		out := ansi.Strip(formBody(head, body, from, to, focus, foot, height))
		lines := strings.Split(out, "\n")
		if len(lines) != height {
			t.Errorf("height %d: formBody drew %d lines", height, len(lines))
		}
		if !strings.Contains(lines[len(lines)-1], "enter edit") {
			t.Errorf("height %d: the key line is gone:\n%s", height, out)
		}
		if !strings.Contains(out, "short") {
			t.Errorf("height %d: no row of the form is left:\n%s", height, out)
		}
	}
}
