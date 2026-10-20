package tui

import (
	"fmt"
	"strings"

	"github.com/lezli01/vincent/internal/keymap"
)

// helpTitle names the surface the overlay is describing, so it is obvious
// the sheet is about where you are.
func helpTitle(ctx bindingContext) string {
	if ctx == "" {
		return "Help"
	}
	return "Help — " + string(ctx)
}

// helpFooter is the overlay's own key row. While help is open the footer
// stops advertising the surface underneath: those keys do nothing until the
// sheet is closed, and offering them is what made two contradictory rows
// (T3.8 finding).
//
// It fits its width (task 135.1): the description is shed first, then the
// scroll keys' names, and the close keys are never cut.
func helpFooter(width int) string {
	closeKeys := styleKey.Render(opKey(keymap.Help)) + styleDim.Render(" close  ") +
		styleKey.Render("esc") + styleDim.Render(" close")
	scroll := styleKey.Render("↑↓ pgup pgdn home end") + styleDim.Render(" scroll  ")
	desc := " " + styleDim.Render("the keys of the surface you were on, plus the global ones")
	for _, c := range [][2]string{
		{desc, scroll + closeKeys},
		{"", scroll + closeKeys},
		{"", styleKey.Render("↑↓") + styleDim.Render(" scroll  ") + closeKeys},
	} {
		if width <= 0 || cols(c[0])+2+cols(c[1]) <= width {
			return padBetween(c[0], c[1], width)
		}
	}
	return padBetween("", closeKeys, width)
}

// helpState is what the sheet knows about the thing under it, beyond its
// binding context: the task the action keys would act on — the same target,
// edit gate and strip the palette is built from (surfaceTarget), so the two
// cannot disagree about what is valid (task 129.17).
type helpState struct {
	// target is the selected task, the open one in the workspace, or the
	// board's marked set. Zero means no task — an empty board, or a daemon
	// the TUI cannot reach — and then no action is valid now.
	target taskActions
	// editable is the palette's gate on edit+retry: a step with text to edit.
	editable bool
	// tabs is the workspace's strip as drawn, nil off the workspace.
	tabs []taskViewTab
}

// helpText is the §15 cheat sheet for one surface, rendered from the binding
// registry — the same source the palette and the footer draw from, so the
// help cannot promise a key the code does not bind. It shows the keys that
// work *here*: an orientation header for the board and the workspace, the
// focused surface's own keys, the global ones, the task actions, and the
// palette's navigation. Printing all eight surfaces' sections at once meant
// reading past seven irrelevant ones (T3.8 finding).
//
// It is a learning sheet, not the palette: where the palette omits a task
// action that cannot happen (§15), help lists the whole vocabulary and
// splits it — what the target offers now, then the rest dimmed under "not
// available now" (task 129.17 decision 2).
func helpText(ctx bindingContext, github bool, st helpState) string {
	return strings.Join(helpLines(ctx, github, st, unwrapped), "\n") + "\n"
}

// unwrapped is a width no help row reaches: helpText lays the sheet out as
// if the terminal were endless, for readers that want its words, not its
// layout.
const unwrapped = 1 << 16

// helpRow is one key and what it does, dimmed when it is not available now.
type helpRow struct {
	key, label string
	dim        bool
}

// helpSection is a titled block of rows.
type helpSection struct {
	title string
	rows  []helpRow
}

// helpLines is the sheet laid out for a body width cells wide (task 135.1):
// every label wraps under its own column rather than running off the frame.
func helpLines(ctx bindingContext, github bool, st helpState, width int) []string {
	var sections []helpSection
	addRows := func(title string, rows []binding, dim bool) {
		if len(rows) == 0 {
			return
		}
		sec := helpSection{title: strings.ToUpper(title)}
		for _, r := range rows {
			key := r.key
			switch {
			case r.unbound:
				// Its key went to a user binding on a lenient load (task 128).
				key = "unbound"
			case key == "":
				// Palette-only navigation: the palette is its key.
				key = opKey(keymap.Palette)
			}
			sec.rows = append(sec.rows, helpRow{key: key, label: r.label, dim: dim})
		}
		sections = append(sections, sec)
	}
	addSection := func(title string, rows []binding) { addRows(title, rows, false) }

	if orient := orientation(ctx, st.tabs); len(orient) > 0 {
		sections = append(sections, helpSection{title: "THIS SCREEN", rows: orient})
	}

	var global, nav, now, later []binding
	for _, r := range withoutGitHub(registry(), github) {
		switch {
		case r.nav:
			nav = append(nav, r)
		case r.scope == scopeGlobal:
			global = append(global, r)
		case r.scope == scopeTaskAction:
			if st.offersNow(r) {
				now = append(now, r)
			} else {
				later = append(later, r)
			}
		}
	}
	// This surface first: it is the answer to "what can I do here".
	if ctx != "" {
		addSection(string(ctx), withoutGitHub(bindingsFor(ctx), github))
	}
	if isHomeContext(ctx) {
		addSection("actions now (on "+st.targetNoun()+")", now)
		addRows("not available now", later, true)
	}
	addSection("global keys", global)
	// The three popups' keys ride along with the panels they pop up over, and
	// they come after the keys that work *now* rather than before them. Each
	// popup prints its own keys inside itself while it owns the keyboard, and
	// while one is open it is the context (task 059), so these rows are here
	// for completeness: the sheet scrolls (task 129.17), but what a short
	// terminal shows first should be what works, not the hypothetical (task
	// 025 — a second popup section is what made the difference visible).
	if isHomeContext(ctx) {
		addSection("answer form (while a task is waiting on you)", bindingsFor(ctxForm))
		addSection("repair form ("+opKey(keymap.Repair)+" on a blocked task)", bindingsFor(ctxRepairForm))
		addSection("follow-up form ("+opKey(keymap.FollowUp)+" on a finished task)", bindingsFor(ctxFollowUpForm))
	}
	addSection("go to (from the "+opKey(keymap.Palette)+" palette)", nav)
	return layoutHelp(sections, width)
}

// helpKeyColMin and helpKeyColMax bound the key column. It is as wide as the
// sheet's widest key needs and no wider than the cap, so one long chord
// cannot push every label across the screen: a key that does not fit puts its
// label on the next line instead.
const (
	helpKeyColMin = 8
	helpKeyColMax = 12
)

// helpIndent is the cells before a row's key.
const helpIndent = 2

// layoutHelp renders the sections at width: titles, then each row's key in
// a column measured in display cells and its label wrapped with a hanging
// indent at the label column. Rows are styled after wrapping.
func layoutHelp(sections []helpSection, width int) []string {
	widest := 0
	for _, sec := range sections {
		for _, r := range sec.rows {
			widest = max(widest, cols(r.key))
		}
	}
	keyCol := min(max(widest+1, helpKeyColMin), helpKeyColMax)
	labelCol := helpIndent + keyCol
	var out []string
	for _, sec := range sections {
		out = append(out, "", " "+styleTitle.Render(sec.title), "")
		for _, r := range sec.rows {
			lead := strings.Repeat(" ", helpIndent) + r.key
			var lines []string
			if cols(r.key) < keyCol {
				lines = wrapText(r.label, width, wrapOpts{prefix: padRight(lead, labelCol), indent: labelCol})
			} else {
				lines = append([]string{lead}, wrapText(r.label, width,
					wrapOpts{prefix: strings.Repeat(" ", labelCol), indent: labelCol})...)
			}
			for _, l := range lines {
				if r.dim {
					// The indent stays outside the style, as it always has.
					text := strings.TrimLeft(l, " ")
					l = l[:len(l)-len(text)] + styleDim.Render(text)
				}
				out = append(out, l)
			}
		}
	}
	return out
}

// offersNow reports whether a task-action row is valid on the target — the
// palette's own test, edit gate included.
func (st helpState) offersNow(r binding) bool {
	if st.target.id == 0 && !st.target.bulk() {
		return false
	}
	if r.op == keymap.EditRetry && !st.editable {
		return false
	}
	return st.target.offers(r.action)
}

// targetNoun names what "actions now" act on, the way the palette titles its
// action group.
func (st helpState) targetNoun() string {
	switch {
	case st.target.bulk():
		return selectedNoun(len(st.target.marked))
	case st.target.id != 0:
		return fmt.Sprintf("#%d", st.target.id)
	default:
		return "the selected task"
	}
}

// orientation is the sheet's "this screen" header (task 129.17): what the
// surface is made of, before any key list. The workspace gets its strip, tab
// by tab, in drawn order; the board gets the three moves that get a newcomer
// anywhere. Every other surface is small enough that its own section already
// leads.
func orientation(ctx bindingContext, tabs []taskViewTab) []helpRow {
	var rows []helpRow
	switch {
	case tabs != nil:
		for _, tab := range tabs {
			rows = append(rows, helpRow{key: taskTabDigits[tab], label: taskTabNames[tab] + " — " + taskTabPurpose[tab]})
		}
	case ctx == ctxTasks:
		rows = []helpRow{
			{key: opKey(keymap.OpenRow), label: "open the task — its workspace, tab by tab"},
			{key: opKey(keymap.NextAttention), label: "jump to the next task that needs you"},
			{key: opKey(keymap.Palette), label: "find everything — every command, beside its key"},
		}
	}
	return rows
}

// padRight pads s to width display cells, or follows it with one space when
// it is already that wide: a key and its label never touch.
func padRight(s string, width int) string {
	if w := cols(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s + " "
}
