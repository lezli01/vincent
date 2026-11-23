package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// #1056: an accepted write can be taken back with u, which writes the value
// it replaced; the screen says what that value was.
func TestEditorUndoPutsTheValueBack(t *testing.T) {
	w := editorFixture(t)
	e := w.editor
	i := editorRowIndex(t, w, "description")
	e.cursor = i
	was := e.rows[i].value
	if cmd := w.commitRow(e.rows[i], "a new description"); cmd == nil || e.pending == nil {
		t.Fatal("the write was not sent")
	}
	w.updateEditorMsg(wfEditorSavedMsg{result: apiclient.WorkflowWriteResult{Version: "v2"}})
	if e.undo == nil || e.undo.row.value != was {
		t.Fatalf("undo = %+v, want the replaced value %q", e.undo, was)
	}
	if got := ansi.Strip(w.render(100, 40)); !strings.Contains(got, "u puts it back") {
		t.Errorf("the screen does not offer the undo:\n%s", got)
	}
	e.cursor = 0
	if _, cmd := w.updateKey(registryKey(t, "u")); cmd == nil {
		t.Fatal("u sent nothing")
	}
	if e.cursor != i || e.rows[i].value != was {
		t.Fatalf("u wrote %q at row %d, want %q at %d", e.rows[i].value, e.cursor, was, i)
	}
	w.updateEditorMsg(wfEditorSavedMsg{result: apiclient.WorkflowWriteResult{Version: "v3"}})
	if e.undo != nil {
		t.Fatal("an undo is itself undoable")
	}
}

// #1056: the remove confirmation takes y only — enter, the editor's edit
// key, cancels it.
func TestEditorRemoveTakesYOnly(t *testing.T) {
	w := editorFixture(t)
	for i, r := range w.editor.rows {
		if r.list != "" {
			w.editor.cursor = i
			break
		}
	}
	w.updateKey(registryKey(t, "d"))
	if w.editor.overlay == nil {
		t.Fatal("d asked nothing")
	}
	if _, cmd := w.updateKey(registryKey(t, "enter")); cmd != nil || w.editor.saving {
		t.Fatal("enter removed the item")
	}
	w.updateKey(registryKey(t, "d"))
	if _, cmd := w.updateKey(registryKey(t, "y")); cmd == nil {
		t.Fatal("y did not remove the item")
	}
}
