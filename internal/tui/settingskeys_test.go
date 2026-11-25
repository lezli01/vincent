package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/keymap"
)

func bindingsFixture(t *testing.T) *settingsView {
	t.Helper()
	v := settingsFixture(t)
	v.client = offlineClient()
	v.openForm(tuiKeysPath)
	if v.bindings == nil {
		t.Fatal("the key bindings row did not open the screen")
	}
	return v
}

// bindingCursorOn puts the screen's cursor on one operation.
func bindingCursorOn(t *testing.T, v *settingsView, op keymap.Op) {
	t.Helper()
	for i, info := range v.bindings.ops {
		if info.Op == op {
			v.bindings.cursor = i
			return
		}
	}
	t.Fatalf("no operation %s on the screen", op)
}

func init() {
	panelKeyProbes[ctxKeyBindings] = map[string]func(*testing.T){
		"down": func(t *testing.T) {
			v := bindingsFixture(t)
			v.updateKey(registryKey(t, "down"))
			if v.bindings.cursor != 1 {
				t.Fatalf("down left the cursor on %d", v.bindings.cursor)
			}
		},
		"enter": func(t *testing.T) {
			v := bindingsFixture(t)
			v.updateKey(registryKey(t, "enter"))
			if !v.bindings.capturing || !v.capturesInput() {
				t.Fatal("enter did not start taking a key press")
			}
		},
		"backspace": func(t *testing.T) {
			v := bindingsFixture(t)
			bindingCursorOn(t, v, keymap.Refresh)
			v.bindings.overrides["refresh"] = "f5"
			if cmd := v.updateKey(registryKey(t, "backspace")); cmd == nil || !v.bindings.saving {
				t.Fatal("backspace on a changed operation did not save its default back")
			}
		},
		"u": func(t *testing.T) {
			v := bindingsFixture(t)
			v.undo = &settingsUndo{op: keymap.Refresh, opWas: "f5"}
			if cmd := v.updateKey(registryKey(t, "u")); cmd == nil || !v.bindings.saving {
				t.Fatal("u did not put the last change back")
			}
		},
		"esc": func(t *testing.T) {
			v := bindingsFixture(t)
			v.updateKey(registryKey(t, "esc"))
			if v.bindings != nil {
				t.Fatal("esc did not go back to Settings")
			}
		},
		"t": func(t *testing.T) {
			v := bindingsFixture(t)
			v.updateKey(registryKey(t, "t"))
			if v.bindings != nil || v.form == nil || v.form.key.path != tuiKeysPath {
				t.Fatal("t did not open the pairs as text")
			}
		},
	}
}

// #1079: every operation is listed with its key and default, a clash is
// named before anything is saved, and a free key is saved.
func TestKeyBindingsScreen(t *testing.T) {
	v := bindingsFixture(t)
	out := ansi.Strip(v.render(180, 44))
	for _, want := range []string{"Key bindings", "refresh", "KEYS SCREENS SHARE"} {
		if !strings.Contains(out, want) {
			t.Errorf("the screen does not show %q:\n%s", want, out)
		}
	}
	v.updateKey(synthKey("end"))
	if out := ansi.Strip(v.render(180, 44)); !strings.Contains(out, "pause_all") || !strings.Contains(out, "GLOBAL KEYS") {
		t.Errorf("end does not reach the last operation:\n%s", out)
	}
	v.bindings.cursor = 0
	if len(v.bindings.ops) != len(keymap.Catalog()) {
		t.Fatalf("the screen lists %d operations, the catalog has %d", len(v.bindings.ops), len(keymap.Catalog()))
	}

	bindingCursorOn(t, v, keymap.Refresh)
	v.updateKey(namedKey("enter"))
	if cmd := v.updateKey(keyPress("q")); cmd != nil || v.bindings.saving {
		t.Fatal("a clash with quit's q was saved")
	}
	if !v.status().bad || !strings.Contains(v.status().text, "not saved") {
		t.Fatalf("the clash is not named: %q", v.status().text)
	}

	v.updateKey(namedKey("enter"))
	cmd := v.updateKey(synthKey("f5"))
	if cmd == nil || !v.bindings.saving {
		t.Fatalf("a free key was not saved: %q", v.status().text)
	}
}

// esc while waiting for a key changes nothing.
func TestKeyBindingsEscKeepsTheKey(t *testing.T) {
	v := bindingsFixture(t)
	v.updateKey(namedKey("enter"))
	if cmd := v.updateKey(namedKey("esc")); cmd != nil || v.bindings == nil || v.bindings.capturing {
		t.Fatal("esc while capturing did not just cancel")
	}
}

// keysSaved is the daemon's answer to a save from the screen: the
// configuration with these overrides in force.
func keysSaved(v *settingsView, keys map[string]string) configSavedMsg {
	cfg := v.config
	cfg.TUI.Keys = keys
	return configSavedMsg{path: tuiKeysPath, cfg: cfg}
}

// #1158: backspace reset a binding with nothing to take it back. The reset
// says what the key was and `u` puts it back — Settings' one undo slot,
// named, and not itself undoable.
func TestKeyBindingsResetIsUndoable(t *testing.T) {
	v := bindingsFixture(t)
	v.update(keysSaved(v, map[string]string{"refresh": "f5"}))
	v.bindings.adopt(v.config)
	bindingCursorOn(t, v, keymap.Refresh)
	if cmd := v.updateKey(namedKey("backspace")); cmd == nil {
		t.Fatal("backspace did not reset")
	}
	v.update(keysSaved(v, map[string]string{}))
	if note := v.status().text; !strings.Contains(note, "was f5") || !strings.Contains(note, "u puts it back") {
		t.Fatalf("the reset does not say how to take it back: %q", note)
	}
	bindingCursorOn(t, v, keymap.Quit)
	if cmd := v.updateKey(keyPress("u")); cmd == nil || !v.bindings.saving {
		t.Fatalf("u did not put the binding back: %q", v.status().text)
	}
	if got := v.bindings.sending["refresh"]; got != "f5" {
		t.Fatalf("u sends refresh=%q, want f5", got)
	}
	if v.bindings.ops[v.bindings.cursor].Op != keymap.Refresh {
		t.Fatal("u did not put the cursor on the operation it puts back")
	}
	v.update(keysSaved(v, map[string]string{"refresh": "f5"}))
	if note := v.status().text; !strings.Contains(note, "refresh put back to f5") {
		t.Fatalf("the undo is not named: %q", note)
	}
	if v.undo != nil {
		t.Fatalf("the undo is itself undoable: %+v", v.undo)
	}
	if cmd := v.updateKey(keyPress("u")); cmd != nil || !strings.Contains(v.status().text, "nothing to undo") {
		t.Fatalf("a second u: note %q", v.status().text)
	}
}

// #1158: the slot is Settings' own, so `u` after leaving the screen still
// takes a reset back — on the screen, where the change is seen.
func TestKeyBindingsResetUndoneFromSettings(t *testing.T) {
	v := bindingsFixture(t)
	v.update(keysSaved(v, map[string]string{"refresh": "f5"}))
	v.bindings.adopt(v.config)
	bindingCursorOn(t, v, keymap.Refresh)
	v.updateKey(namedKey("backspace"))
	v.update(keysSaved(v, map[string]string{}))
	v.updateKey(namedKey("esc"))
	v.updateKey(namedKey("tab"))
	if cmd := v.updateKey(keyPress("u")); cmd == nil || v.bindings == nil || !v.bindings.saving {
		t.Fatal("u in Settings did not reopen the screen to put the binding back")
	}
	if got := v.bindings.sending["refresh"]; got != "f5" {
		t.Fatalf("u sends refresh=%q, want f5", got)
	}
}

// #1158: a failed undo is not left pending, so the next save is a change
// of its own and can be undone in turn.
func TestKeyBindingsFailedUndoIsNotPending(t *testing.T) {
	v := bindingsFixture(t)
	v.update(keysSaved(v, map[string]string{"refresh": "f5"}))
	v.bindings.adopt(v.config)
	bindingCursorOn(t, v, keymap.Refresh)
	v.updateKey(namedKey("backspace"))
	v.update(keysSaved(v, map[string]string{}))
	v.updateKey(keyPress("u"))
	v.update(configSavedMsg{path: tuiKeysPath, err: errDaemonUnreachable})
	if v.undoing {
		t.Fatal("a failed undo is still pending")
	}
	v.updateKey(namedKey("enter"))
	v.updateKey(synthKey("f6"))
	v.update(keysSaved(v, map[string]string{"refresh": "f6"}))
	if v.undo == nil || !strings.Contains(v.status().text, "u puts it back") {
		t.Fatalf("the next change is not undoable: note %q", v.status().text)
	}
}

// #1208: binding next_item to the key next_chat was moved to is the pair
// exception, not a clash, so the screen saves it.
func TestKeyBindingsPairSharesAnyKey(t *testing.T) {
	v := bindingsFixture(t)
	v.update(keysSaved(v, map[string]string{"next_chat": "alt+n"}))
	v.bindings.adopt(v.config)
	bindingCursorOn(t, v, keymap.NextItem)
	v.updateKey(namedKey("enter"))
	if cmd := v.updateKey(synthKey("alt+n")); cmd == nil || !v.bindings.saving {
		t.Fatalf("next_item on next_chat's alt+n was not saved: %q", v.status().text)
	}
	if got := v.bindings.sending["next_item"]; got != "alt+n" {
		t.Fatalf("the save sends next_item=%q, want alt+n", got)
	}
}
