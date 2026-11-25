package tui

import (
	"strings"
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

// #1159: a short query is answered by rows that say it — a word starting
// with it, or the query inside a word — never by rows whose letters merely
// turn up in order across a long label (`:key` had preselected "new task",
// `:bind` "Search all chats").
func TestPaletteShortQueriesPreferWords(t *testing.T) {
	contexts := []bindingContext{ctxTasks, ctxTaskOverview, ctxChat, ctxIssues, ctxSettings, ctxDaemon}
	for _, ctx := range contexts {
		rows := paletteEntries(ctx, everyAction, true, true, true, nil, nil)
		for _, q := range []string{"key", "bind", "arch", "set", "pr", "diff", "log", "sync", "mrg"} {
			got := paletteWith(q, rows...)
			wordStart := false
			for _, e := range rows {
				if hasWordStarting(paletteHay(e), q) {
					wordStart = true
				}
			}
			for _, e := range got {
				if !strings.Contains(paletteHay(e), q) {
					t.Errorf("%s: %q lists %q, which only scatters its letters", ctx, q, e.label)
				}
			}
			if wordStart && (len(got) == 0 || !hasWordStarting(paletteHay(got[0]), q)) {
				t.Errorf("%s: %q puts %+v first, want a row with a word starting %q", ctx, q, got, q)
			}
		}
		for q, want := range map[string]string{
			"key":  keyBindingsPaletteLabel,
			"bind": keyBindingsPaletteLabel,
			"pr":   "pull requests — what is open in this project on GitHub",
			"log":  "daemon — identity, adapters, log",
		} {
			if got := paletteWith(q, rows...); len(got) == 0 || got[0].label != want {
				t.Errorf("%s: %q found %+v first, want %q", ctx, q, got, want)
			}
		}
	}
}

// #1159: letters in order still find a long word, but rank below every row
// that holds the words themselves.
func TestPaletteScatteredLettersRankLast(t *testing.T) {
	rows := []paletteEntry{
		{group: "screens", label: "workflows — every workflow the project can run"},
		{group: "screens", label: "work items — wfl"},
	}
	got := paletteWith("wrkflw", rows...)
	if len(got) != 1 || got[0].label != rows[0].label {
		t.Fatalf("wrkflw found %+v, want only the workflows row", got)
	}
	// A short query scatters nowhere: "dmn" is in "daemon" in order, but
	// three letters turn up in order in most long rows.
	if got := paletteWith("dmn", rows[0], paletteEntry{group: "screens", label: "daemon — identity"}); len(got) != 0 {
		t.Errorf("dmn found %+v, want nothing", got)
	}
	if s, scattered := fuzzyScore("daemon reload", []string{"reload", "dmn"}); s != 0 || scattered {
		t.Errorf("a short word no row holds still matched: %d %v", s, scattered)
	}
	a, as := fuzzyScore("workflows list", []string{"list", "wrkflw"})
	b, bs := fuzzyScore("workflows list", []string{"list", "workflows"})
	if !as || bs || a == 0 || b == 0 {
		t.Errorf("scattered flags: %d %v, %d %v", a, as, b, bs)
	}
}

func paletteHay(e paletteEntry) string {
	return strings.ToLower(e.label + " " + e.group + " " + e.key + " " + e.keywords)
}

func hasWordStarting(hay, q string) bool {
	for _, w := range strings.FieldsFunc(hay, func(r rune) bool { return !wordRune(r) }) {
		if strings.HasPrefix(w, q) {
			return true
		}
	}
	return false
}
