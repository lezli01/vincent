package tui

import (
	"reflect"
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

// #1136: an add and a move are undone by their inverse — the added item
// taken out, the moved one moved back — each written as one PATCH.
func TestEditorUndoTakesBackAnAddAndAMove(t *testing.T) {
	w := editorFixture(t)
	e := w.editor
	editorStepRow(t, w, 0)
	if cmd := w.editorMove(1); cmd == nil {
		t.Fatal("J sent nothing")
	}
	w.updateEditorMsg(wfEditorSavedMsg{result: apiclient.WorkflowWriteResult{Version: "v2"}})
	if e.undo == nil {
		t.Fatal("a move left nothing to undo")
	}
	want := []apiclient.WorkflowOp{{Op: apiclient.WorkflowOpMove, Path: "steps[1]", To: 0}}
	if !reflect.DeepEqual(e.undo.ops, want) {
		t.Fatalf("the move's undo = %+v, want %+v", e.undo.ops, want)
	}
	if got := ansi.Strip(w.render(100, 40)); !strings.Contains(got, "u moves it back") {
		t.Errorf("the screen does not offer the move's undo:\n%s", got)
	}
	if _, cmd := w.updateKey(registryKey(t, "u")); cmd == nil || !e.saving {
		t.Fatal("u did not write the move back")
	}
	w.updateEditorMsg(wfEditorSavedMsg{result: apiclient.WorkflowWriteResult{Version: "v3"}})
	if e.undo != nil {
		t.Fatal("an undone move is itself undoable")
	}

	editorStepRow(t, w, 0)
	cmd := w.editorInsert(wfListTarget{list: "steps", kind: "steps", index: 0, onItem: true}, "command")
	if cmd == nil {
		t.Fatal("the add sent nothing")
	}
	w.updateEditorMsg(wfEditorSavedMsg{result: apiclient.WorkflowWriteResult{Version: "v4"}})
	want = []apiclient.WorkflowOp{{Op: apiclient.WorkflowOpRemove, Path: "steps[1]"}}
	if e.undo == nil || !reflect.DeepEqual(e.undo.ops, want) {
		t.Fatalf("the add's undo = %+v, want %+v", e.undo, want)
	}
	if got := ansi.Strip(w.render(100, 40)); !strings.Contains(got, "u takes it out") {
		t.Errorf("the screen does not offer the add's undo:\n%s", got)
	}
	if _, cmd := w.updateKey(registryKey(t, "u")); cmd == nil || !e.saving {
		t.Fatal("u did not take the added step out")
	}
}

// #1136: a remove cannot be undone — the form holds the item only as parsed
// values, not as the lines the file had — so the confirmation says so, the
// remove leaves nothing to undo, and the hint offers u only when it will
// undo something.
func TestEditorUndoHintOnlyWhenThereIsSomethingToUndo(t *testing.T) {
	w := editorFixture(t)
	e := w.editor
	if got := ansi.Strip(w.render(100, 40)); strings.Contains(got, "u undo") {
		t.Errorf("a fresh editor offers u:\n%s", got)
	}
	i := editorRowIndex(t, w, "description")
	e.cursor = i
	w.commitRow(e.rows[i], "a new description")
	w.updateEditorMsg(wfEditorSavedMsg{result: apiclient.WorkflowWriteResult{Version: "v2"}})
	if got := ansi.Strip(w.render(100, 40)); !strings.Contains(got, "u undo") {
		t.Errorf("after a write the hint does not offer u:\n%s", got)
	}
	editorStepRow(t, w, 0)
	w.updateKey(registryKey(t, "d"))
	if got := ansi.Strip(w.render(100, 40)); !strings.Contains(got, "cannot be undone") {
		t.Errorf("the remove confirmation does not say it is final:\n%s", got)
	}
	if _, cmd := w.updateKey(registryKey(t, "y")); cmd == nil {
		t.Fatal("y did not remove the item")
	}
	w.updateEditorMsg(wfEditorSavedMsg{result: apiclient.WorkflowWriteResult{Version: "v3"}})
	if e.undo != nil {
		t.Fatalf("a remove left an undo: %+v", e.undo)
	}
	if got := ansi.Strip(w.render(100, 40)); strings.Contains(got, "u undo") || strings.Contains(got, "puts it back") {
		t.Errorf("after a remove the screen still offers u:\n%s", got)
	}
}
