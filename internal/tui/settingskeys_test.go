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
	if !v.bindings.noteBad || !strings.Contains(v.bindings.note, "not saved") {
		t.Fatalf("the clash is not named: %q", v.bindings.note)
	}

	v.updateKey(namedKey("enter"))
	cmd := v.updateKey(synthKey("f5"))
	if cmd == nil || !v.bindings.saving {
		t.Fatalf("a free key was not saved: %q", v.bindings.note)
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
