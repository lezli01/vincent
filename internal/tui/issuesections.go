package tui

import (
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The issues list as a board (§15 view 12, task 134.8).
//
// An issue sits in one of four lanes the daemon derives from its state and
// its root tasks (task 134 decisions 1–4) and serves as `lane` on every row.
// The TUI never derives one: it partitions the listing by that field and
// draws each lane as a column of cards (task 150 decision 3, superseding task
// 134 decision 6's stacked sections; issueboard.go). A lane is a fact about the
// issue's tasks rather than a field anyone sets, so `m` runs the action a lane
// is derived from (task 145).
//
// "Section" and issueSection, not "lane" alone: boardlanes.go and
// keymap.Lane already use that word for a fan-out's children.

// issueSection is one section of the list: the wire lane it holds and the
// label its header draws.
type issueSection struct {
	lane  string
	label string
}

// issueSections is the board order (decision 6): what is waiting to be
// started, what is being worked on, what is waiting on a human to take it
// over, and what is finished. The lane strings are the wire values
// issuestate.Lanes serves, duplicated here for the reason boardgroup.go's
// groupKey is — the TUI renders what comes from the wire — and
// issuesections_test.go holds the two together.
var issueSections = []issueSection{
	{lane: "open", label: "open"},
	{lane: "in_progress", label: "in progress"},
	{lane: "hand_off", label: "hand-off"},
	{lane: "done", label: "done"},
}

// issueLaneDone is the section `s` shows and hides (decision 5).
const issueLaneDone = "done"

// issueSectionOf is the index of the section an issue belongs in. A lane
// this TUI does not know — a newer daemon's, or none from one older than
// task 134.4 — falls into the first section rather than off the screen: a
// row in the wrong section is visible and wrong, a row in no section is
// silently gone.
func issueSectionOf(iss apiclient.Issue) int {
	for i, s := range issueSections {
		if s.lane == iss.Lane {
			return i
		}
	}
	return 0
}

// issueRow is one entry of the board's flat order: a column's head, or an
// issue in it.
type issueRow struct {
	issue apiclient.Issue
	// header marks a column's head; section is the index into issueSections
	// of the column.
	header  bool
	section int
}

// selectable reports whether the cursor may rest on the row: only an issue.
func (r issueRow) selectable() bool { return !r.header }

// shownSections is how many sections the list draws: three while `done` is
// hidden, four once `s` shows it.
func (v *issuesView) shownSections() int {
	if v.showDone {
		return len(issueSections)
	}
	return len(issueSections) - 1
}

// filtered is the issues `/` keeps, in the daemon's order.
func (v *issuesView) filtered() []apiclient.Issue {
	q := strings.ToLower(strings.TrimSpace(v.filter.Value()))
	if q == "" {
		return v.issues
	}
	out := make([]apiclient.Issue, 0, len(v.issues))
	for _, iss := range v.issues {
		if issueMatches(iss, q) {
			out = append(out, iss)
		}
	}
	return out
}

// partition splits issues into the sections, stably: the daemon's order
// holds within each.
func partitionIssues(issues []apiclient.Issue) [][]apiclient.Issue {
	out := make([][]apiclient.Issue, len(issueSections))
	for _, iss := range issues {
		i := issueSectionOf(iss)
		out[i] = append(out[i], iss)
	}
	return out
}

// lines is the board in flat order: every shown column's head, then its
// issues.
func (v *issuesView) lines() []issueRow {
	parts := partitionIssues(v.filtered())
	out := make([]issueRow, 0, len(v.issues)+len(issueSections))
	for i := range v.shownSections() {
		out = append(out, issueRow{header: true, section: i})
		for _, iss := range parts[i] {
			out = append(out, issueRow{issue: iss, section: i})
		}
	}
	return out
}

// rows is every issue on screen, in section order: the issues `/` keeps, which is what a count of the list means.
func (v *issuesView) rows() []issueRow {
	parts := partitionIssues(v.filtered())
	out := make([]issueRow, 0, len(v.issues))
	for i := range v.shownSections() {
		for _, iss := range parts[i] {
			out = append(out, issueRow{issue: iss, section: i})
		}
	}
	return out
}

// current is the issue under the cursor.
func (v *issuesView) current() (issueRow, bool) {
	lines := v.lines()
	if v.cursor < 0 || v.cursor >= len(lines) {
		return issueRow{}, false
	}
	row := lines[v.cursor]
	if row.header {
		return issueRow{}, false
	}
	return row, true
}

// cursorSection is the section the cursor is in, -1 on an empty list.
func (v *issuesView) cursorSection() int {
	lines := v.lines()
	if v.cursor < 0 || v.cursor >= len(lines) {
		return -1
	}
	return lines[v.cursor].section
}

// moveCursor moves the selection delta cards within its column.
func (v *issuesView) moveCursor(delta int) { v.moveInLane(delta) }

// pageCursor moves the selection by delta cards — a page, what the column
// showed in the last render (#796) — clamped to the column.
func (v *issuesView) pageCursor(delta int) { v.moveInLane(delta) }

// edgeCursor selects the first or last card of the selection's column.
func (v *issuesView) edgeCursor(last bool) { v.edgeOfLane(last) }

// clampCursor puts the cursor on the selectable row nearest its index —
// below first, then above — and records what it is on.
func (v *issuesView) clampCursor() {
	lines := v.lines()
	v.cursor = min(max(v.cursor, 0), max(len(lines)-1, 0))
	if len(lines) == 0 || lines[v.cursor].selectable() {
		v.remember(lines)
		return
	}
	for d := 1; d < len(lines); d++ {
		for _, i := range []int{v.cursor + d, v.cursor - d} {
			if i >= 0 && i < len(lines) && lines[i].selectable() {
				v.cursor = i
				v.remember(lines)
				return
			}
		}
	}
	v.remember(lines)
}

// remember records what the cursor is on, so a re-list — which can reorder
// the rows and move an issue between lanes — keeps the selection on the issue
// rather than on the index.
func (v *issuesView) remember(lines []issueRow) {
	v.selected = 0
	if v.cursor < 0 || v.cursor >= len(lines) {
		return
	}
	if row := lines[v.cursor]; !row.header {
		v.selected = row.issue.ID
	}
}

// reselect puts the cursor back on the issue it was on before a re-list,
// wherever that now is. An issue that has left the view — closed while `done`
// is hidden, or deleted — leaves the cursor at its index, on the nearest card.
func (v *issuesView) reselect() {
	lines := v.lines()
	for i, row := range lines {
		if v.selected != 0 && !row.header && row.issue.ID == v.selected {
			v.cursor = i
			return
		}
	}
	v.clampCursor()
}

// reselectKeep is reselect for a filter keystroke: when the filter hides the
// selected issue, the cursor rests elsewhere but the selection is kept, so
// clearing the filter brings it back (#1058).
func (v *issuesView) reselectKeep() {
	sel := v.selected
	v.reselect()
	if sel != 0 {
		v.selected = sel
	}
}

// selectIssue moves the cursor onto an issue by id, if it is on screen.
func (v *issuesView) selectIssue(id int64) {
	v.selected = id
	v.reselect()
}
