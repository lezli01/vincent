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

// upgrade swaps the catalog and the fixed keys for one test, the seam task
// 128's upgrade simulation needs: "release N+1" is the shipped tables plus
// what a release adds.
func upgrade(t *testing.T, ops []Info, keys []Fixed, retired map[string]string) {
	t.Helper()
	oldCatalog, oldFixed, oldRetired := catalog, fixed, retiredOps
	catalog = append(append([]Info{}, catalog...), ops...)
	fixed = append(append([]Fixed{}, fixed...), keys...)
	if retired != nil {
		retiredOps = retired
	}
	t.Cleanup(func() { catalog, fixed, retiredOps = oldCatalog, oldFixed, oldRetired })
}

// TestUpgradeAddsADefaultTheUserBound is task 128's acceptance criterion: a
// tui.keys valid on release N still loads on N+1 when N+1 adds an operation
// or a fixed key on a key the user had bound. The lenient build lets the
// user's binding win and warns; the strict one refuses the same map.
func TestUpgradeAddsADefaultTheUserBound(t *testing.T) {
	// Release N: ctrl+e and ctrl+k are free, and the user binds them.
	release := map[string]string{"refresh": "ctrl+e", "filter": "ctrl+k"}
	if _, err := Build(release); err != nil {
		t.Fatalf("release N refuses its own keymap: %v", err)
	}
	const bookmark Op = "bookmark"
	upgrade(t,
		[]Info{{Op: bookmark, Default: "ctrl+e", Meaning: "bookmark the task", Kind: KindTerm, Surfaces: []Surface{"task table"}}},
		[]Fixed{{Surface: "output", Key: "ctrl+k", Meaning: "jump to the next message"}},
		nil)

	if _, err := Build(release); err == nil {
		t.Fatal("strict Build accepted a key an upgrade gave to another meaning")
	}
	km, warnings, err := BuildLenient(release)
	if err != nil {
		t.Fatalf("lenient load refused release N's keymap: %v", err)
	}
	if got := km.Key(Refresh); got != "ctrl+e" {
		t.Errorf("refresh = %q, want the user's ctrl+e", got)
	}
	if got := km.Key(bookmark); got != "" {
		t.Errorf("the new operation = %q, want unbound", got)
	}
	if !km.Shadowed("output", "ctrl+k") {
		t.Error("the new fixed key is not shadowed on its surface")
	}
	if km.Shadowed("output", "f") {
		t.Error("an untouched fixed key is shadowed")
	}
	joined := strings.Join(warnings, "\n")
	for _, w := range []string{"bookmark", "unbound", "ctrl+k", "jump to the next message"} {
		if !strings.Contains(joined, w) {
			t.Errorf("warnings %q do not name %q", joined, w)
		}
	}
	if len(warnings) != 2 {
		t.Errorf("warnings = %q, want one per yielded meaning", warnings)
	}
}

// TestLenientStillRefusesTheUsersOwnClash: two overrides on one key, an
// unknown id, bad syntax and the text-field rules are not upgrade artefacts.
func TestLenientStillRefusesTheUsersOwnClash(t *testing.T) {
	for name, overrides := range map[string]map[string]string{
		"two overrides":      {"refresh": "ctrl+e", "filter": "ctrl+e"},
		"unknown id":         {"reload": "ctrl+r"},
		"fixed name":         {"group": "G"},
		"bad syntax":         {"refresh": "reload"},
		"typing on a letter": {"palette_alt": "P"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := BuildLenient(overrides); err == nil {
				t.Fatalf("%v was accepted on load", overrides)
			}
		})
	}
}

// TestLenientTakesATodayClash: retry on o is refused on write because o is
// browser's; on load the user's binding wins and browser is unbound.
func TestLenientTakesATodayClash(t *testing.T) {
	km, warnings, err := BuildLenient(map[string]string{"retry": "o"})
	if err != nil {
		t.Fatal(err)
	}
	if km.Key(Retry) != "o" || km.Key(Browser) != "" || len(warnings) != 1 {
		t.Errorf("retry=%q browser=%q warnings=%q", km.Key(Retry), km.Key(Browser), warnings)
	}
	// A swap is not a clash: nothing yields and nothing warns.
	if _, warnings, err := BuildLenient(map[string]string{"pause": "x", "reject": "p"}); err != nil || len(warnings) != 0 {
		t.Errorf("a swap: warnings %q, err %v", warnings, err)
	}
}

// TestRetiredIDIsDroppedNotAliased is decision 1: dropped with a warning in
// both modes, never mapped onto another operation, while an unknown id stays
// an error in both.
func TestRetiredIDIsDroppedNotAliased(t *testing.T) {
	upgrade(t, nil, nil, map[string]string{"bookmark": "bookmarks became a filter scope; bind scope instead"})
	overrides := map[string]string{"bookmark": "ctrl+e"}
	km, err := Build(overrides)
	if err != nil {
		t.Fatalf("strict Build refused a retired id: %v", err)
	}
	if len(km.keys) != 0 {
		t.Errorf("a retired id bound something: %v", km.keys)
	}
	km, warnings, err := BuildLenient(overrides)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "bookmark") || !strings.Contains(warnings[0], "bind scope instead") {
		t.Errorf("warnings = %q", warnings)
	}
	for _, info := range Catalog() {
		if km.Key(info.Op) != info.Default {
			t.Errorf("%s moved to %q: a retired id was aliased", info.Op, km.Key(info.Op))
		}
	}
	for _, build := range []func(map[string]string) error{
		func(o map[string]string) error { _, err := Build(o); return err },
		func(o map[string]string) error { _, _, err := BuildLenient(o); return err },
	} {
		if build(map[string]string{"bookmarkz": "ctrl+e"}) == nil {
			t.Error("an unknown id was accepted")
		}
	}
}

// TestOverviewZeroYieldsToAUserBinding is the upgrade task 129.12 is: `0`
// became a fixed key of the task workspace, and a tui.keys that had already
// bound it still loads (task 128 decision 3). The binding wins, the fixed
// meaning is shadowed on every surface that records it, and a warning says
// so; the strict write path refuses the same map.
func TestOverviewZeroYieldsToAUserBinding(t *testing.T) {
	overrides := map[string]string{"refresh": "0"}
	if _, err := Build(overrides); err == nil {
		t.Fatal("strict Build accepted 0, which the Overview holds")
	}
	km, warnings, err := BuildLenient(overrides)
	if err != nil {
		t.Fatalf("lenient load refused a tui.keys binding 0: %v", err)
	}
	if km.Key(Refresh) != "0" {
		t.Errorf("refresh = %q, want the user's 0", km.Key(Refresh))
	}
	for _, s := range []Surface{"task overview", "task workspace"} {
		if !km.Shadowed(s, "0") {
			t.Errorf("0 is not shadowed on %s", s)
		}
	}
	if joined := strings.Join(warnings, "\n"); !strings.Contains(joined, "Overview") {
		t.Errorf("warnings %q do not name the Overview", joined)
	}
}

// Every Overview row the registry lists is recorded, so no override can
// silently take a jump link.
func TestOverviewRowsAreFixed(t *testing.T) {
	want := map[string]bool{"0": false, "3": false, "4": false, "6": false, "7": false}
	for _, f := range FixedKeys() {
		if f.Surface == "task overview" {
			if _, ok := want[f.Key]; ok {
				want[f.Key] = true
			}
		}
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("task overview/%s is not a fixed key", key)
		}
	}
}

// TestUpgradeYieldsNextFailureAndAttentionFilter is task 129.18 under task
// 128's rule: a tui.keys written before N and H had defaults, binding either
// to another operation, still loads. The user's binding wins and the new
// operation is left unbound.
func TestUpgradeYieldsNextFailureAndAttentionFilter(t *testing.T) {
	km, warnings, err := BuildLenient(map[string]string{"retry": "N", "pause": "H"})
	if err != nil {
		t.Fatalf("a keymap binding N and H refused on load: %v", err)
	}
	if km.Key(Retry) != "N" || km.Key(Pause) != "H" {
		t.Errorf("retry=%q pause=%q, want the user's N and H", km.Key(Retry), km.Key(Pause))
	}
	if km.Key(NextFailure) != "" || km.Key(AttentionFilter) != "" {
		t.Errorf("next_failure=%q attention_filter=%q, want both unbound",
			km.Key(NextFailure), km.Key(AttentionFilter))
	}
	joined := strings.Join(warnings, "\n")
	for _, w := range []string{"next_failure", "attention_filter"} {
		if !strings.Contains(joined, w) {
			t.Errorf("warnings %q do not name %q", joined, w)
		}
	}
	if err := Check(km); err != nil {
		t.Errorf("the yielded keymap fails its own check: %v", err)
	}
	// Both are rebindable like any other term.
	moved, err := Build(map[string]string{"next_failure": "ctrl+n", "attention_filter": "ctrl+h"})
	if err != nil {
		t.Fatalf("rebinding both: %v", err)
	}
	if moved.Key(NextFailure) != "ctrl+n" || moved.Key(AttentionFilter) != "ctrl+h" {
		t.Errorf("rebound next_failure=%q attention_filter=%q", moved.Key(NextFailure), moved.Key(AttentionFilter))
	}
}
