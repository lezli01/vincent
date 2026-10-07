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
// draws each lane as a stacked, foldable section (decision 6) — never as
// columns, and never as something a row can be dragged between, because a
// lane is a fact about the issue's tasks rather than a field anyone sets.
//
// "Section" and issueSection, not "lane" alone: boardlanes.go and
// keymap.Lane already use that word for a fan-out's children.
//
// The fold set is copied from the board's idea rather than generalised out
// of boardgroup.go, whose code is typed over apiclient.Task — the chats
// board's precedent (chatsrows.go).

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

// issueRow is one line of the list: a section header, an issue, or the dim
// placeholder under a section with nothing in it.
type issueRow struct {
	issue apiclient.Issue
	// header marks a section header; section is the index into
	// issueSections of the header or of the issue's section.
	header  bool
	section int
	// count and attention are a header's: how many rows it holds after the
	// filter, and how many of them have a root task waiting on a human. The
	// badge is on the header so a folded section never hides it.
	count     int
	attention int
	collapsed bool
	// none is the placeholder line under an empty section.
	none bool
}

// selectable reports whether the cursor may rest on the row: an issue, or a
// collapsed header standing in for issues not on screen. An expanded header
// is a label, and stepped over, as on the task board (boardgroup.go).
func (r issueRow) selectable() bool {
	if r.header {
		return r.collapsed
	}
	return !r.none
}

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

// lines lays the list out: every shown section's header, then its issues
// unless it is folded, or a placeholder when it holds none.
func (v *issuesView) lines() []issueRow {
	parts := partitionIssues(v.filtered())
	out := make([]issueRow, 0, len(v.issues)+2*len(issueSections))
	for i := range v.shownSections() {
		issues := parts[i]
		attention := 0
		for _, iss := range issues {
			if iss.Attention {
				attention++
			}
		}
		collapsed := v.folded[issueSections[i].lane]
		out = append(out, issueRow{header: true, section: i, count: len(issues), attention: attention, collapsed: collapsed})
		if collapsed {
			continue
		}
		if len(issues) == 0 {
			out = append(out, issueRow{section: i, none: true})
			continue
		}
		for _, iss := range issues {
			out = append(out, issueRow{issue: iss, section: i})
		}
	}
	return out
}

// rows is every issue on screen, in section order, folded sections
// included: the issues `/` keeps, which is what a count of the list means.
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

// laneTotals counts the listing per section, unfiltered, for the totals line.
func (v *issuesView) laneTotals() []int {
	out := make([]int, len(issueSections))
	for _, iss := range v.issues {
		out[issueSectionOf(iss)]++
	}
	return out
}

// current is the issue under the cursor. A collapsed header is not one.
func (v *issuesView) current() (issueRow, bool) {
	lines := v.lines()
	if v.cursor < 0 || v.cursor >= len(lines) {
		return issueRow{}, false
	}
	row := lines[v.cursor]
	if row.header || row.none {
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

// moveCursor steps to the next row the cursor may rest on, in the
// direction of delta, and stays put when there is none.
func (v *issuesView) moveCursor(delta int) {
	lines := v.lines()
	step := 1
	if delta < 0 {
		step = -1
	}
	for i := v.cursor + step; i >= 0 && i < len(lines); i += step {
		if lines[i].selectable() {
			v.cursor = i
			v.remember(lines)
			return
		}
	}
	v.clampCursor()
}

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
// the rows and move an issue between sections — keeps the selection on the
// issue or the folded section rather than on the index.
func (v *issuesView) remember(lines []issueRow) {
	v.selected, v.selectedLane = 0, ""
	if v.cursor < 0 || v.cursor >= len(lines) {
		return
	}
	switch row := lines[v.cursor]; {
	case row.header && row.collapsed:
		v.selectedLane = issueSections[row.section].lane
	case !row.header && !row.none:
		v.selected = row.issue.ID
	}
}

// reselect puts the cursor back on the issue or folded section it was on
// before a re-list, wherever that now is. An issue that has left the view —
// closed while `done` is hidden, or deleted — leaves the cursor at its
// index, on the nearest row.
func (v *issuesView) reselect() {
	lines := v.lines()
	for i, row := range lines {
		switch {
		case v.selected != 0 && !row.header && !row.none && row.issue.ID == v.selected,
			v.selectedLane != "" && row.header && row.collapsed && issueSections[row.section].lane == v.selectedLane:
			v.cursor = i
			return
		}
	}
	v.clampCursor()
}

// selectIssue moves the cursor onto an issue by id, if it is on screen.
func (v *issuesView) selectIssue(id int64) {
	v.selected, v.selectedLane = id, ""
	v.reselect()
}

// fold collapses or expands the cursor's section (← and →). Folding puts
// the cursor on the header, which now stands in for the section's issues.
func (v *issuesView) fold(collapse bool) {
	sec := v.cursorSection()
	if sec < 0 {
		return
	}
	lane := issueSections[sec].lane
	if v.folded[lane] == collapse {
		return
	}
	v.setFold(lane, collapse)
	if collapse {
		v.selected, v.selectedLane = 0, lane
	}
	v.reselect()
}

// foldAll collapses or expands every shown section (C and O).
func (v *issuesView) foldAll(collapse bool) {
	sec := v.cursorSection()
	for i := range v.shownSections() {
		v.setFold(issueSections[i].lane, collapse)
	}
	if collapse && sec >= 0 {
		v.selected, v.selectedLane = 0, issueSections[sec].lane
	}
	v.reselect()
}

func (v *issuesView) setFold(lane string, collapse bool) {
	if collapse {
		v.folded[lane] = true
	} else {
		delete(v.folded, lane)
	}
}
