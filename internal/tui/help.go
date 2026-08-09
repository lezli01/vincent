package tui

import "strings"

// helpText is the §15 global-keys overlay. View-specific keys (p, c, a, r,
// s, A, n) arrive with their views in later Phase 3 PRs and get documented
// here as they land.
func helpText() string {
	rows := [][2]string{
		{"?", "toggle this help"},
		{"1..6", "switch view: board · task detail · new task · projects · workflows · daemon"},
		{"r", "retry connecting (when the daemon is unreachable)"},
		{"q", "quit the TUI (the daemon keeps running)"},
		{"ctrl+c", "quit the TUI"},
	}
	boardRows := [][2]string{
		{"↑/↓", "move the selection"},
		{"enter", "open the selected task"},
		{"/", "filter by id, title, project or state"},
		{"esc", "clear the filter"},
	}
	var b strings.Builder
	b.WriteString("\n  Global keys\n\n")
	writeKeyRows(&b, rows)
	b.WriteString("\n  Board\n\n")
	writeKeyRows(&b, boardRows)
	b.WriteString("\n  Task actions (pause, cancel, approve, retry, skip, archive) land in PR K.\n")
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
