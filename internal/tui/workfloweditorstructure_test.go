package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The structural keys' probes. What they pin is the shape of the ops the form
// emits; that the daemon accepts them is pinned live, against the real
// handlers and the bytes on disk.

// editorStepRow puts the cursor on the nth list member of the open form.
func editorStepRow(t *testing.T, w *workflowsView, n int) {
	t.Helper()
	seen := 0
	for i, row := range w.editor.rows {
		if row.list == "" {
			continue
		}
		if seen == n {
			w.editor.cursor = i
			return
		}
		seen++
	}
	t.Fatalf("no list member %d: %+v", n, w.editor.rows)
}

// TestEditorInsertCarriesTheTypesRequiredFields is the criterion the brief
// states: the PATCH that follows an `a` must not be refusable for a field the
// form itself failed to write.
func TestEditorInsertCarriesTheTypesRequiredFields(t *testing.T) {
	w := editorFixture(t)
	e := w.editor
	for _, tc := range []struct{ typ, key string }{
		{"agent", "prompt"},
		{"command", "run"},
		{"manual", "instructions"},
		{"condition", "if"},
		{"include", "workflow"},
	} {
		ops := e.insertOps(wfListTarget{list: "steps", kind: "steps", index: 0}, tc.typ)
		if len(ops) == 0 {
			t.Fatalf("%s: no ops", tc.typ)
		}
		if ops[0].Op != apiclient.WorkflowOpInsert || ops[0].Path != "steps[0]" {
			t.Errorf("%s: op = %+v, want an insert at steps[0]", tc.typ, ops[0])
		}
		keys := map[string]string{}
		for _, f := range ops[0].Item {
			keys[f.Key] = f.Value
		}
		for _, want := range []string{"id", "type", tc.key} {
			if _, ok := keys[want]; !ok {
				t.Errorf("%s: the skeleton has no %s: %+v", tc.typ, want, ops[0].Item)
			}
		}
		if keys["type"] != tc.typ {
			t.Errorf("%s: type = %q", tc.typ, keys["type"])
		}
	}
}

// A group and a fan_out are invalid while they are empty (§8.2, §7.6), so the
// same PATCH carries the child that makes them valid — the daemon applies the
// ops in order and validates once, at the end.
func TestEditorInsertCompletesAStructuralStep(t *testing.T) {
	w := editorFixture(t)
	for _, tc := range []struct{ typ, child string }{
		{"parallel", "steps[0].steps[0]"},
		{"loop", "steps[0].steps[0]"},
		{"fan_out", "steps[0].lanes[0]"},
	} {
		ops := w.editor.insertOps(wfListTarget{list: "steps", kind: "steps", index: 0}, tc.typ)
		paths := make([]string, 0, len(ops))
		ids := map[string]bool{}
		for _, op := range ops {
			paths = append(paths, op.Path)
			for _, f := range op.Item {
				if f.Key == "id" {
					if ids[f.Value] {
						t.Errorf("%s: two entries of one PATCH share the id %s", tc.typ, f.Value)
					}
					ids[f.Value] = true
				}
			}
		}
		found := false
		for _, p := range paths {
			if p == tc.child {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: ops %v carry no %s", tc.typ, paths, tc.child)
		}
	}
	// A lane with only an id is refused too: it names no workflow and carries
	// no steps, so the skeleton gives it one.
	ops := w.editor.insertOps(wfListTarget{list: "steps[0].lanes", kind: "lanes"}, "")
	if len(ops) != 2 || ops[1].Path != "steps[0].lanes[0].steps[0]" {
		t.Errorf("lane skeleton = %+v, want the lane and an inline step", ops)
	}
}

// A declared field's skeleton is its two required keys.
func TestEditorInsertBuildsADeclaredField(t *testing.T) {
	w := editorFixture(t)
	ops := w.editor.insertOps(wfListTarget{list: "fields", kind: "fields"}, "")
	if len(ops) != 1 || ops[0].Path != "fields[0]" {
		t.Fatalf("field skeleton = %+v", ops)
	}
	keys := map[string]string{}
	for _, f := range ops[0].Item {
		keys[f.Key] = f.Value
	}
	if keys["name"] == "" || keys["type"] == "" {
		t.Errorf("a declared field skeleton is missing a required key: %+v", ops[0].Item)
	}
}

// An id a PATCH generates cannot collide with one the file already has: the
// daemon refuses a duplicate, and the refusal would name a step the form
// wrote rather than one the author did.
func TestEditorInsertPicksAFreeID(t *testing.T) {
	w := editorFixture(t)
	used := w.editor.usedIDs()
	for _, want := range []string{"plan", "check", "ship"} {
		if !used[want] {
			t.Errorf("usedIDs missed %s: %v", want, used)
		}
	}
	ops := w.editor.insertOps(wfListTarget{list: "steps", kind: "steps", index: 0}, "command")
	for _, f := range ops[0].Item {
		if f.Key == "id" && used[f.Value] {
			t.Errorf("the skeleton reused the id %s", f.Value)
		}
	}
}

// `a` on a member adds after it, so "another one here" lands where the eye is.
func TestEditorAddInsertsAfterTheCursor(t *testing.T) {
	w := editorFixture(t)
	editorStepRow(t, w, 1)
	ops := w.editor.insertOps(wfListTarget{
		list: "steps", kind: "steps", index: 1, count: 3, onItem: true,
	}, "command")
	if ops[0].Path != "steps[2]" {
		t.Errorf("insert path = %s, want steps[2]", ops[0].Path)
	}
}

// The type picker offers what the descriptor allows in that context, and no
// free-text row: a type §8.2 refuses is a 400 the form must never offer.
func TestEditorAddOffersOnlyLegalTypes(t *testing.T) {
	w := editorFixture(t)
	editorStepRow(t, w, 0)
	w.updateKey(registryKey(t, "a"))
	pick, ok := w.editor.overlay.(*wfEditorPicker)
	if !ok {
		t.Fatalf("a opened %T, want the type picker", w.editor.overlay)
	}
	if pick.picker.allowFree {
		t.Error("the type picker takes free text")
	}
	for _, opt := range pick.picker.options {
		if opt.value == "break" {
			t.Error("break is offered at the top level; §8.2 refuses it there")
		}
	}
}

// `d` never removes on the keystroke: §15 view 5's "no destructive action" is
// narrowed by this issue to "no unconfirmed destructive action".
func TestEditorDeleteAsksFirst(t *testing.T) {
	w := editorFixture(t)
	editorStepRow(t, w, 1)
	_, cmd := w.updateKey(registryKey(t, "d"))
	if cmd != nil {
		t.Fatal("d wrote without asking")
	}
	confirm, ok := w.editor.overlay.(*wfEditorConfirm)
	if !ok {
		t.Fatalf("d opened %T, want the confirmation", w.editor.overlay)
	}
	if !strings.Contains(confirm.prompt, "steps[1]") {
		t.Errorf("the prompt does not say what is being removed: %q", confirm.prompt)
	}
	// Any key but y is "no".
	if overlay, cmd := confirm.Update(tea.KeyPressMsg{Code: 'n', Text: "n"}); overlay != nil || cmd != nil {
		t.Error("n did not cancel the removal")
	}
	if _, cmd := confirm.Update(tea.KeyPressMsg{Code: 'y', Text: "y"}); cmd == nil {
		t.Error("y did not send the removal")
	}
}

// A scalar row is not a list item, so the structural keys say so rather than
// addressing whatever list happens to be nearby.
func TestEditorStructuralKeysRefuseAScalarRow(t *testing.T) {
	for _, key := range []string{"d", "K", "J"} {
		w := editorFixture(t)
		w.editor.cursor = editorRowIndex(t, w, "description")
		if _, cmd := w.updateKey(registryKey(t, key)); cmd != nil {
			t.Errorf("%s on a scalar row sent a write", key)
		}
		if w.editor.err == "" {
			t.Errorf("%s on a scalar row said nothing", key)
		}
	}
}

// K and J emit a move with the destination index, and the cursor follows the
// row it moved.
func TestEditorMoveEmitsAMove(t *testing.T) {
	w := editorFixture(t)
	editorStepRow(t, w, 1)
	before := w.editor.cursor
	if _, cmd := w.updateKey(registryKey(t, "K")); cmd == nil {
		t.Fatal("K sent nothing")
	}
	if w.editor.cursor != before-1 {
		t.Errorf("the cursor did not follow the moved row: %d", w.editor.cursor)
	}
	// At the ends there is nowhere to go, and nothing is written.
	w2 := editorFixture(t)
	editorStepRow(t, w2, 0)
	if _, cmd := w2.updateKey(registryKey(t, "K")); cmd != nil {
		t.Error("K at the top of the list still wrote")
	}
	editorStepRow(t, w2, 2)
	if _, cmd := w2.updateKey(registryKey(t, "J")); cmd != nil {
		t.Error("J at the bottom of the list still wrote")
	}
}

// The list holder rows address the end of the list they head, which is what
// makes an empty list refillable.
func TestEditorListTargetFromTheListItself(t *testing.T) {
	w := editorFixture(t)
	w.editor.cursor = editorRowIndex(t, w, "steps")
	target, ok := w.editor.listTarget()
	if !ok || target.list != "steps" || target.onItem {
		t.Fatalf("target = %+v, ok %v; want the steps list itself", target, ok)
	}
	if target.index != 3 {
		t.Errorf("index = %d, want the end of a three-step list", target.index)
	}
	w.editor.cursor = editorRowIndex(t, w, "fields")
	if target, ok := w.editor.listTarget(); !ok || target.kind != "fields" {
		t.Errorf("the declared-field row addresses %+v", target)
	}
}
