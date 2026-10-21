package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/keymap"
)

// palette is the §15 command palette: `:` opens a searchable popup listing
// what can be done right now — the selected task's valid actions, navigation
// to the takeover screens, the focused panel's commands, and the global
// keys — each beside its direct key, so it teaches shortcuts rather than
// replacing them. Invalid task actions are omitted, not greyed: an action
// that cannot happen is never on screen. (The help sheet does grey them: it
// teaches the vocabulary and runs nothing — task 129.17 decision 2.) In the
// task workspace it also lists the strip's tabs by digit.
type palette struct {
	input   textField
	entries []paletteEntry
	cursor  int
	// page is the last render's layout, which pgup and pgdown move
	// through (task 135.10 decision 5).
	page popupPage
}

// paletteEntry is one runnable line. key is empty for the palette-only
// navigation entries, which deliberately have no shortcut. global marks a
// scopeGlobal row, whose key the root runs itself even where a text field has
// the keyboard (task 114 decision 3).
type paletteEntry struct {
	group     string
	label     string
	key       string
	nav       bool
	navTarget viewID
	global    bool
	// unbound is a row whose key a user binding took on a lenient load
	// (task 128): listed, so the human can see it is gone, but not runnable.
	unbound bool
	// tab marks a "go to the <Name> tab" row (task 129.17); key is the tab's
	// digit, which running the row replays like any panel key.
	tab   taskViewTab
	isTab bool
	// action is a key-less row's own command (task 130.13): a row with no
	// shortcut to replay, run directly. "open this task's issue" is one.
	action tea.Cmd
}

func newPalette(entries []paletteEntry) *palette {
	in := newTextField()
	in.SetPlaceholder("type to search commands")
	in.SetPrompt(": ")
	in.Focus()
	return &palette{input: in, entries: entries}
}

// paletteEntries builds the palette for one surface. target is the selected
// task as the action bar sees it; editable reports whether the current step
// has text E could edit; connected gates the action group — nothing can act
// on a task the daemon cannot see; github gates the rows that only mean
// something when some project's integration is usable (task 052.6); live,
// when non-nil, drops the surface's panel rows whose keys are inert in its
// current state — the same gate the footer applies, so the palette never
// offers a press the footer knows does nothing (issue #372); tabs, when non-nil, is the task workspace's
// strip, one "go to" row per tab on it (task 129.17).
func paletteEntries(ctx bindingContext, target taskActions, editable, connected, github bool, live func([]binding) []binding, tabs []taskViewTab) []paletteEntry {
	out := make([]paletteEntry, 0, len(registry()))
	if connected && (target.id != 0 || target.bulk()) {
		// A selection is what the keys act on, so it is what the section is
		// titled after (task 011) — running `archive` from here with nine rows
		// marked must not read as archiving the one row named above the list.
		group := fmt.Sprintf("actions on #%d", target.id)
		if target.bulk() {
			group = "actions on " + selectedNoun(len(target.marked))
		}
		for _, b := range registry() {
			if b.scope != scopeTaskAction || !target.offers(b.action) {
				continue
			}
			if b.op == keymap.EditRetry && !editable {
				continue
			}
			out = append(out, paletteEntry{group: group, label: b.label, key: b.key, unbound: b.unbound})
		}
	}
	// The workspace's tabs, in drawn order, the current one included — it is
	// a harmless no-op, and a list that moved under the cursor would not be.
	// Only the tabs: Overview's jump links stay on Overview, where the attempt
	// they move the cursor to is on screen (task 129.17 decision 3).
	for _, tab := range tabs {
		out = append(out, paletteEntry{
			group: "tabs", label: "go to the " + taskTabNames[tab] + " tab",
			key: taskTabDigits[tab], tab: tab, isTab: true,
		})
	}
	// Screens get their own section: navigating to them is the reason the
	// digits could be retired, so it must not read as one more command
	// (T3.8 finding). "screens", not "views": a screen is a routed
	// takeover, and a pane of the task workspace is a tab (task 129.7).
	for _, b := range withoutGitHub(registry(), github) {
		if b.nav {
			out = append(out, paletteEntry{
				group: "screens", label: b.label, key: b.key,
				nav: true, navTarget: b.navTarget, global: true,
			})
		}
	}
	rows := withoutGitHub(bindingsFor(ctx), github)
	if live != nil {
		rows = live(rows)
	}
	for _, b := range rows {
		if b.noPalette {
			continue
		}
		out = append(out, paletteEntry{group: string(ctx), label: b.label, key: b.key, unbound: b.unbound})
	}
	for _, b := range registry() {
		if b.scope == scopeGlobal && !b.nav && !b.noPalette {
			e := paletteEntry{group: "global", label: b.label, key: b.key, global: true, unbound: b.unbound}
			if b.op == keymap.Project {
				// The picker must stay reachable when a user binding took
				// `@` (task 132.4): run it directly rather than replaying a
				// key that may be unbound.
				e.action = func() tea.Msg { return openProjectPickerMsg{} }
			}
			out = append(out, e)
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
	case "pgup", "pgdown", "home", "end":
		p.cursor = popupPageKey(msg.String(), p.cursor, len(p.matches()), p.page)
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

// paste types text into the search line — the palette is a text field like
// any other, and pasting a workflow or task name into it is the reason to
// have one.
func (p *palette) paste(text string) tea.Cmd {
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(tea.PasteMsg{Content: text})
	if n := len(p.matches()); p.cursor >= n {
		p.cursor = max(n-1, 0)
	}
	return cmd
}

// paletteReserve is the lines under the list that carry the focused entry's
// whole label (task 135.10 decision 3). Fixed, so the list keeps its height
// whichever entry the cursor is on.
const paletteReserve = 2

// render draws the palette box for overlaying: the search line, then the
// matches windowed around the cursor, grouped by section, and under them the
// focused entry's whole label — a row cuts a long label with "…", and the
// reserve is where it is read in full.
func (p *palette) render(w, h int) string {
	inner := max(w-2, 10)
	lines := make([]string, 0, h)
	p.input.SetWidth(max(inner-1, 1))
	lines = append(lines, fieldRows(" ", p.input)...)

	m := p.matches()
	if len(m) == 0 {
		lines = append(lines, styleDim.Render("  nothing matches — esc closes"))
		return frame("commands", strings.Join(lines, "\n"), w, h, true)
	}
	cursor := min(p.cursor, len(m)-1)

	rows := make([]string, 0, len(m)*2)
	entryRow := make([]int, 0, len(m))
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
		entryRow = append(entryRow, len(rows))
		mark, style := "  ", styleDim
		if i == cursor {
			mark, style = styleFocus.Render("› "), styleTitle
			cursorRow = len(rows)
		}
		rows = append(rows, mark+paletteRow(e, style, inner-2))
	}
	list := h - 2 - len(lines)
	// The reserve and the rule above it, while the list keeps a few rows
	// of its own; a box too short for both keeps the list.
	reserve := list-paletteReserve-1 >= 3
	if reserve {
		list -= paletteReserve + 1
	}
	shown, page := popupWindow(rows, entryRow, cursorRow, list)
	p.page = page
	lines = append(lines, shown...)
	if reserve {
		full := wrapText(m[cursor].label, inner-1, wrapOpts{prefix: " ", indent: 1, maxLines: paletteReserve})
		lines = append(lines, styleDim.Render(strings.Repeat("─", inner)))
		for i := range paletteReserve {
			line := ""
			if i < len(full) {
				line = full[i]
			}
			lines = append(lines, styleTitle.Render(line))
		}
	}
	return frame("commands", strings.Join(lines, "\n"), w, h, true)
}

// paletteRow is one entry after the cursor mark: the label, then its key at
// the right edge. The budget is measured in cells (task 135.10 decision 4):
// the key is never cut, and when it leaves the label no room the label
// shrinks to "…" first.
func paletteRow(e paletteEntry, style lipgloss.Style, width int) string {
	key := e.key
	switch {
	case e.unbound:
		key = "unbound"
	case key == "":
		key = "—"
	}
	kw := ansi.StringWidth(key)
	// One cell between label and key, and one kept clear at the edge.
	label := ansi.Truncate(e.label, max(width-kw-2, 1), "…")
	pad := max(width-ansi.StringWidth(label)-kw-1, 1)
	return style.Render(label) + strings.Repeat(" ", pad) + styleKey.Render(key)
}
