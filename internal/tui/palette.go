package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// palette is the §15 command palette: `:` opens a searchable popup listing
// what can be done right now — the selected task's valid actions, navigation
// to the takeover screens, the focused panel's commands, and the global
// keys — each beside its direct key, so it teaches shortcuts rather than
// replacing them. Invalid task actions are omitted, not greyed: an action
// that cannot happen is never on screen.
type palette struct {
	input   textinput.Model
	entries []paletteEntry
	cursor  int
}

// paletteEntry is one runnable line. key is empty for the palette-only
// navigation entries, which deliberately have no shortcut.
type paletteEntry struct {
	group     string
	label     string
	key       string
	nav       bool
	navTarget viewID
}

func newPalette(entries []paletteEntry) *palette {
	in := textinput.New()
	in.Placeholder = "type to search commands"
	in.Prompt = ": "
	in.Focus()
	return &palette{input: in, entries: entries}
}

// paletteEntries builds the palette for one surface. target is the selected
// task as the action bar sees it; editable reports whether the current step
// has text E could edit; connected gates the action group — nothing can act
// on a task the daemon cannot see.
func paletteEntries(ctx bindingContext, target taskActions, editable, connected bool) []paletteEntry {
	out := make([]paletteEntry, 0, len(bindings))
	if connected && target.id != 0 {
		group := fmt.Sprintf("actions on #%d", target.id)
		for _, b := range bindings {
			if b.scope != scopeTaskAction || !target.has(b.action) {
				continue
			}
			if b.key == "E" && !editable {
				continue
			}
			out = append(out, paletteEntry{group: group, label: b.label, key: b.key})
		}
	}
	// Views get their own section: navigating to them is the reason the
	// digits could be retired, so it must not read as one more command
	// (T3.8 finding).
	for _, b := range bindings {
		if b.nav {
			out = append(out, paletteEntry{
				group: "views", label: b.label, key: b.key,
				nav: true, navTarget: b.navTarget,
			})
		}
	}
	for _, b := range bindingsFor(ctx) {
		if b.noPalette {
			continue
		}
		out = append(out, paletteEntry{group: string(ctx), label: b.label, key: b.key})
	}
	for _, b := range bindings {
		if b.scope == scopeGlobal && !b.nav && !b.noPalette {
			out = append(out, paletteEntry{group: "global", label: b.label, key: b.key})
		}
	}
	return out
}

// matches filters entries against the typed query, case-insensitive, over
// label, group and key.
func (p *palette) matches() []paletteEntry {
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	if q == "" {
		return p.entries
	}
	out := make([]paletteEntry, 0, len(p.entries))
	for _, e := range p.entries {
		hay := strings.ToLower(e.label + " " + e.group + " " + e.key)
		if strings.Contains(hay, q) {
			out = append(out, e)
		}
	}
	return out
}

// update handles one key. done reports the palette should close; run is the
// entry to execute when one was chosen.
func (p *palette) update(msg tea.KeyPressMsg) (run *paletteEntry, done bool, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, true, nil
	case "up":
		p.cursor = max(p.cursor-1, 0)
		return nil, false, nil
	case "down":
		p.cursor = min(p.cursor+1, max(len(p.matches())-1, 0))
		return nil, false, nil
	case "enter":
		m := p.matches()
		if len(m) == 0 {
			return nil, true, nil
		}
		e := m[min(p.cursor, len(m)-1)]
		return &e, true, nil
	}
	var c tea.Cmd
	p.input, c = p.input.Update(msg)
	// The match set changed under the cursor; keep it on a row.
	if n := len(p.matches()); p.cursor >= n {
		p.cursor = max(n-1, 0)
	}
	return nil, false, c
}

// render draws the palette box for overlaying: the search line, then the
// matches windowed around the cursor, grouped by section.
func (p *palette) render(w, h int) string {
	inner := max(w-2, 10)
	lines := make([]string, 0, h)
	lines = append(lines, " "+p.input.View())

	m := p.matches()
	if len(m) == 0 {
		lines = append(lines, styleDim.Render("  nothing matches — esc closes"))
		return frame("commands", strings.Join(lines, "\n"), w, h, true)
	}
	cursor := min(p.cursor, len(m)-1)

	rows := make([]string, 0, len(m)*2)
	cursorRow := 0
	group := ""
	for i, e := range m {
		if e.group != group {
			group = e.group
			// A section header, not another dim line: a list where the
			// headings look like the entries reads as one flat wall
			// (T3.8 finding). Blank line above, rule to the right edge.
			if len(rows) > 0 {
				rows = append(rows, "")
			}
			label := " " + strings.ToUpper(group) + " "
			fill := max(inner-ansi.StringWidth(label)-1, 0)
			rows = append(rows, styleTitle.Render(label)+
				styleDim.Render(strings.Repeat("─", fill)))
		}
		mark, style := "  ", styleDim
		if i == cursor {
			mark, style = styleFocus.Render("› "), styleTitle
			cursorRow = len(rows)
		}
		key := e.key
		if key == "" {
			key = "—"
		}
		label := ansi.Truncate(e.label, inner-8, "…")
		pad := max(inner-2-ansi.StringWidth(label)-len(key)-1, 1)
		rows = append(rows, mark+style.Render(label)+strings.Repeat(" ", pad)+styleKey.Render(key))
	}
	lines = append(lines, window(rows, cursorRow, h-3)...)
	return frame("commands", strings.Join(lines, "\n"), w, h, true)
}
