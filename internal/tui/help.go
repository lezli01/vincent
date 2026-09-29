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
func helpFooter(width int) string {
	pinned := styleKey.Render("↑↓ pgup pgdn home end") + styleDim.Render(" scroll  ") +
		styleKey.Render(opKey(keymap.Help)) + styleDim.Render(" close  ") +
		styleKey.Render("esc") + styleDim.Render(" close")
	line := " " + styleDim.Render("the keys of the surface you were on, plus the global ones")
	return padBetween(line, pinned, width)
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
	var b strings.Builder
	writeRows := func(title string, rows []binding, dim bool) {
		if len(rows) == 0 {
			return
		}
		b.WriteString("\n " + styleTitle.Render(strings.ToUpper(title)) + "\n\n")
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
			line := padRight(key, 8) + r.label
			if dim {
				line = styleDim.Render(line)
			}
			b.WriteString("  " + line + "\n")
		}
	}
	writeSection := func(title string, rows []binding) { writeRows(title, rows, false) }

	writeOrientation(&b, ctx, st.tabs)

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
		writeSection(string(ctx), withoutGitHub(bindingsFor(ctx), github))
	}
	if isHomeContext(ctx) {
		writeSection("actions now (on "+st.targetNoun()+")", now)
		writeRows("not available now", later, true)
	}
	writeSection("global keys", global)
	// The three popups' keys ride along with the panels they pop up over, and
	// they come after the keys that work *now* rather than before them. Each
	// popup prints its own keys inside itself while it owns the keyboard, and
	// while one is open it is the context (task 059), so these rows are here
	// for completeness: the sheet scrolls (task 129.17), but what a short
	// terminal shows first should be what works, not the hypothetical (task
	// 025 — a second popup section is what made the difference visible).
	if isHomeContext(ctx) {
		writeSection("answer form (while a task is waiting on you)", bindingsFor(ctxForm))
		writeSection("repair form ("+opKey(keymap.Repair)+" on a blocked task)", bindingsFor(ctxRepairForm))
		writeSection("follow-up form ("+opKey(keymap.FollowUp)+" on a finished task)", bindingsFor(ctxFollowUpForm))
	}
	writeSection("go to (from the "+opKey(keymap.Palette)+" palette)", nav)
	return b.String()
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

// writeOrientation is the sheet's "this screen" header (task 129.17): what
// the surface is made of, before any key list. The workspace gets its strip,
// tab by tab, in drawn order; the board gets the three moves that get a
// newcomer anywhere. Every other surface is small enough that its own
// section already leads.
func writeOrientation(b *strings.Builder, ctx bindingContext, tabs []taskViewTab) {
	var rows [][2]string
	switch {
	case tabs != nil:
		for _, tab := range tabs {
			rows = append(rows, [2]string{taskTabDigits[tab], taskTabNames[tab] + " — " + taskTabPurpose[tab]})
		}
	case ctx == ctxTasks:
		rows = [][2]string{
			{opKey(keymap.OpenRow), "open the task — its workspace, tab by tab"},
			{opKey(keymap.NextAttention), "jump to the next task that needs you"},
			{opKey(keymap.Palette), "find everything — every command, beside its key"},
		}
	}
	if len(rows) == 0 {
		return
	}
	b.WriteString("\n " + styleTitle.Render("THIS SCREEN") + "\n\n")
	for _, r := range rows {
		b.WriteString("  " + padRight(r[0], 8) + r[1] + "\n")
	}
}

// helpScrollCue is the sheet's last line when it does not fit: where the
// window is, and which way there is more.
func helpScrollCue(from, visible, total int) string {
	to := min(from+visible, total)
	arrows := ""
	if from > 0 {
		arrows += "↑"
	}
	if to < total {
		arrows += "↓"
	}
	return styleDim.Render(fmt.Sprintf(" %s more · lines %d–%d of %d", arrows, from+1, to, total))
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s + " "
	}
	return s + strings.Repeat(" ", width-len(s))
}
