package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The pane's own probes. The live regression for the data loss is in
// workfloweditorlive_test.go, against the real handlers and the bytes on disk;
// these pin the overlay's contract so a change to it fails here first.

func ctrlS() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl} }

// TestEditorPaneSaveWithoutTypingWritesNothing is claim 2 in miniature: the
// commit closure is never called for a pane nobody edited, so opening a prompt
// and closing it cannot rewrite the file.
func TestEditorPaneSaveWithoutTypingWritesNothing(t *testing.T) {
	const body = "first\n\n  indented\n\nlast\n"
	committed := 0
	pane, _ := newWFEditorPane(body, func(string) tea.Cmd { committed++; return nil })
	if pane.Value() != body {
		t.Errorf("the pane altered a value it was only shown:\n%q", pane.Value())
	}
	overlay, cmd := pane.Update(ctrlS())
	if committed != 0 {
		t.Errorf("ctrl+s on a clean pane sent %d writes, want none", committed)
	}
	if overlay != nil || cmd != nil {
		t.Errorf("ctrl+s on a clean pane did not simply close: overlay %v cmd %v", overlay, cmd)
	}
}

// A pane that was typed into commits, newlines and all.
func TestEditorPaneSaveCommitsTheEditedBody(t *testing.T) {
	var got string
	pane, _ := newWFEditorPane("one\ntwo", func(v string) tea.Cmd { got = v; return nil })
	next, _ := pane.Update(tea.KeyPressMsg{Code: 'X', Text: "X"})
	pane, _ = next.(*wfEditorPane)
	if !pane.Dirty() {
		t.Fatal("typing did not mark the pane dirty")
	}
	if overlay, _ := pane.Update(ctrlS()); overlay != nil {
		t.Error("ctrl+s did not close the pane")
	}
	if !strings.Contains(got, "X") || !strings.Contains(got, "\n") {
		t.Errorf("committed body = %q, want the edited multi-line value", got)
	}
}

// esc abandons the edit: no write, whatever was typed.
func TestEditorPaneEscapeDiscards(t *testing.T) {
	committed := 0
	pane, _ := newWFEditorPane("body", func(string) tea.Cmd { committed++; return nil })
	next, _ := pane.Update(tea.KeyPressMsg{Code: 'z', Text: "z"})
	overlay, _ := next.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if overlay != nil || committed != 0 {
		t.Errorf("esc wrote or stayed open: overlay %v, writes %d", overlay, committed)
	}
}

// A ControlText row opens the pane rather than the one-line field, which is
// the whole of the fix at the editor's level.
func TestEditorTextRowOpensThePane(t *testing.T) {
	w := editorFixtureWith(t, promptDefinition("alpha\n\nbeta\n"))
	w.editor.path = "steps[0]"
	w.editor.rebuild()
	w.editor.cursor = editorRowIndex(t, w, "prompt")
	w.updateKey(registryKey(t, "enter"))
	if w.editor.input != nil {
		t.Fatal("a multi-line row opened the one-line field")
	}
	pane, ok := w.editor.overlay.(*wfEditorPane)
	if !ok {
		t.Fatalf("overlay = %T, want the multi-line pane", w.editor.overlay)
	}
	if pane.Value() != "alpha\n\nbeta\n" {
		t.Errorf("the pane flattened the value: %q", pane.Value())
	}
	if !w.capturesInput() {
		t.Error("the pane does not hold the keyboard: q would quit the TUI mid-edit")
	}
	// FullPane, so the body is the pane rather than the rows behind it.
	if !pane.FullPane() {
		t.Error("the pane does not claim the body")
	}
	if _, cmd := w.updateKey(registryKey(t, "ctrl+s")); cmd != nil {
		t.Error("ctrl+s without an edit sent a write")
	}
	if w.editor.overlay != nil {
		t.Error("ctrl+s did not close the pane")
	}
}

// promptDefinition is one agent step whose prompt is a block scalar.
func promptDefinition(prompt string) apiclient.WorkflowDefinition {
	return apiclient.WorkflowDefinition{
		Name: "review", Scope: "global", File: "/tmp/review.yaml",
		Definition: &apiclient.WorkflowBody{
			Name:  "review",
			Steps: []apiclient.WorkflowStepDef{{ID: "plan", Type: "agent", Prompt: prompt}},
		},
	}
}

// The map sub-form edits the row's own `k=v` text and commits a flow mapping,
// which is what a mapping row used to write a quoted scalar over.
func TestEditorMapSubFormCommitsAMapping(t *testing.T) {
	var got string
	form := newWFEditorMap("A=1, B=2", func(v string) tea.Cmd { got = v; return nil })
	if form.Dirty() {
		t.Error("a freshly opened sub-form reports itself edited")
	}
	// Drop the first pair, then add one.
	form.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	form.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // onto the add row
	form.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, r := range "C=3" {
		form.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	form.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !form.Dirty() {
		t.Fatal("the sub-form did not notice the edit")
	}
	overlay, _ := form.Update(ctrlS())
	if overlay != nil {
		t.Error("ctrl+s did not close the sub-form")
	}
	if got != "B=2, C=3" {
		t.Fatalf("committed row = %q, want B=2, C=3", got)
	}
	if flow := renderFlowMap(got); flow != "{B: 2, C: 3}" {
		t.Errorf("renderFlowMap(%q) = %q, want a YAML flow mapping", got, flow)
	}
}
