package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/keymap"
)

// Task 128. A tui.keys that an upgrade made clash — a new default, a new
// fixed key on a key the user had bound — loads leniently: the user's binding
// wins, the displaced meaning is unbound, and the TUI says so once.

// withLenientKeymap installs overrides through the load path's build, the one
// applyKeys uses, and restores the defaults after.
func withLenientKeymap(t *testing.T, overrides map[string]string) []string {
	t.Helper()
	km, warnings, err := keymap.BuildLenient(overrides)
	if err != nil {
		t.Fatalf("test keymap refused: %v", err)
	}
	setKeymap(km)
	t.Cleanup(func() { setKeymap(keymap.Default()) })
	return warnings
}

// A shadowed fixed key does not fire on its surface: with refresh on g, the
// task table's g no longer regroups — refresh is not answered there, so the
// key does nothing at all.
func TestShadowedFixedKeyDoesNotFire(t *testing.T) {
	m := connectedRoot(t)
	b := m.views[viewHome].(*shell).board
	if ctx := m.activeContext(); ctx != ctxTasks {
		t.Fatalf("fixture is on %s, want the task table", ctx)
	}
	before := b.group
	m.Update(key("g"))
	if b.group.equal(before) {
		t.Fatal("control: g does not regroup under the shipped keymap")
	}

	withLenientKeymap(t, map[string]string{"refresh": "g"})
	before = b.group
	m.Update(key("g"))
	if !b.group.equal(before) {
		t.Errorf("a shadowed g still regrouped the task table (%s → %s)", before.label(), b.group.label())
	}
}

// `?`, the palette and the footer render what a lenient load left without a
// key as unbound rather than advertising a key that does something else now.
func TestUnboundRowsRenderAsUnbound(t *testing.T) {
	withLenientKeymap(t, map[string]string{"refresh": "g", "retry": "o", "chat": "q"})

	var sawGroup, sawBrowser bool
	for i, b := range registry() {
		switch {
		case b.op == keymap.Browser:
			sawBrowser = true
			if !b.unbound || b.key != "" || b.hint != "" {
				t.Errorf("browser row %q: key %q hint %q unbound %v", b.label, b.key, b.hint, b.unbound)
			}
		case b.op == "" && b.context == ctxTasks && bindings[i].key == "g":
			sawGroup = true
			if !b.unbound || b.key != "" {
				t.Errorf("group row %q: key %q unbound %v", b.label, b.key, b.unbound)
			}
		}
	}
	if !sawBrowser || !sawGroup {
		t.Fatalf("fixture rows missing: browser %v, group %v", sawBrowser, sawGroup)
	}
	if h := helpText(ctxTasks, true); !strings.Contains(h, "unbound") {
		t.Errorf("help does not mark the shadowed group row unbound:\n%s", h)
	}
	var unboundEntry bool
	for _, e := range paletteEntries(ctxTasks, taskActions{}, false, true, true, nil) {
		if e.unbound {
			unboundEntry = true
			if e.key != "" {
				t.Errorf("palette entry %q is unbound but keyed %q", e.label, e.key)
			}
		}
	}
	if !unboundEntry {
		t.Error("the palette lists no unbound entry for the shadowed group row")
	}
	for _, s := range footerPinnedSegs(false) {
		if strings.Contains(s.text, "quit") {
			t.Errorf("footer still pins quit on %q, which chat now owns", s.key)
		}
	}
}

// Picking an unbound palette entry does nothing: there is no key to replay.
func TestUnboundPaletteEntryDoesNotRun(t *testing.T) {
	withLenientKeymap(t, map[string]string{"refresh": "g"})
	m := connectedRoot(t)
	b := m.views[viewHome].(*shell).board
	before := b.group
	m.openPalette()
	for _, r := range "group" {
		m.Update(key(string(r)))
	}
	m.Update(enterKey())
	if !b.group.equal(before) {
		t.Error("running the unbound group entry regrouped the table")
	}
}

// The one-time notice: raised by a config fetch whose keymap warns, cleared by
// the next key, and not raised again for the same warnings.
func TestKeymapNoticeIsShownOnce(t *testing.T) {
	t.Cleanup(func() { setKeymap(keymap.Default()) })
	m := connectedRoot(t)
	msg := boardConfigMsg{keys: map[string]string{"refresh": "q"}}
	m.Update(msg)
	if !strings.Contains(m.View().Content, "tui.keys") {
		t.Fatalf("no keymap notice after a warning fetch:\n%s", m.View().Content)
	}
	m.Update(key("down"))
	if strings.Contains(m.View().Content, "tui.keys") {
		t.Error("the notice outlived the next key")
	}
	m.Update(msg)
	if strings.Contains(m.View().Content, "tui.keys") {
		t.Error("the same warnings raised the notice twice")
	}
	if got := opKey(keymap.Quit); got != "" {
		t.Errorf("quit = %q after refresh took q, want unbound", got)
	}
	// A keymap with a hard error keeps the one in force (task 118 decision 9).
	m.Update(boardConfigMsg{keys: map[string]string{"refresh": "ctrl+e", "filter": "ctrl+e"}})
	if got := opKey(keymap.Refresh); got != "q" {
		t.Errorf("a refused keymap replaced the one in force: refresh = %q", got)
	}
}
