package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The reverse of TestEveryPanelKeyIsHandled (#1165). That walk proves every
// key the registry promises reaches a view that answers it; this one proves
// every key a view answers is one the registry promises — `z` and ctrl+y on
// the archived board, alt+↑/↓ in the chat diff and j/k/g/G on the key
// bindings screen had all been handled for releases without `?`, the palette
// or the footer ever naming them.
//
// It cannot read a key switch, so it presses: each surface below is built
// fresh for every key in keyUniverse, and a key that returned a command or
// changed what the surface draws (or its status line) was handled. Such a key
// must then be a row of the surface's context, a global row, or an alias a
// row's label names (keyAliases). A fixture is one state of one surface, so
// a key that only acts in another state goes unseen; that is the price of
// pressing rather than parsing, and a surface whose keys depend on its state
// gets one fixture per state (the archived board's narrow table and wide
// cards).

// keySurface is one view in one state, as the reverse walk drives it.
type keySurface struct {
	press func(tea.KeyPressMsg) tea.Cmd
	snap  func() string
}

// reverseFixture builds a keySurface for one context.
type reverseFixture struct {
	name string
	ctx  bindingContext
	// actions marks a surface that routes the §6 action keys, whose rows
	// are scopeTaskAction rather than the context's own.
	actions bool
	build   func(t *testing.T) keySurface
}

// shownResult is the status line as a key's effect: a note is left out,
// because a note is how a key that means nothing here says so (#1138) — `a`
// on an archived task explains that it offers no approve, and that is not a
// meaning the help owes a row.
func shownResult(h *statusHolder) string {
	if h.kind == statusNote {
		return ""
	}
	return h.text
}

func boardSurface(b *board, w, h int) keySurface {
	return keySurface{
		press: func(msg tea.KeyPressMsg) tea.Cmd { _, cmd := b.updateKey(msg); return cmd },
		snap:  func() string { return b.render(w, h) + "\x00" + shownResult(b.status()) },
	}
}

func chatsSurface(v *chatsView) keySurface {
	return keySurface{
		press: func(msg tea.KeyPressMsg) tea.Cmd { _, cmd := v.updateKey(msg); return cmd },
		snap:  func() string { return v.render(100, 30) + "\x00" + shownResult(v.status()) },
	}
}

var reverseFixtures = []reverseFixture{
	{name: "task board", ctx: ctxTasks, actions: true, build: func(*testing.T) keySurface {
		return boardSurface(taskBoardFixture(), 160, 40)
	}},
	{name: "archived tasks, narrow", ctx: ctxArchived, actions: true, build: func(*testing.T) keySurface {
		return boardSurface(testArchivedBoard(), 100, 30)
	}},
	{name: "archived tasks, wide", ctx: ctxArchived, actions: true, build: func(*testing.T) keySurface {
		b := testArchivedBoard()
		b.route(tea.WindowSizeMsg{Width: 160, Height: 40})
		return boardSurface(b, 160, 40)
	}},
	{name: "archived chats", ctx: ctxArchivedChats, build: func(*testing.T) keySurface {
		return chatsSurface(archivedChatsFixture())
	}},
	{name: "chats board", ctx: ctxChats, build: func(*testing.T) keySurface {
		return chatsSurface(chatsFixture())
	}},
	{name: "chat diff", ctx: ctxChatDiff, build: func(t *testing.T) keySurface {
		v := chatDiffFixture(t)
		// Short enough that the open file scrolls, so a line scroll shows.
		v.updateKey(synthKey("enter"))
		return keySurface{
			press: func(msg tea.KeyPressMsg) tea.Cmd { _, cmd := v.updateKey(msg); return cmd },
			snap:  func() string { return v.render(100, 12) + "\x00" + shownResult(v.status()) },
		}
	}},
	{name: "key bindings", ctx: ctxKeyBindings, build: func(t *testing.T) keySurface {
		v := bindingsFixture(t)
		// Off the top, so a jump to the first row shows as well as one to the last.
		v.updateKey(synthKey("down"))
		v.updateKey(synthKey("down"))
		return keySurface{
			press: func(msg tea.KeyPressMsg) tea.Cmd { return v.updateKey(msg) },
			snap:  func() string { return v.render(180, 44) + "\x00" + shownResult(v.status()) },
		}
	}},
}

// keyAliases are keys a context answers that are not rows of their own: a
// second spelling of a row's key, named in that row's label (`j` in "↑/↓ or
// j/k"). The walk holds each one to its canonical row and to that label, so
// an alias the help does not mention fails here as surely as a missing row.
var keyAliases = map[bindingContext]map[string]string{
	ctxTasks: {
		"up": "down", "j": "down", "k": "down",
		"pgup": "pgdown", "home": "pgdown", "end": "pgdown", "g": "pgdown", "G": "pgdown",
	},
	ctxArchived: {
		"up": "down", "j": "down", "k": "down",
		"pgup": "pgdown", "home": "pgdown", "end": "pgdown", "G": "pgdown",
		"ctrl+d": "pgdown", "ctrl+u": "pgdown",
		"left": "right",
		"V":    "space",
		"<":    ">",
	},
	ctxArchivedChats: {
		"up": "down", "j": "down", "k": "down",
		"pgup": "pgdown", "home": "pgdown", "end": "pgdown",
		"<": ">",
	},
	ctxChats: {
		"up": "down", "j": "down", "k": "down",
		"pgup": "pgdown", "home": "pgdown", "end": "pgdown",
		"[": "]",
	},
	ctxChatDiff: {
		"up": "down", "j": "down", "k": "down",
		"[":     "]",
		"space": "enter", "right": "enter", "left": "enter", "l": "enter", "h": "enter",
		"pgup": "pgdown", "ctrl+d": "pgdown", "ctrl+u": "pgdown",
		"f": "pgdown", "b": "pgdown", "d": "pgdown", "u": "pgdown",
		"shift+left": "shift+right",
		"alt+up":     "alt+down",
	},
	ctxKeyBindings: {
		"up": "down", "j": "down", "k": "down", "pgup": "down", "pgdown": "down",
		"home": "g", "end": "g", "G": "g",
	},
}

// keyUniverse is every press the walk tries: the printable ASCII keys, ctrl
// and alt over the letters, and the named keys and their alt and shift forms.
func keyUniverse() []string {
	var keys []string
	for r := '!'; r <= '~'; r++ {
		keys = append(keys, string(r))
	}
	for r := 'a'; r <= 'z'; r++ {
		keys = append(keys, "ctrl+"+string(r), "alt+"+string(r))
	}
	for _, k := range []string{"up", "down", "left", "right", "home", "end", "pgup", "pgdown"} {
		keys = append(keys, k, "alt+"+k, "shift+"+k, "ctrl+"+k)
	}
	return append(keys, "enter", "esc", "tab", "shift+tab", "space", "backspace", "delete")
}

// labelNames reports whether a row's label names key, as a whole word of the
// label: `j` is named by "(↑/↓ or j/k)" and not by "jump".
func labelNames(label, key string) bool {
	glyph := strings.NewReplacer("up", "↑", "down", "↓", "left", "←", "right", "→").Replace(key)
	words := strings.FieldsFunc(label, func(r rune) bool {
		return r == ' ' || r == '/' || r == ',' || r == ';' || r == '(' || r == ')' || r == ':'
	})
	return slices.Contains(words, key) || slices.Contains(words, glyph)
}

func TestEveryHandledKeyIsRegistered(t *testing.T) {
	globals, actions := map[string]bool{}, map[string]bool{}
	for _, b := range registry() {
		switch {
		case b.key == "":
		case b.scope == scopeGlobal:
			globals[b.key] = true
		case b.scope == scopeTaskAction:
			actions[b.key] = true
		}
	}
	for _, f := range reverseFixtures {
		t.Run(f.name, func(t *testing.T) {
			rows := map[string]binding{}
			for _, b := range bindingsFor(f.ctx) {
				if b.key != "" && !b.echo {
					rows[b.key] = b
				}
			}
			for alias, canon := range keyAliases[f.ctx] {
				row, ok := rows[canon]
				switch {
				case !ok:
					t.Errorf("alias %q names %q, which is not a %s row", alias, canon, f.ctx)
				case !labelNames(row.label, alias):
					t.Errorf("alias %q: the %q row's label does not name it: %q", alias, canon, row.label)
				}
			}
			s := f.build(t)
			if a, b := s.snap(), s.snap(); a != b {
				t.Fatal("the surface draws differently twice in a row; the walk cannot tell a key's effect from that")
			}
			for _, key := range keyUniverse() {
				msg := synthKey(key)
				s := f.build(t)
				before := s.snap()
				cmd := s.press(msg)
				if cmd == nil && s.snap() == before {
					continue
				}
				pressed := msg.String()
				if _, ok := rows[pressed]; ok {
					continue
				}
				if _, ok := keyAliases[f.ctx][pressed]; ok {
					continue
				}
				if globals[pressed] || (f.actions && actions[pressed]) {
					continue
				}
				t.Errorf("%s answers %q, and no %s row names it — register it, or name it in a row's label and keyAliases",
					f.name, pressed, f.ctx)
			}
		})
	}
}

// wideArchivedBoard is the archive from 130 columns: cards, not a table.
func wideArchivedBoard() *board {
	b := testArchivedBoard()
	b.route(tea.WindowSizeMsg{Width: 160, Height: 40})
	b.render(160, 40)
	return b
}

// pressChanges reports whether key changed what s draws.
func pressChanges(t *testing.T, s keySurface, key string) bool {
	t.Helper()
	before := s.snap()
	s.press(registryKey(t, key))
	return s.snap() != before
}

// The forward probes for the rows #1165 registered.
func init() {
	archived := panelKeyProbes[ctxArchived]
	archived["down"] = func(t *testing.T) {
		b := testArchivedBoard()
		b.render(100, 30)
		b.updateKey(registryKey(t, "down"))
		if b.tbl.Cursor() != 1 {
			t.Fatalf("down left the cursor on row %d", b.tbl.Cursor())
		}
	}
	archived["pgdown"] = func(t *testing.T) {
		b := testArchivedBoard()
		b.render(100, 30)
		b.updateKey(registryKey(t, "pgdown"))
		if b.tbl.Cursor() == 0 {
			t.Fatal("pgdown did not move the cursor")
		}
	}
	archived["g"] = func(t *testing.T) {
		b := testArchivedBoard()
		b.updateKey(registryKey(t, "g"))
		if !b.groupPinned {
			t.Fatal("g did not cycle the narrow list's grouping")
		}
		if !pressChanges(t, boardSurface(wideArchivedBoard(), 160, 40), "end") ||
			!func() bool {
				w := wideArchivedBoard()
				w.updateKey(synthKey("end"))
				return pressChanges(t, boardSurface(w, 160, 40), "g")
			}() {
			t.Fatal("g did not jump to the first card on the wide archive")
		}
	}
	archived["right"] = func(t *testing.T) {
		if !pressChanges(t, boardSurface(wideArchivedBoard(), 160, 40), "right") {
			t.Fatal("right did not move across the wide archive's row")
		}
	}
	archived["f"] = func(t *testing.T) {
		if !pressChanges(t, boardSurface(wideArchivedBoard(), 160, 40), "f") {
			t.Fatal("f did not fold the issue group on the wide archive")
		}
	}
	archived["C"] = func(t *testing.T) {
		if !pressChanges(t, boardSurface(wideArchivedBoard(), 160, 40), "C") {
			t.Fatal("C did not fold every group on the wide archive")
		}
	}
	archived["O"] = func(t *testing.T) {
		b := wideArchivedBoard()
		b.updateKey(synthKey("C"))
		if !pressChanges(t, boardSurface(b, 160, 40), "O") {
			t.Fatal("O did not open the groups C folded")
		}
	}
	archived[boardSortKey] = func(t *testing.T) {
		b := testArchivedBoard()
		b.updateKey(registryKey(t, boardSortKey))
		if b.sortOrder == "" {
			t.Fatal("z did not change the archive's order")
		}
	}
	archived[copyPickKey] = func(t *testing.T) {
		b := testArchivedBoard()
		b.render(100, 30)
		_, cmd := b.updateKey(registryKey(t, copyPickKey))
		if _, ok := drain(cmd).(openCopyPickerMsg); !ok {
			t.Fatalf("ctrl+y produced %#v, want the copy picker", drain(cmd))
		}
	}

	for ctx, fixture := range map[bindingContext]func() *chatsView{
		ctxArchivedChats: archivedChatsFixture,
		ctxChats:         chatsFixture,
	} {
		probes := panelKeyProbes[ctx]
		probes["down"] = func(t *testing.T) {
			v := fixture()
			before, _ := v.current()
			v.updateKey(registryKey(t, "down"))
			if after, _ := v.current(); after.ID == before.ID {
				t.Fatalf("down left the cursor on chat %d", after.ID)
			}
		}
		probes["pgdown"] = func(t *testing.T) {
			v := fixture()
			before, _ := v.current()
			v.updateKey(registryKey(t, "pgdown"))
			if after, _ := v.current(); after.ID == before.ID {
				t.Fatalf("pgdown left the cursor on chat %d", after.ID)
			}
		}
	}

	panelKeyProbes[ctxChatDiff]["alt+down"] = func(t *testing.T) {
		v := chatDiffFixture(t)
		v.updateKey(synthKey("enter"))
		v.render(100, 12)
		v.updateKey(registryKey(t, "alt+down"))
		if v.diff.pane.vp.YOffset() != 1 {
			t.Fatalf("alt+down left the chat diff at line %d", v.diff.pane.vp.YOffset())
		}
	}
	panelKeyProbes[ctxChatDiff]["alt+d"] = func(t *testing.T) {
		v := chatDiffFixture(t)
		v.updateKey(registryKey(t, "alt+d"))
		if v.diff.open {
			t.Fatal("the chat_diff key did not close the diff it opened")
		}
	}
}
