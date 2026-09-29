package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/reasons"
)

// The TUI's display rule (§15, task 129.7): no snake_case identifier is
// rendered as prose. A state reads as its words, a reason as its catalogue
// title (internal/reasons) with the raw code dim beside it — the code is what
// the CLI, the API and MCP speak, and the dim token is the bridge between the
// two spellings. Task Details is the one exception: it keeps the raw
// identifiers for copy-paste (task 049 decision 3).

// stateWords is a task state (§6) as the TUI prints it. The words stay close
// to the identifiers so the dim raw codes elsewhere never read as a
// translation: only the three states with an underscore change, and
// `awaiting_gate` names what the human does rather than the workflow term.
func stateWords(state string) string {
	switch state {
	case stateAwaitingInput:
		return "awaiting input"
	case stateAwaitingGate:
		return "awaiting approval"
	case stateAwaitingChildren:
		return "waiting on lanes"
	default:
		return state
	}
}

// reasonTitle is a block, failure, skip or hold reason's plain-language
// title. An unknown code explains as itself (reasons.Explain).
func reasonTitle(code string) string { return reasons.Explain(code).Title }

// reasonSeparator joins a reason's title to its raw code. It is a glyph
// rather than colour alone so the code stays distinguishable from its title
// on a monochrome terminal.
const reasonSeparator = " · "

// reasonText is `check failed · check_failed` as plain text, for a caller that
// wraps or measures before it styles. An unknown code renders once: its title
// is the code, and `x · x` would say nothing twice.
func reasonText(code string) string {
	title := reasonTitle(code)
	if title == code {
		return code
	}
	return title + reasonSeparator + code
}

// renderReason is reasonText styled: the title in style, the separator and
// the raw code faint. Every detail surface that names a reason renders it
// through here, so the rule has one spelling.
func renderReason(code string, style lipgloss.Style) string {
	title := reasonTitle(code)
	if title == code {
		return style.Render(code)
	}
	return style.Render(title) + styleDim.Render(reasonSeparator+code)
}

// styleNone renders its text unchanged, for a reason whose title takes no
// colour of its own.
var styleNone = lipgloss.NewStyle()
