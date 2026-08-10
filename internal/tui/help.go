package tui

import "strings"

// helpText is the §15 keys overlay.
func helpText() string {
	rows := [][2]string{
		{"?", "toggle this help"},
		{"tab", "move focus between the panels (shift+tab goes back)"},
		{"1..6", "switch screen: home (board+detail) · new task · projects · workflows · daemon"},
		{"esc", "back to the task table; on it, clear the filter"},
		{"n", "start a new task for the project you are looking at"},
		{"q", "quit the TUI (the daemon keeps running)"},
		{"ctrl+c", "quit the TUI"},
	}
	// The action keys are offered only when the daemon lists them for the
	// selected task, so an action that is not valid right now is not shown.
	actionRows := [][2]string{
		{"p", "pause a running task, or resume a paused one"},
		{"a / x", "approve or reject a gate"},
		{"r", "retry a blocked task (retry connecting when the daemon is unreachable)"},
		{"E", "edit the failing step's prompt or command in $EDITOR, then retry"},
		{"s", "skip the current step"},
		{"c", "cancel the task (asks first — a running step is killed)"},
		{"A", "archive a finished task (asks first — the worktree is removed)"},
	}
	boardRows := [][2]string{
		{"↑/↓", "move the selection (the panels below follow the cursor)"},
		{"enter", "open the selected task in the panels — or its answer form when it is asking"},
		{"/", "filter by id, title, project or state"},
	}
	detailRows := [][2]string{
		{"d", "switch the pane between output and diff (reloads the diff)"},
		{"↑/↓", "select an attempt, or scroll the pane"},
		{"f / G", "follow the live output again"},
	}
	formRows := [][2]string{
		{"space", "pick an option (toggles, for a multi-select question)"},
		{"e", "type your own answer — options are suggestions, never a list"},
		{"enter", "submit the answer; the run resumes where it stopped"},
		{"esc", "close the popup without answering (what you picked is kept)"},
	}
	newTaskRows := [][2]string{
		{"↑/↓", "move between fields"},
		{"enter", "open the focused field's editor or picker"},
		{"e", "edit the description in $EDITOR"},
		{"+ / -", "nudge the priority (higher runs first)"},
		{"a / d", "in the fields editor: add or delete a key/value pair"},
		{"R", "re-probe the adapters (the list is otherwise cache-served)"},
		{"ctrl+s", "create the task"},
		{"esc", "leave the field, then the form (a touched draft asks first)"},
	}
	projectRows := [][2]string{
		{"a", "register a repository"},
		{"enter/e", "edit the selected project"},
		{"d", "remove the project (asks first; its task rows go with it)"},
		{"/", "filter by name or path"},
		{"ctrl+s", "in the form: save"},
	}
	workflowRows := [][2]string{
		{"↑/↓", "move between entries"},
		{"enter", "show the entry's steps"},
		{"e", "open the workflow file in $EDITOR (the view updates when you save)"},
		{"R", "re-read the registry"},
	}
	daemonRows := [][2]string{
		{"R", "re-read the daemon info, the config and the log"},
		{"f / G", "follow the end of the log again"},
		{"↑/↓", "scroll the log"},
	}
	var b strings.Builder
	b.WriteString("\n  Global keys\n\n")
	writeKeyRows(&b, rows)
	b.WriteString("\n  Task actions (on the selected task, board or detail)\n\n")
	writeKeyRows(&b, actionRows)
	b.WriteString("\n  Board\n\n")
	writeKeyRows(&b, boardRows)
	b.WriteString("\n  Task detail\n\n")
	writeKeyRows(&b, detailRows)
	b.WriteString("\n  Answer form (while a task is waiting on you)\n\n")
	writeKeyRows(&b, formRows)
	b.WriteString("\n  New task\n\n")
	writeKeyRows(&b, newTaskRows)
	b.WriteString("\n  Projects\n\n")
	writeKeyRows(&b, projectRows)
	b.WriteString("\n  Workflows\n\n")
	writeKeyRows(&b, workflowRows)
	b.WriteString("\n  Daemon (the only view that still works when the daemon is down)\n\n")
	writeKeyRows(&b, daemonRows)
	return b.String()
}

func writeKeyRows(b *strings.Builder, rows [][2]string) {
	for _, r := range rows {
		b.WriteString("  ")
		b.WriteString(padRight(r[0], 8))
		b.WriteString(r[1])
		b.WriteString("\n")
	}
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s + " "
	}
	return s + strings.Repeat(" ", width-len(s))
}
