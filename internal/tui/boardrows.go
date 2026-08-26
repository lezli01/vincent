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

// countRunning and countAttention feed the header. They count the whole
// fetched list, not the filtered view: a filter hiding the one task that
// needs you must not also hide that it exists.
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

func renderState(state string) string {
	label := state
	if needsAttention(state) {
		label = attentionBadge + " " + state
	}
	if st, ok := stateStyles[state]; ok {
		return st.Render(label)
	}
	return label
}

// renderBoardState is the board's state cell. A queued task held by the
// scheduler (§11) shows when it resumes — `queued → 14:20` — so it reads as
// waiting on a clock rather than on a slot, which a bare `queued` cannot say.
//
// The reason itself is deliberately not in this cell: it does not fit
// widthState, and widening the column for a rare state would cost every board
// the columns that get shed first. The detail header, which has the width,
// names it (renderDetailState).
func renderBoardState(t apiclient.Task) string {
	// A fan-out parent says what its subtree is doing, because its own state
	// says nothing a reader can act on (task 014). A blocked lane is
	// invisible in the task list by design (decision 13), and this is what
	// pays for that.
	if t.State == stateAwaitingChildren && t.Children != nil {
		if label := t.Children.Summary(); label != "" {
			return applyStateStyle(t.State, t.State+" ("+label+")")
		}
	}
	_, until, ok := t.Hold()
	if !ok || until == nil {
		return renderState(t.State)
	}
	return applyStateStyle(t.State, t.State+" → "+until.Local().Format("15:04"))
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
// cell (§5.4, task 033). Empty rather than a dash: a step saying nothing is
// the ordinary case for most step types and every workflow that has not
// adopted the protocol, and a column of dashes reads as something missing.
//
// Truncated with an ellipsis rather than wrapped, because the table gives
// every row one line. The whole message is on the attempt line in the detail
// view, which has the width — the same division of labour renderBoardState
// makes for a hold's reason.
func formatStatus(message *string) string {
	if message == nil || *message == "" {
		return ""
	}
	return ansi.Truncate(*message, widthStatus, "…")
}

// formatStep renders k/n plus the step name when there is room. A snapshot
// that would not parse has no step count, so it renders a dash rather than
// a confident "1/0".
//
// A task inside a `loop` gets its iteration appended — `3/7 green · loop 4/10`
// — because k/n alone says nothing about the one number that is moving
// (§7.8, task 016 decision 14). Every other task carries no rollup and reads
// exactly as it did.
func formatStep(t apiclient.Task, withName bool) string {
	k, n, ok := t.StepDisplay()
	if !ok {
		return "—"
	}
	s := fmt.Sprintf("%d/%d", k, n)
	if withName && t.StepName != "" {
		s += " " + t.StepName
	}
	if loop := t.Loop.Display(); withName && loop != "" {
		s += " · " + loop
	}
	return s
}
