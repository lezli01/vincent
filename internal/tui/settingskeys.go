package tui

import (
	"context"
	"fmt"
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The key-bindings screen (#1079). `tui.keys` was one text row of
// operation=key pairs: no list of operation ids, no press-a-key capture, and
// a clash came back as the daemon's field error. This lists every rebindable
// operation with the key in force and its default, takes a binding by the
// key pressed, names a clash before anything is saved — by the same
// keymap.Build the daemon runs on the PATCH — and resets one operation alone.

const tuiKeysPath = "tui.keys"

// keyBindings is the screen's state, a layer over Settings.
type keyBindings struct {
	ops       []keymap.Info
	overrides map[string]string
	cursor    int
	top       int
	// capturing is the next key press being taken as the cursor's binding.
	capturing bool
	saving    bool
	// sending is the map on its way to the daemon, and pending the change
	// it makes, which becomes Settings' undo slot when it lands (#1158).
	sending map[string]string
	pending *settingsUndo
}

func newKeyBindings(cfg apiclient.Config) *keyBindings {
	b := &keyBindings{ops: keymap.Catalog()}
	b.adopt(cfg)
	return b
}

// adopt takes the overrides in force from a configuration.
func (b *keyBindings) adopt(cfg apiclient.Config) {
	b.overrides = map[string]string{}
	maps.Copy(b.overrides, cfg.TUI.Keys)
}

// current is an operation's key in force and whether it is the default.
func (b *keyBindings) current(info keymap.Info) (string, bool) {
	if k, ok := b.overrides[string(info.Op)]; ok {
		return k, k == info.Default
	}
	return info.Default, true
}

// propose checks one new binding the way the daemon will and, when it holds,
// returns the whole map to save. A key equal to the default drops the
// override rather than writing the default down.
func (b *keyBindings) propose(op keymap.Op, key string) (map[string]string, error) {
	next := maps.Clone(b.overrides)
	if next == nil {
		next = map[string]string{}
	}
	info, _ := keymap.Lookup(op)
	if key == "" || key == info.Default {
		delete(next, string(op))
	} else {
		next[string(op)] = key
	}
	if _, err := keymap.Build(next); err != nil {
		return nil, err
	}
	return next, nil
}

// saveKeys sends the whole map, as the tui.keys row's own save does. op is
// the operation the save changes, remembered so `u` can put back its key
// alone (#1158) rather than the whole map, which may have moved since.
func (v *settingsView) saveKeys(next map[string]string, op keymap.Op) tea.Cmd {
	b := v.bindings
	if v.client == nil {
		v.undoing = false
		v.status().result("the daemon is not reachable", true)
		return nil
	}
	b.pending = &settingsUndo{op: op, opWas: b.overrides[string(op)]}
	if k, ok := v.key(tuiKeysPath); ok {
		b.pending.key, b.pending.was = k, k.read(v.config)
	}
	b.sending = next
	b.saving = true
	v.status().result("saving…", false)
	client := v.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		cfg, err := client.PatchConfig(ctx, apiclient.ConfigPatch{TUI: &apiclient.ConfigTUIPatch{Keys: &next}})
		return configSavedMsg{path: tuiKeysPath, cfg: cfg, err: err}
	}
}

// applyKeysSaved lands a save made from this screen.
func (v *settingsView) applyKeysSaved(msg configSavedMsg) {
	b := v.bindings
	pending := b.pending
	b.saving, b.sending, b.pending = false, nil, nil
	if msg.err != nil {
		v.undoing = false // #1158: an undo that failed is not pending
		v.status().result(errString(msg.err), true)
		return
	}
	v.config, v.configOK, v.configErr = msg.cfg, true, nil
	b.adopt(msg.cfg)
	note := "saved — the new key works now"
	defer func() { v.status().result(note, false) }()
	if pending == nil {
		return
	}
	info, _ := keymap.Lookup(pending.op)
	if v.undoing {
		// Named, as Settings names what it puts back (#1137), and not
		// itself undoable.
		v.undoing, v.undo = false, nil
		now, _ := b.current(info)
		note += " · " + string(pending.op) + " put back to " + now
		return
	}
	// #1158: backspace reset a binding with nothing to take it back. What
	// it was, and the key that puts it back — Settings' one undo slot.
	v.undo = pending
	note += " · was " + valueOr(pending.opWas, info.Default) + " · u puts it back"
}

// undoBinding is `u` for a change made on this screen: the operation's own
// key put back, checked as any binding is, with the cursor on it so the
// write is made on screen (#1158, as #1137 does for a setting).
func (v *settingsView) undoBinding(u *settingsUndo) tea.Cmd {
	if v.bindings == nil {
		v.goTo(tuiKeysPath)
		v.form = nil
		v.bindings = newKeyBindings(v.config)
	}
	b := v.bindings
	for i, info := range b.ops {
		if info.Op == u.op {
			b.cursor = i
		}
	}
	next, err := b.propose(u.op, u.opWas)
	if err != nil {
		v.status().result("not put back — "+err.Error(), true)
		return nil
	}
	v.undoing = true
	return v.saveKeys(next, u.op)
}

// updateBindings is the screen's keyboard.
func (v *settingsView) updateBindings(msg tea.KeyPressMsg) tea.Cmd {
	b := v.bindings
	if b.saving {
		return nil
	}
	if b.capturing {
		b.capturing = false
		k := msg.String()
		if k == "esc" {
			v.status().note("nothing changed", false)
			return nil
		}
		info := b.ops[b.cursor]
		key, err := keymap.ParseKey(k)
		if err != nil {
			v.status().result(fmt.Sprintf("%s cannot be bound: %v", k, err), true)
			return nil
		}
		if cur, _ := b.current(info); cur == key {
			v.status().note(string(info.Op)+" is already "+key, false)
			return nil
		}
		next, err := b.propose(info.Op, key)
		if err != nil {
			// The clash is named here, before anything is written.
			v.status().result("not saved — "+err.Error(), true)
			return nil
		}
		return v.saveKeys(next, info.Op)
	}
	switch msg.String() {
	case "esc":
		v.bindings = nil
	case "up", "k":
		b.cursor = max(b.cursor-1, 0)
	case "down", "j":
		b.cursor = min(b.cursor+1, len(b.ops)-1)
	case "pgup":
		b.cursor = max(b.cursor-10, 0)
	case "pgdown":
		b.cursor = min(b.cursor+10, len(b.ops)-1)
	case "home", "g":
		b.cursor = 0
	case "end", "G":
		b.cursor = len(b.ops) - 1
	case "enter":
		// A question that owns the keyboard, drawn by this screen rather
		// than posted to the status line (#1164).
		b.capturing = true
	case opKey(keymap.FreeText):
		// Every pair as one line of text, to paste a whole keymap; the
		// daemon checks it on save as it always did.
		if k, ok := v.key(tuiKeysPath); ok {
			v.bindings = nil
			v.form = newConfigForm(k, v.config)
		}
	case "backspace":
		info := b.ops[b.cursor]
		if _, isDefault := b.current(info); isDefault {
			v.status().note(string(info.Op)+" is already on its default, "+info.Default, false)
			return nil
		}
		next, err := b.propose(info.Op, "")
		if err != nil {
			v.status().result("not reset — "+err.Error(), true)
			return nil
		}
		return v.saveKeys(next, info.Op)
	case "u":
		if v.undo == nil {
			v.status().note("nothing to undo", false)
			return nil
		}
		return v.undoLast()
	}
	return nil
}

// kindHeading names an operation's kind as the guide's table does.
func kindHeading(k keymap.Kind) string {
	switch k {
	case keymap.KindAction:
		return "task actions"
	case keymap.KindGlobal:
		return "global keys"
	}
	return "keys screens share"
}

// renderBindings draws the screen in width×height.
func (v *settingsView) renderBindings(width, height int) string {
	b := v.bindings
	head := []string{
		styleTitle.Render("  Key bindings") + styleDim.Render(fmt.Sprintf("  ·  %d operations, %d changed", len(b.ops), len(b.overrides))),
		styleDim.Render("  enter set by pressing the key · backspace back to the default · u undo · " + opKey(keymap.FreeText) + " type them all as text · esc back to Settings"),
	}
	note := ""
	if b.capturing {
		note = styleAsk.Render("  press the new key for " + string(b.ops[b.cursor].Op) + " — esc keeps it")
	}
	room := max(height-len(head)-3, 3)
	// Every line first, headings included, then a window around the
	// cursor's line: counting operations alone let the headings push the
	// cursor's own row off the bottom.
	var lines []string
	at := 0
	lastKind := keymap.Kind(-1)
	for i, info := range b.ops {
		if info.Kind != lastKind {
			lastKind = info.Kind
			lines = append(lines, styleDim.Render("  "+strings.ToUpper(kindHeading(info.Kind))))
		}
		key, isDefault := b.current(info)
		keyCell := styleKey.Render(fmt.Sprintf("%-14s", key))
		def := ""
		if !isDefault {
			keyCell = styleAsk.Render(fmt.Sprintf("%-14s", key))
			def = styleDim.Render("  default " + info.Default)
		}
		marker := "  "
		if i == b.cursor {
			at = len(lines)
			marker = styleFocus.Render("▸ ")
			if b.capturing {
				keyCell = styleAsk.Render(fmt.Sprintf("%-14s", "press a key…"))
			}
		}
		line := marker + fmt.Sprintf("%-18s", string(info.Op)) + " " + keyCell + " " + info.Meaning + def
		lines = append(lines, ansi.Truncate(line, max(width, 1), "…"))
	}
	if at < b.top {
		b.top = at
	}
	if at >= b.top+room {
		b.top = at - room + 1
	}
	b.top = min(b.top, max(len(lines)-room, 0))
	rows := lines[b.top:min(b.top+room, len(lines))]
	out := make([]string, 0, len(head)+len(rows)+3)
	out = append(out, head...)
	out = append(out, "")
	out = append(out, rows...)
	if note != "" {
		out = append(out, "", ansi.Truncate(note, max(width, 1), "…"))
	}
	return clampLines(out, height)
}
