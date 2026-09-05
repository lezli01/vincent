package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task states as they appear on the wire (§6). They are duplicated here as
// plain strings rather than imported from internal/store: the TUI is an API
// client and must not link the daemon's persistence layer.
const (
	stateQueued        = "queued"
	stateRunning       = "running"
	stateAwaitingGate  = "awaiting_gate"
	stateAwaitingInput = "awaiting_input"
	// stateAwaitingChildren is a fan-out parent waiting on its lanes (§7.6,
	// task 014). It is *not* an attention state: nobody is being asked for
	// anything by the parent itself — though a blocked lane inside it is,
	// which is what the rollup in its row says.
	stateAwaitingChildren = "awaiting_children"
	stateBlocked          = "blocked"
	statePaused           = "paused"
	stateDone             = "done"
	stateAborted          = "aborted"
	stateArchived         = "archived"
)

// needsAttention reports whether a task is waiting on a human (§15). These
// are the states pinned to the top of the board.
func needsAttention(state string) bool {
	switch state {
	case stateAwaitingInput, stateAwaitingGate, stateBlocked:
		return true
	default:
		return false
	}
}

// Sort bands. The board is ordered by band first, then within a band by the
// rule that band cares about — so the queue reads in the order it will
// actually run, and work waiting on a human is never below it.
const (
	bandAttention = iota
	bandRunning
	bandQueued
	bandPaused
	bandTerminal
)

func band(state string) int {
	switch {
	case needsAttention(state):
		return bandAttention
	case state == stateRunning, state == stateAwaitingChildren:
		// A parked parent bands with running work: its subtree is running,
		// and sorting it down among terminal tasks would hide an active
		// fan-out at the bottom of the board.
		return bandRunning
	case state == stateQueued:
		return bandQueued
	case state == statePaused:
		return bandPaused
	default:
		return bandTerminal
	}
}

// sortTasks orders tasks for display, in place.
func sortTasks(tasks []apiclient.Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		ba, bb := band(a.State), band(b.State)
		if ba != bb {
			return ba < bb
		}
		switch ba {
		case bandAttention:
			// Oldest wait first: the task that has been blocked on a human
			// longest is the one most likely to be forgotten.
			return a.UpdatedAt.Before(b.UpdatedAt)
		case bandRunning:
			// Longest-running first, which surfaces a wedged task.
			return startedBefore(a, b)
		case bandQueued:
			// True scheduler order (§11), so the board answers "what runs
			// next" without the reader having to know the rule.
			if a.Priority != b.Priority {
				return a.Priority > b.Priority
			}
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
			return a.ID < b.ID
		default:
			// Everything settled reads newest first.
			return a.ID > b.ID
		}
	})
}

func startedBefore(a, b apiclient.Task) bool {
	switch {
	case a.StartedAt == nil && b.StartedAt == nil:
		return a.ID < b.ID
	case a.StartedAt == nil:
		return false
	case b.StartedAt == nil:
		return true
	case a.StartedAt.Equal(*b.StartedAt):
		return a.ID < b.ID
	default:
		return a.StartedAt.Before(*b.StartedAt)
	}
}

// filterTasks keeps tasks matching a case-insensitive substring of the id,
// title, project name or state. Filtering is client-side: it runs on every
// keystroke against a list already in memory, so a round-trip per character
// would be pure latency.
func filterTasks(tasks []apiclient.Task, query string) []apiclient.Task {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return tasks
	}
	out := make([]apiclient.Task, 0, len(tasks))
	for _, t := range tasks {
		haystack := strings.ToLower(strings.Join([]string{
			strconv.FormatInt(t.ID, 10), t.Title, t.ProjectName, t.State,
		}, " "))
		if strings.Contains(haystack, q) {
			out = append(out, t)
		}
	}
	return out
}

// countAttention feeds the header, and countRunning feeds it only when
// /v1/info has never loaded (board.slotsUsed). Both count the whole fetched
// list, not the filtered view: a filter hiding the one task that needs you
// must not also hide that it exists.
//
// countRunning is not §11's slot count and cannot be made into one from this
// list: it sees neither the fan-out lanes, which never enter it, nor
// `awaiting_input`, which holds a slot without being `running` (issue #324).
// It is the honest thing to render from a board that never reached the
// daemon, and nothing more.
func countRunning(tasks []apiclient.Task) int {
	n := 0
	for _, t := range tasks {
		if t.State == stateRunning {
			n++
		}
	}
	return n
}

func countAttention(tasks []apiclient.Task) int {
	n := 0
	for _, t := range tasks {
		if needsAttention(t.State) {
			n++
		}
	}
	return n
}

var stateStyles = map[string]lipgloss.Style{
	stateRunning:       lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
	stateQueued:        lipgloss.NewStyle().Faint(true),
	stateAwaitingInput: lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true),
	stateAwaitingGate:  lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	// Cyan like running: the subtree is working, just not on this row.
	stateAwaitingChildren: lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
	stateBlocked:          lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
	statePaused:           lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
	stateDone:             lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
	stateAborted:          lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	stateArchived:         lipgloss.NewStyle().Faint(true),
}

// attentionBadge marks a row waiting on a human. It is a glyph rather than
// colour alone so the distinction survives a monochrome terminal.
const attentionBadge = "!"

// stateLabel is a state as it reads before any style is applied.
func stateLabel(state string) string {
	if needsAttention(state) {
		return attentionBadge + " " + state
	}
	return state
}

func renderState(state string) string { return applyStateStyle(state, stateLabel(state)) }

// boardStateLabel is the board's state cell as plain text. A queued task held
// by the scheduler (§11) shows when it resumes — `queued → 14:20` — so it
// reads as waiting on a clock rather than on a slot, which a bare `queued`
// cannot say.
//
// The reason itself is deliberately not in this cell: it does not fit
// widthState, and widening the column for a rare state would cost every board
// the columns that get shed first. The detail header, which has the width,
// names it (renderDetailState).
//
// Plain rather than styled because the board wraps this cell (task 050): the
// text is wrapped first and each produced line styled after, so no wrapping
// is ever ANSI-aware — the same order the output pane uses (v0 T4.16).
// What no longer fits after wrapping is cut on the row's last line, so
// `awaiting_children (2 blocked)` is readable on the board rather than only
// in the detail view.
func boardStateLabel(t apiclient.Task) string {
	// A fan-out parent says what its subtree is doing, because its own state
	// says nothing a reader can act on (task 014). A blocked lane is
	// invisible in the task list by design (decision 13), and this is what
	// pays for that.
	if t.State == stateAwaitingChildren && t.Children != nil {
		if label := t.Children.Summary(); label != "" {
			return t.State + " (" + label + ")"
		}
	}
	_, until, ok := t.Hold()
	if !ok || until == nil {
		return stateLabel(t.State)
	}
	return t.State + " → " + until.Local().Format("15:04")
}

// renderBoardState is boardStateLabel with the state's colour applied — the
// whole cell in one piece, for anything that wants the rendered form without
// the board's line layout.
func renderBoardState(t apiclient.Task) string {
	return applyStateStyle(t.State, boardStateLabel(t))
}

// renderDetailState is the header form: the full `queued · usage limit →
// 14:20`, where there is room for the reason. A hold with no resume time
// still names the reason — the engine always computes one, so this is the
// shape a future hold-setter that does not would take.
func renderDetailState(t apiclient.Task) string {
	reason, until, ok := t.Hold()
	if !ok {
		return renderState(t.State)
	}
	label := t.State + " · " + strings.ReplaceAll(reason, "_", " ")
	if until != nil {
		label += " → " + until.Local().Format("15:04")
	}
	return applyStateStyle(t.State, label)
}

func applyStateStyle(state, label string) string {
	if st, ok := stateStyles[state]; ok {
		return st.Render(label)
	}
	return label
}

// formatElapsed renders a duration compactly enough for a table cell.
func formatElapsed(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// formatCost renders cost-so-far. A task no adapter reported a cost for
// shows a dash: "$0.00" would be a claim the daemon never made.
func formatCost(c *float64) string {
	if c == nil {
		return "—"
	}
	return fmt.Sprintf("$%.2f", *c)
}

// formatStatus renders the newest step run's own status message in the board
// cell (§5.4, task 036). Empty rather than a dash: a step saying nothing is
// the ordinary case for most step types and every workflow that has not
// adopted the protocol, and a column of dashes reads as something missing.
//
// The whole message, uncut: a board row is up to three lines tall now and the
// cell wraps across them (task 050 decision 6), so the message is readable
// here rather than only on the attempt line in the detail view. What still
// does not fit at three lines is cut there, by wrapCellLines — the width this
// used to truncate at is a column *base* now, not the width the cell renders
// at, so this function has no width to cut against.
func formatStatus(message *string) string {
	if message == nil || *message == "" {
		return ""
	}
	return *message
}

// boardRowLines is the ceiling on a board row's height (task 050 decision 4).
// Three: two lines is not enough for a status message at a narrow width, and
// past three a board is a list of paragraphs rather than a table you scan.
const boardRowLines = 3

// wrapCellLines lays a cell's text out across a column, at most height lines,
// with whatever is left over cut with an ellipsis on the last one — which is
// what the table did to the whole cell before it wrapped.
//
// The text is plain, never styled: the style goes on each produced line
// afterwards (task 050 decision 8), following the output pane's recorded
// precedent (v0 T4.16). That keeps escape sequences out of the width
// arithmetic entirely, so no wrapping here has to be ANSI-aware.
func wrapCellLines(text string, width, height int) []string {
	if text == "" || width <= 0 || height <= 0 {
		return nil
	}
	// A cell is one run of text however the daemon spelled it: a newline
	// inside a status message would otherwise become a line the row never
	// budgeted for, and the table would render it into the row below.
	if strings.ContainsAny(text, "\n\r\t") {
		text = strings.Join(strings.Fields(text), " ")
	}
	if ansi.StringWidth(text) <= width {
		return []string{text}
	}
	lines := strings.Split(ansi.Wrap(text, width, "-"), "\n")
	if len(lines) <= height {
		return lines
	}
	// Everything past the last line is rejoined and cut there, so the
	// ellipsis says "there is more" rather than the row simply stopping.
	rest := strings.Join(lines[height-1:], " ")
	return append(lines[:height-1:height-1], ansi.Truncate(rest, width, "…"))
}

// formatStep renders k/n plus the step name when there is room. A snapshot
// that would not parse has no step count, so it renders a dash rather than
// a confident "1/0".
//
// A task inside a `loop` gets its position appended — `3/7 green · loop 4/10
// · repair 2/3` — because k/n counts the whole loop as one step and so says
// nothing about either number that is moving (§7.8, task 016 decision 14).
// Every other task carries no rollup and reads exactly as it did.
//
// Those clauses are *fitted* to the column rather than left for the cell to
// wrap: three of them outgrow every width below widthStepMax, and a STEP
// column spilling onto a second and third line to finish a counter is the
// row height spent on the least of what the row says. width is the STEP
// column's own width, so the clauses drop from the tail — the body step
// first, then the item, then the counter — and what is left always fits on
// one line.
func formatStep(t apiclient.Task, withName bool, width int) string {
	k, n, ok := t.StepDisplay()
	if !ok {
		return "—"
	}
	s := fmt.Sprintf("%d/%d", k, n)
	if !withName {
		// The narrow column shows k/n alone; the loop goes with the name,
		// because a bare `loop 4/10` beside no step name names nothing.
		return s
	}
	if t.StepName != "" {
		s += " " + t.StepName
	}
	for clauses := t.Loop.Clauses(); len(clauses) > 0; clauses = clauses[:len(clauses)-1] {
		fitted := s + " · " + strings.Join(clauses, " · ")
		if ansi.StringWidth(fitted) <= width {
			return fitted
		}
	}
	return s
}
