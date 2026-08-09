package tui

import "strings"

// helpText is the §15 keys overlay. The new-task flow (n) and the remaining
// views arrive in later Phase 3 PRs and get documented here as they land.
func helpText() string {
	rows := [][2]string{
		{"?", "toggle this help"},
		{"1..6", "switch view: board · task detail · new task · projects · workflows · daemon"},
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
		{"↑/↓", "move the selection"},
		{"enter", "open the selected task"},
		{"/", "filter by id, title, project or state"},
		{"esc", "clear the filter"},
	}
	detailRows := [][2]string{
		{"tab", "move focus: timeline → output → answer form"},
		{"d", "switch the pane between output and diff (reloads the diff)"},
		{"↑/↓", "select an attempt, or scroll the pane"},
		{"f / G", "follow the live output again"},
		{"esc", "back to the board"},
	}
	formRows := [][2]string{
		{"space", "pick an option (toggles, for a multi-select question)"},
		{"e", "type your own answer — options are suggestions, never a list"},
		{"enter", "submit the answer; the run resumes where it stopped"},
		{"esc", "leave the form without answering"},
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
