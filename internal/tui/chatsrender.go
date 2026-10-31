package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The chats board's rendering, split from the view for the reason every other
// screen's is: what a screen *is* and what it *looks like* change for
// different reasons and at different rates.

func (v *chatsView) render(width, height int) string {
	if width < 4 || height < 2 {
		return ""
	}
	if v.create != nil {
		// The form is the whole screen while it is open, held to the board's
		// height like anything else the board returns: a form taller than
		// the pane would push the frame's own footer off it (#801 F13).
		return clipLines(v.create.render(width, height), height)
	}
	lines := make([]string, 0, height)
	lines = append(lines, v.headerLine(width))
	if v.filtering || v.filter.Value() != "" {
		v.filter.SetWidth(max(width-1, 10))
		lines = append(lines, fieldRows(" ", v.filter)...)
	}
	lines = append(lines, "")

	body, cursorRow := v.bodyLines(width)
	if v.hasRows() {
		// The heading sits above the window rather than in it, so scrolling
		// a long board never takes the column names with it.
		lines = append(lines, v.columnHeading())
	}
	footer := v.footerLines(width)
	room := max(height-len(lines)-len(footer), 1)
	lines = append(lines, window(body, cursorRow, room)...)
	lines = append(lines, footer...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

// clipLines keeps at most height lines of a rendered block.
func clipLines(s string, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// headerLine says what the board is showing and how many chats are waiting on
// a human. The badge is here and nowhere else: the task board's own
// needs-attention count is untouched by a chat (task 067 decision 4).
func (v *chatsView) headerLine(width int) string {
	left := " " + styleTitle.Render("chats")
	if v.archived {
		// The window and the page are what this board is showing (#804
		// AR-5), named the way the archived task board names them; the scope
		// label would only repeat the title.
		left = " " + styleTitle.Render("archived chats") + styleDim.Render("  ·  "+v.label())
	}
	switch {
	case v.loadErr != "":
		left += styleDim.Render("  ·  " + v.loadErr)
	case v.project.id == 0:
		left += styleDim.Render("  ·  no project selected")
	case !v.loaded && v.loading:
		// The project's name, not a bare "loading…": after a switch this is
		// the one line saying which project the empty board is waiting on.
		left += styleDim.Render("  ·  loading " + v.project.name + "…")
	default:
		left += styleDim.Render("  ·  " + plural(len(v.chats), "chat", "chats"))
		// Which listing is on, named only when it is not the default: a
		// board hiding terminal chats must say so once `s` has been pressed,
		// or an empty board reads as no chats at all (issue #298).
		if v.scope != apiclient.ArchivedExclude && !v.archived {
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

// bodyLines is the board, and the index of the cursor's line so the window
// can follow it.
func (v *chatsView) bodyLines(width int) (lines []string, cursorRow int) {
	rows := v.rows()
	if v.project.id == 0 {
		// Never an unfiltered list in its place (task 132.10). No selection
		// means no project is registered only once a listing has said so;
		// until then it is still resolving (review F9).
		if v.noProjects {
			return []string{styleDim.Render("  " + noProjectsEmpty())}, 0
		}
		return []string{styleDim.Render("  Resolving the project…")}, 0
	}
	if len(rows) == 0 {
		if v.loaded {
			return []string{styleDim.Render("  No chats. Press " + opKey(keymap.New) + " to start one.")}, 0
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

// hasRows reports whether the body is a list of chats rather than one of its
// empty or resolving messages, which is when the column heading is drawn.
func (v *chatsView) hasRows() bool {
	return v.project.id != 0 && len(v.rows()) > 0
}

// Column widths of a chat row, shared by rowLine and columnHeading so the
// heading cannot drift from the cells it names. The state cell fits the
// widest label, "waiting on you", with a cell to spare.
const (
	chatColID       = 5
	chatColState    = 15
	chatColAgent    = 8
	chatColActivity = 11
)

// chatRowCursorWidth is the display width of the cursor gutter, "▸ " or two
// spaces. Display width, not len: the glyph is three bytes and one column,
// and counting bytes left every title two columns short of the edge (AR-8).
var chatRowCursorWidth = ansi.StringWidth("▸ ")

// chatRowFixed lays out the cells ahead of the title. Every cell is padded
// by display width: the state cell arrives styled, and a running one carries
// a multi-byte spinner frame, so a byte-counting %-15s shifted every later
// column by its escape bytes (#801 F11).
func chatRowFixed(id, state, agent, activity string) string {
	return padRight(id, chatColID+1) +
		padRight(state, chatColState+1) +
		padRight(agent, chatColAgent+1) +
		strings.Repeat(" ", max(chatColActivity-ansi.StringWidth(activity), 0)) + activity + "  "
}

// columnHeading names the board's columns, dim, aligned with chatRowFixed.
func (v *chatsView) columnHeading() string {
	return styleDim.Render(strings.Repeat(" ", chatRowCursorWidth) +
		chatRowFixed("id", "state", "agent", "last active") + "title")
}

func (v *chatsView) rowLine(r chatRow, selected bool, width int) string {
	cursor := "  "
	if selected {
		cursor = "▸ "
	}
	c := r.chat
	// Columns: id, state, agent, last activity, then the title with
	// whatever width is left. The title is last because it is the only cell
	// that can usefully be truncated.
	// The activity cell is wide enough for the absolute stamp a terminal chat
	// shows instead of a duration (issue #298).
	// A running row carries the moving frame *beside* the word, never instead
	// of it (task 089): the state cell is a fixed-width column rendered
	// through the shared chatStateLabel vocabulary, and swapping one state's
	// word for a glyph breaks that vocabulary for that state alone.
	state := chatStateLabel(c.State)
	if c.State == "running" {
		state += " " + spinnerFrame(v.frame)
	}
	fixed := chatRowFixed(
		"#"+strconv.FormatInt(c.ID, 10),
		applyStateStyle(c.State, state),
		c.Agent,
		chatActivity(*c, v.now()))
	title := c.Title
	// A chat opened on a task says which one ahead of its title (task 119):
	// the task holds the worktree and the lock, and it is what a reader of
	// this row goes looking for. A prefix rather than a column, because
	// most chats have no task and a column of dashes would tax every row. A
	// chat opened on an issue says which issue the same way (task 140.9).
	if c.IssueID != nil {
		title = styleDim.Render(fmt.Sprintf("issue #%d · ", *c.IssueID)) + title
	}
	if c.LinkedTaskID != nil {
		title = styleDim.Render(fmt.Sprintf("task #%d · ", *c.LinkedTaskID)) + title
	}
	if room := width - chatRowCursorWidth - ansi.StringWidth(fixed); room > 0 {
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
	case "closed":
		return "closed"
	default:
		return state
	}
}

func (v *chatsView) footerLines(width int) []string {
	// The delete question swallows every key but its answers, so it is
	// drawn first and wrapped whole: a board that hides it looks frozen
	// while `y` still deletes (#790).
	if v.delPrompt != nil {
		return append([]string{""}, wrapStatus(v.delPrompt.question(), width, styleAsk)...)
	}
	if v.confirm != nil {
		// Wrapped for the reason the delete question is: cut to one line,
		// a long question loses its `(y/n)` (#801 F2).
		return append([]string{""}, wrapStatus(v.confirm.text, width, styleWarn)...)
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
