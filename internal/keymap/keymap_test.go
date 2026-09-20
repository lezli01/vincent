package keymap

import (
	"strings"
	"testing"
)

// TestDefaultsPassTheirOwnChecker is decision 4's half that the shipped
// keymap is held to: the same Check an override is refused by.
func TestDefaultsPassTheirOwnChecker(t *testing.T) {
	if err := Check(Default()); err != nil {
		t.Fatalf("the shipped keymap fails §15's clauses: %v", err)
	}
	km, err := Build(nil)
	if err != nil {
		t.Fatalf("an absent tui.keys is refused: %v", err)
	}
	for _, info := range Catalog() {
		if got := km.Key(info.Op); got != info.Default {
			t.Errorf("%s: an empty map binds %q, want the default %q", info.Op, got, info.Default)
		}
	}
}

func TestBuildRefusals(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]string
		// want are substrings the one error must carry: the operation, the
		// key and what it collides with.
		want []string
	}{
		{"unknown operation", map[string]string{"reload": "ctrl+r"}, []string{"reload", "unknown operation", "refresh"}},
		{"fixed operation", map[string]string{"group": "G"}, []string{"group", "not rebindable", "surface-local"}},
		{"esc is fixed", map[string]string{"esc": "q"}, []string{"esc", "not rebindable", "layer stack"}},
		{"not a key", map[string]string{"refresh": "reload"}, []string{"refresh", `"reload" is not a key`}},
		{"shifted letter", map[string]string{"refresh": "shift+r"}, []string{"refresh", `"R"`}},
		{"a live global", map[string]string{"refresh": "q"}, []string{"refresh", `"q"`, "quit"}},
		{"a §6 action", map[string]string{"filter": "p"}, []string{"filter", `"p"`, "pause"}},
		{"a fixed panel row", map[string]string{"refresh": "g"}, []string{"refresh", `"g"`, "task table", "group the tasks"}},
		{"a reserved vim alias", map[string]string{"archive": "j"}, []string{"archive", `"j"`, "move down (vim)"}},
		{"second meaning on a disjoint surface", map[string]string{"delete": "i"}, []string{"delete", `"i"`, "workflows", "structured form"}},
		{"an operation's key on another operation", map[string]string{"browser": "e"}, []string{`"e"`, "editor", "browser"}},
		{"an exception does not travel with the key", map[string]string{"refresh": "r"}, []string{"refresh", `"r"`, "retry"}},
		{"printable key for a text-field escape hatch", map[string]string{"palette_alt": "P"}, []string{"palette_alt", `"P"`, "text field types"}},
		{"help_alt on a printable key", map[string]string{"help_alt": "space"}, []string{"help_alt", `"space"`, "text field types"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Build(c.overrides)
			if err == nil {
				t.Fatalf("%v was accepted", c.overrides)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
		})
	}
}

func TestBuildAccepts(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]string
		want      map[Op]string
	}{
		{"a free ctrl key", map[string]string{"refresh": "ctrl+e"}, map[Op]string{Refresh: "ctrl+e", Repair: "R"}},
		{"a function key", map[string]string{"quit": "f10"}, map[Op]string{Quit: "f10"}},
		{"the default is a no-op", map[string]string{"archive": "A"}, map[Op]string{Archive: "A"}},
		// Replace, not alias (decision 6): the vacated key is free for the
		// other operation in the same edit.
		{"a swap", map[string]string{"pause": "x", "reject": "p"}, map[Op]string{Pause: "x", Reject: "p"}},
		// f3 rather than f2: f2 is the chat workspace's fixed alias for the
		// skill list (task 124.13), and one key means one thing (§15).
		{"text-field hatch on a function key", map[string]string{"palette_alt": "f3", "help_alt": "alt+h"}, map[Op]string{PaletteAlt: "f3", HelpAlt: "alt+h"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			km, err := Build(c.overrides)
			if err != nil {
				t.Fatalf("%v was refused: %v", c.overrides, err)
			}
			for op, want := range c.want {
				if got := km.Key(op); got != want {
					t.Errorf("%s = %q, want %q", op, got, want)
				}
			}
		})
	}
}

// TestCatalogIsWellFormed: one entry per id, every default a key ParseKey
// accepts, and every exception naming operations that exist on its key by
// default — an exception for a key nothing holds is a stale decision.
func TestCatalogIsWellFormed(t *testing.T) {
	seen := map[Op]bool{}
	for _, info := range Catalog() {
		if seen[info.Op] {
			t.Errorf("%s listed twice", info.Op)
		}
		seen[info.Op] = true
		if _, err := ParseKey(info.Default); err != nil {
			t.Errorf("%s: default %v", info.Op, err)
		}
		if len(info.Surfaces) == 0 {
			t.Errorf("%s: answered nowhere", info.Op)
		}
	}
	for _, e := range exceptions {
		for _, op := range e.ops {
			if info, ok := Lookup(op); !ok || info.Default != e.key {
				t.Errorf("exception on %q names %s, which is not on that key by default", e.key, op)
			}
		}
		if e.why == "" {
			t.Errorf("exception on %q records no decision", e.key)
		}
	}
	for _, f := range FixedKeys() {
		if _, err := ParseKey(f.Key); err != nil {
			t.Errorf("fixed %s on %s: %v", f.Key, f.Surface, err)
		}
	}
}
