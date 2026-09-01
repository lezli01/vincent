package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The chats board's rendering, split from the view for the reason every other
// screen's is: what a screen *is* and what it *looks like* change for
// different reasons and at different rates.

func (v *chatsView) render(width, height int) string {
	if width < 4 || height < 2 {
		return ""
	}
	lines := make([]string, 0, height)
	lines = append(lines, v.headerLine(width))
	if v.filtering || v.filter.Value() != "" {
		v.filter.SetWidth(max(width-1, 10))
		lines = append(lines, fieldRows(" ", v.filter)...)
	}
	lines = append(lines, "")

	body, cursorRow := v.bodyLines(width)
	footer := v.footerLines()
	room := max(height-len(lines)-len(footer), 1)
	lines = append(lines, window(body, cursorRow, room)...)
	lines = append(lines, footer...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	out := strings.Join(lines, "\n")
	if v.create != nil {
		return v.create.render(width, height)
	}
	return out
}

// headerLine says what the board is showing and how many chats are waiting on
// a human. The badge is here and nowhere else: the task board's own
// needs-attention count is untouched by a chat (task 067 decision 4).
func (v *chatsView) headerLine(width int) string {
	left := " " + styleTitle.Render("chats")
	switch {
	case v.loadErr != "":
		left += styleDim.Render("  ·  " + v.loadErr)
	case !v.loaded && v.loading:
		left += styleDim.Render("  ·  loading…")
	default:
		left += styleDim.Render("  ·  " + plural(len(v.chats), "chat", "chats"))
		// Which listing is on, named only when it is not the default: a
		// board hiding terminal chats must say so once `s` has been pressed,
		// or an empty board reads as no chats at all (issue #298).
		if v.scope != apiclient.ArchivedExclude {
			left += styleDim.Render(" · " + chatScopeLabel(v.scope))
		}
		if n := countChatsAwaiting(v.chats); n > 0 {
			left += styleDim.Render(" · ") +
				styleWarn.Render(fmt.Sprintf("%d waiting on you", n))
		}
	}
	right := ""
	if !v.lastLoad.IsZero() {
		right = styleDim.Render("updated "+v.lastLoad.Format("15:04:05")) + " "
	}
	return padBetween(left, right, width)
}

// bodyLines is the grouped board, and the index of the cursor's line so the
// window can follow it.
func (v *chatsView) bodyLines(width int) (lines []string, cursorRow int) {
	rows := v.rows()
	if len(rows) == 0 {
		if v.loaded {
			return []string{styleDim.Render("  No chats. Press n to start one.")}, 0
		}
		return []string{styleDim.Render("  …")}, 0
	}
	lines = make([]string, 0, len(rows))
	for i, r := range rows {
		if i == v.cursor {
			cursorRow = len(lines)
		}
		lines = append(lines, v.rowLine(r, i == v.cursor, width))
	}
	return lines, cursorRow
}

func (v *chatsView) rowLine(r chatRow, selected bool, width int) string {
	cursor := "  "
	if selected {
		cursor = "▸ "
	}
	if r.header {
		marker := "▾"
		if r.collapsed {
			marker = "▸"
		}
		return cursor + styleTitle.Render(fmt.Sprintf("%s %s", marker, r.label)) +
			styleDim.Render(fmt.Sprintf("  (%d)", r.count))
	}
	c := r.chat
	// Columns: id, state, agent, turns, last activity, then the title with
	// whatever width is left. The title is last because it is the only cell
	// that can usefully be truncated.
	// The activity cell is wide enough for the absolute stamp a terminal chat
	// shows instead of a duration (issue #298).
	fixed := fmt.Sprintf("%-5s %-15s %-8s %5s %11s  ",
		"#"+strconv.FormatInt(c.ID, 10),
		applyStateStyle(c.State, chatStateLabel(c.State)),
		c.Agent,
		"", // the turn count is not on the list DTO; see chatTurnsCell
		chatActivity(*c, v.now()))
	title := c.Title
	if room := width - len(cursor) - ansi.StringWidth(fixed); room > 0 {
		title = ansi.Truncate(title, room, "…")
	}
	return cursor + fixed + title
}

// chatStateLabel is §5.5's vocabulary as a human reads it. `awaiting_input`
// is spelled out because it is the one state that is asking for something.
func chatStateLabel(state string) string {
	switch state {
	case "awaiting_input":
		return "waiting on you"
	case "running":
		return "running"
	case "idle":
		return "idle"
	case "archived":
		return "archived"
	case "handed_off":
		return "handed off"
	default:
		return state
	}
}

func (v *chatsView) footerLines() []string {
	if v.confirm != nil {
		return []string{"", " " + styleWarn.Render(v.confirm.text)}
	}
	if v.note != "" {
		style := styleDim
		if v.noteBad {
			style = styleWarn
		}
		return []string{"", " " + style.Render(v.note)}
	}
	return nil
}
