package tui

import "strings"

// helpText is the §15 keys overlay, rendered from the binding registry —
// the same source the palette (and T3.12's footer) draw from, so the help
// cannot promise a key the code does not bind.
func helpText() string {
	var b strings.Builder
	writeSection := func(title string, rows []binding) {
		if len(rows) == 0 {
			return
		}
		b.WriteString("\n  " + title + "\n\n")
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
	for _, r := range bindings {
		switch {
		case r.nav:
			nav = append(nav, r)
		case r.scope == scopeGlobal:
			global = append(global, r)
		case r.scope == scopeTaskAction:
			actions = append(actions, r)
		}
	}
	writeSection("Global keys", global)
	writeSection("Go to (from the : palette)", nav)
	writeSection("Task actions (on the selected task, offered only when valid)", actions)
	for _, sec := range []struct {
		ctx   bindingContext
		title string
	}{
		{ctxTasks, "Task table"},
		{ctxTimeline, "Timeline"},
		{ctxOutput, "Output pane"},
		{ctxForm, "Answer form (while a task is waiting on you)"},
		{ctxNewTask, "New task"},
		{ctxProjects, "Projects"},
		{ctxWorkflows, "Workflows"},
		{ctxDaemon, "Daemon (the only view that still works when the daemon is down)"},
	} {
		writeSection(sec.title, bindingsFor(sec.ctx))
	}
	return b.String()
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s + " "
	}
	return s + strings.Repeat(" ", width-len(s))
}
