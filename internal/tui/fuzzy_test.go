package tui

import (
	"testing"
)

func paletteWith(q string, entries ...paletteEntry) []paletteEntry {
	p := newPalette(entries)
	p.input.SetValue(q)
	return p.matches()
}

// #1080: words in any order, and a typo, still find the row.
func TestPaletteMatchesFuzzily(t *testing.T) {
	rows := []paletteEntry{
		{group: "screens", label: "archived tasks — history, with a permanent delete"},
		{group: "screens", label: "daemon — identity, adapters, log"},
		{group: "screens", label: "settings — everything in config.yaml"},
	}
	for q, want := range map[string]string{
		"tasks archived": "archived tasks — history, with a permanent delete",
		"dameon":         "daemon — identity, adapters, log",
		"setings":        "settings — everything in config.yaml",
	} {
		got := paletteWith(q, rows...)
		if len(got) == 0 || got[0].label != want {
			t.Errorf("%q found %+v, want %q first", q, got, want)
		}
	}
	if got := paletteWith("zzqx", rows...); len(got) != 0 {
		t.Errorf("nonsense matched %+v", got)
	}
}

// #1080: tasks, chats and issues are listed once something is typed, and
// running one opens it.
func TestPaletteFindsTasksByNumberAndTitle(t *testing.T) {
	m := connectedRoot(t)
	s := m.views[viewHome].(*shell)
	b := taskBoardFixture()
	b.client = s.board.client
	s.board = b
	m.openPalette()
	for _, e := range m.palette.matches() {
		if e.item {
			t.Fatalf("an item row is listed before anything is typed: %+v", e)
		}
	}
	m.palette.input.SetValue("#13")
	got := m.palette.matches()
	if len(got) == 0 || got[0].group != "tasks" {
		t.Fatalf("#13 found %+v, want task #13 first", got)
	}
	msg, ok := got[0].action().(selectTaskMsg)
	if !ok || msg.id != 13 {
		t.Fatalf("the row opens %#v, want task 13", got[0].action())
	}
}

func TestEditWithin1(t *testing.T) {
	for _, c := range []struct {
		a, b string
		ok   bool
	}{
		{"daemon", "dameon", true},
		{"daemon", "daemn", true},
		{"daemon", "daemons", true},
		{"daemon", "dxemon", true},
		{"daemon", "demn", false},
		{"tasks", "chats", false},
	} {
		if got := editWithin1(c.a, c.b); got != c.ok {
			t.Errorf("editWithin1(%q, %q) = %v, want %v", c.a, c.b, got, c.ok)
		}
	}
}

// #1158: the key-bindings screen is a palette row, found first by the words
// somebody looking for it types, on every surface, and it opens Settings on
// that screen.
func TestPaletteListsTheKeyBindingsScreen(t *testing.T) {
	contexts := []bindingContext{ctxTasks, ctxTaskOverview, ctxDaemon, ctxSettings, ctxChats, ctxChat, ctxIssues, ctxWorkflows}
	for _, ctx := range contexts {
		for _, q := range []string{"key", "keys", "bind", "keybindings", "shortcuts", "key bindings"} {
			got := paletteWith(q, paletteEntries(ctx, everyAction, true, true, true, nil, nil)...)
			if len(got) == 0 || got[0].label != keyBindingsPaletteLabel {
				first := "nothing"
				if len(got) > 0 {
					first = got[0].label
				}
				t.Errorf("%s: %q found %q first, want the key-bindings screen", ctx, q, first)
			}
		}
	}
	for _, e := range paletteEntries(ctxTasks, everyAction, true, false, true, nil, nil) {
		if e.label != keyBindingsPaletteLabel {
			continue
		}
		msg, ok := e.action().(openConfigKeyMsg)
		if !ok || msg.path != tuiKeysPath {
			t.Fatalf("the row sends %#v, want openConfigKeyMsg for %s", e.action(), tuiKeysPath)
		}
		return
	}
	t.Fatal("the key-bindings row is not listed while disconnected")
}
