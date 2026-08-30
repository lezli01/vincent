package tui

import "strings"

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
	pinned := styleKey.Render("?") + styleDim.Render(" close  ") +
		styleKey.Render("esc") + styleDim.Render(" close")
	line := " " + styleDim.Render("the keys of the surface you were on, plus the global ones")
	return padBetween(line, pinned, width)
}

// helpText is the §15 cheat sheet for one surface, rendered from the binding
// registry — the same source the palette and the footer draw from, so the
// help cannot promise a key the code does not bind. It shows the keys that
// work *here*: the focused surface's own, the global ones, the task actions,
// and the palette's navigation. Printing all eight surfaces' sections at
// once meant reading past seven irrelevant ones (T3.8 finding).
func helpText(ctx bindingContext, github bool) string {
	var b strings.Builder
	writeSection := func(title string, rows []binding) {
		if len(rows) == 0 {
			return
		}
		b.WriteString("\n " + styleTitle.Render(strings.ToUpper(title)) + "\n\n")
		for _, r := range rows {
			key := r.key
			if key == "" {
				// Palette-only navigation: the palette is its key.
				key = ":"
			}
			b.WriteString("  ")
			b.WriteString(padRight(key, 8))
			b.WriteString(r.label)
			b.WriteString("\n")
		}
	}

	var global, nav, actions []binding
	for _, r := range withoutGitHub(bindings, github) {
		switch {
		case r.nav:
			nav = append(nav, r)
		case r.scope == scopeGlobal:
			global = append(global, r)
		case r.scope == scopeTaskAction:
			actions = append(actions, r)
		}
	}
	// This surface first: it is the answer to "what can I do here".
	if ctx != "" {
		writeSection(string(ctx), withoutGitHub(bindingsFor(ctx), github))
	}
	if isHomeContext(ctx) {
		writeSection("task actions (on the selected task, offered only when valid)", actions)
	}
	writeSection("global keys", global)
	// The three popups' keys ride along with the panels they pop up over, and
	// they come after the keys that work *now* rather than before them. Each
	// popup prints its own keys inside itself while it owns the keyboard, and
	// while one is open it is the context (task 059), so these rows are here
	// for completeness; the sheet has no scroll, and what it drops off the
	// bottom should be the hypothetical rather than `esc` (task 025 — a
	// second popup section is what made the difference visible).
	if isHomeContext(ctx) {
		writeSection("answer form (while a task is waiting on you)", bindingsFor(ctxForm))
		writeSection("repair form (R on a blocked task)", bindingsFor(ctxRepairForm))
		writeSection("follow-up form (F on a finished task)", bindingsFor(ctxFollowUpForm))
	}
	writeSection("go to (from the : palette)", nav)
	return b.String()
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s + " "
	}
	return s + strings.Repeat(" ", width-len(s))
}
