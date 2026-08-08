package tui

import "strings"

// helpText is the §15 global-keys overlay. View-specific keys (p, c, a, r,
// s, A, /, enter, n) arrive with their views in later Phase 3 PRs and get
// documented here as they land.
func helpText() string {
	rows := [][2]string{
		{"?", "toggle this help"},
		{"1..6", "switch view: board · task detail · new task · projects · workflows · daemon"},
		{"r", "retry connecting (when the daemon is unreachable)"},
		{"q", "quit the TUI (the daemon keeps running)"},
		{"ctrl+c", "quit the TUI"},
	}
	var b strings.Builder
	b.WriteString("\n  Keys\n\n")
	for _, r := range rows {
		b.WriteString("  ")
		b.WriteString(padRight(r[0], 8))
		b.WriteString(r[1])
		b.WriteString("\n")
	}
	b.WriteString("\n  View-specific actions land with their views (PRs I–M).\n")
	return b.String()
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s + " "
	}
	return s + strings.Repeat(" ", width-len(s))
}
