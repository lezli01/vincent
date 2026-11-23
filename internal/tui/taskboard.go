package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
	"github.com/lezli01/vincent/internal/reasons"
)

// The task board (task 150.6): one column for each thing a task can need from
// you, each column grouped by issue, one card per task. The archived board
// keeps the table until task 150.7, so the card board is a mode of the board
// model rather than a second model: selection, marks, the action bar and the
// filter are all the board's own.

const (
	// taskCardH is a task card's outer height.
	taskCardH = 7
	// taskLaneMinW is the narrowest column a task card stays readable in.
	taskLaneMinW = 30
	// taskPreviewMinW is the narrowest terminal that gets a preview.
	taskPreviewMinW = 170
	// taskPreviewMaxW is the widest the preview grows.
	taskPreviewMaxW = 50
	// taskPreviewDebounce is how long the selection must rest before the
	// task's steps are read.
	taskPreviewDebounce = 150 * time.Millisecond
	// taskDotsMax caps a card's step dots; longer workflows show the count.
	taskDotsMax = 8
)

// The board's columns, in the order they are drawn.
const (
	taskColumnNeedsYou = iota
	taskColumnRunning
	taskColumnQueued
	taskColumnFinished
	taskColumnCount
)

// taskColumnLook is how one column is drawn.
type taskColumnLook struct {
	glyph, name string
	tone        lipgloss.Style
}

var taskColumnLooks = [taskColumnCount]taskColumnLook{
	taskColumnNeedsYou: {attentionBadge, "Needs you", styleWarn},
	taskColumnRunning:  {"▶", "Running", toneCyan},
	taskColumnQueued:   {"◷", "Queued", toneBlue},
	taskColumnFinished: {"✓", "Finished", styleOK},
}

// taskColumnOf is the column a task's state belongs in.
func taskColumnOf(state string) int {
	switch {
	case needsAttention(state):
		return taskColumnNeedsYou
	case state == stateRunning || state == stateAwaitingChildren:
		return taskColumnRunning
	case state == stateQueued || state == statePaused:
		return taskColumnQueued
	}
	return taskColumnFinished
}

// taskColumnOfTask is the column a task's card is drawn in: its state's,
// except that a fan-out parent one of whose lanes waits on a human is in
// Needs you (#1067). Folded, its lanes are not on the board, and the parent
// is the only card that can say so.
func taskColumnOfTask(t apiclient.Task) int {
	if keepsAttention(t) {
		return taskColumnNeedsYou
	}
	return taskColumnOf(t.State)
}

// taskCardGroup is one issue's tasks inside one column.
type taskCardGroup struct {
	key    string
	issue  *apiclient.TaskIssue
	tasks  []apiclient.Task
	folded bool
}

// taskStop is somewhere the cursor can rest: a task, or a folded group standing
// in for the tasks it hides.
type taskStop struct {
	id     int64
	header bool
	column int
	key    string
}

// taskCardHit is where the last render drew a card or a group's header.
type taskCardHit struct {
	x0, x1, y0, y1 int
	stop           taskStop
}

// cardGroups is the board as columns of issue groups, in the board's own sort
// order, with the tasks that belong to no issue last in every column.
func (b *board) cardGroups() [taskColumnCount][]taskCardGroup {
	tasks := slices.Clone(b.shownTasks())
	sortTasks(tasks)
	var byColumn [taskColumnCount][]apiclient.Task
	for _, t := range tasks {
		c := taskColumnOfTask(t)
		byColumn[c] = append(byColumn[c], t)
	}
	var out [taskColumnCount][]taskCardGroup
	for c := range byColumn {
		var free *taskCardGroup
		for _, g := range partitionTasks(byColumn[c], groupIssue) {
			group := taskCardGroup{key: g.key, issue: g.tasks[0].Issue, tasks: g.tasks, folded: b.cardFolded(c, g.key)}
			if g.key == groupNoIssue {
				free = &group
				continue
			}
			out[c] = append(out[c], group)
		}
		if free != nil {
			out[c] = append(out[c], *free)
		}
	}
	return out
}

// cardFoldKey names one group in one column: an issue can be folded in Finished
// and open in Running.
func cardFoldKey(column int, key string) string { return strconv.Itoa(column) + "|" + key }

// cardFolded reports whether a group is folded. Finished starts folded, so
// finished work takes little room until someone asks for it.
func (b *board) cardFolded(column int, key string) bool {
	if v, ok := b.cardFolds[cardFoldKey(column, key)]; ok {
		return v
	}
	return column == taskColumnFinished && !b.archived
}

// frameless reports that the archive's cards draw their own panes.
func (b *board) frameless() bool { return b.archived && b.cards }

func (b *board) setCardFold(column int, key string, folded bool) {
	if b.cardFolds == nil {
		b.cardFolds = map[string]bool{}
	}
	b.cardFolds[cardFoldKey(column, key)] = folded
}

// cardStops is where the cursor can rest in each column: every card, and
// under an unfolded fan-out parent every lane box (boardcardlanes.go).
func (b *board) cardStops(groups [taskColumnCount][]taskCardGroup) [taskColumnCount][]taskStop {
	var out [taskColumnCount][]taskStop
	for c := range groups {
		for _, g := range groups[c] {
			if g.folded {
				out[c] = append(out[c], taskStop{header: true, column: c, key: g.key})
				continue
			}
			for _, t := range g.tasks {
				out[c] = append(out[c], taskStop{id: t.ID, column: c, key: g.key})
				if b.archived {
					continue // the archive's grid draws no lanes
				}
				for _, e := range b.cardLaneEntries(t) {
					out[c] = append(out[c], taskStop{id: e.lane.ID, column: c, key: g.key})
				}
			}
		}
	}
	return out
}

// cardCursor is where the cursor is: the remembered stop while it still exists,
// the fold that swallowed the remembered task, or the board's first stop.
func (b *board) cardCursor(stops [taskColumnCount][]taskStop, groups [taskColumnCount][]taskCardGroup) (column, pos int, ok bool) {
	if h := b.cardHeader; h != nil {
		for i, s := range stops[h.column] {
			if s.header && s.key == h.key {
				return h.column, i, true
			}
		}
	}
	for c := range stops {
		for i, s := range stops[c] {
			if !s.header && s.id == b.selectedID && b.selectedID != 0 {
				// A card that moved to another column on its own — an
				// approve that sent it to Running — leaves the cursor in
				// the column it was in (#1058).
				if b.cardPlaced && !b.cardFollow && c != b.cardCol && len(stops[b.cardCol]) > 0 {
					return b.restNear(stops)
				}
				b.cardCol, b.cardPos, b.cardPlaced, b.cardFollow = c, i, true, false
				return c, i, true
			}
		}
	}
	for c := range groups {
		for _, g := range groups[c] {
			if !g.folded || !slices.ContainsFunc(g.tasks, func(t apiclient.Task) bool { return t.ID == b.selectedID }) {
				continue
			}
			for i, s := range stops[c] {
				if s.header && s.key == g.key {
					return c, i, true
				}
			}
		}
	}
	// The card is gone — archived, or filtered out: the nearest card of the
	// column it was in, not the board's first (#1058).
	if b.cardPlaced && len(stops[b.cardCol]) > 0 {
		return b.restNear(stops)
	}
	for c := range stops {
		if len(stops[c]) > 0 {
			return c, 0, true
		}
	}
	return 0, 0, false
}

// restNear rests the cursor on the stop nearest where it last was, in the
// column it was in, and makes that the selection.
func (b *board) restNear(stops [taskColumnCount][]taskStop) (column, pos int, ok bool) {
	c := b.cardCol
	i := min(b.cardPos, len(stops[c])-1)
	s := stops[c][i]
	if s.header {
		stop := s
		b.cardHeader = &stop
	} else {
		b.cardHeader = nil
		b.selectedID = s.id
	}
	b.cardPos, b.cardFollow = i, false
	return c, i, true
}

// cardStop is the stop under the cursor.
func (b *board) cardStop() (taskStop, bool) {
	groups := b.cardGroups()
	stops := b.cardStops(groups)
	c, i, ok := b.cardCursor(stops, groups)
	if !ok {
		return taskStop{}, false
	}
	return stops[c][i], true
}

// cardSelected is board.selected on the card board. A folded group is a stop but
// not a task, so the action keys do nothing there.
func (b *board) cardSelected() (int64, bool) {
	s, ok := b.cardStop()
	if !ok || s.header {
		return 0, false
	}
	return s.id, true
}

// restOn moves the cursor to a stop.
func (b *board) restOn(s taskStop) {
	if s.header {
		stop := s
		b.cardHeader = &stop
		return
	}
	b.cardHeader = nil
	b.selectedID = s.id
	b.cardCol, b.cardPlaced, b.cardFollow = s.column, true, false
}

// moveCard moves the cursor delta stops within its column.
func (b *board) moveCard(delta int) {
	if b.archived && b.cardPerRow > 1 {
		b.stepArchive(delta, 0)
		return
	}
	groups := b.cardGroups()
	stops := b.cardStops(groups)
	c, i, ok := b.cardCursor(stops, groups)
	if ok {
		b.restOn(stops[c][min(max(i+delta, 0), len(stops[c])-1)])
	}
}

// moveCardEdge moves the cursor to the top or the bottom of its column.
func (b *board) moveCardEdge(last bool) {
	groups := b.cardGroups()
	stops := b.cardStops(groups)
	c, _, ok := b.cardCursor(stops, groups)
	if !ok {
		return
	}
	if last {
		b.restOn(stops[c][len(stops[c])-1])
		return
	}
	b.restOn(stops[c][0])
}

// moveColumn moves the cursor to the nearest column in delta's direction with
// anywhere to rest, at the same height or the last stop above it.
func (b *board) moveColumn(delta int) {
	if b.archived && b.cardPerRow > 1 {
		b.stepArchive(0, delta)
		return
	}
	groups := b.cardGroups()
	stops := b.cardStops(groups)
	c, i, ok := b.cardCursor(stops, groups)
	if !ok {
		return
	}
	for n := c + delta; n >= 0 && n < taskColumnCount; n += delta {
		if len(stops[n]) > 0 {
			b.restOn(stops[n][min(i, len(stops[n])-1)])
			return
		}
	}
}

// toggleCardFold folds the group under the cursor, or opens the one it rests on.
func (b *board) toggleCardFold() {
	s, ok := b.cardStop()
	if !ok {
		return
	}
	folded := b.cardFolded(s.column, s.key)
	b.setCardFold(s.column, s.key, !folded)
	if folded {
		b.cardHeader = nil
		return
	}
	b.restOn(taskStop{header: true, column: s.column, key: s.key})
}

// foldAllCards folds or opens every group on the board.
func (b *board) foldAllCards(folded bool) {
	groups := b.cardGroups()
	for c := range groups {
		for _, g := range groups[c] {
			b.setCardFold(c, g.key, folded)
		}
	}
	if !folded {
		b.cardHeader = nil
	}
}

// expandCardFor opens the group hiding a task: a fold is never the reason work
// waiting on a human cannot be reached (task 054 decision 3).
func (b *board) expandCardFor(id int64) bool {
	// The event that calls this can arrive before the listing has moved the
	// task, so the group it is about to join opens as well as the one it is in.
	if t, ok := b.taskByID(id); ok {
		key := groupValue(t, groupIssue)
		if b.cardFolded(taskColumnNeedsYou, key) {
			b.setCardFold(taskColumnNeedsYou, key, false)
			b.cardHeader = nil
			return true
		}
	}
	groups := b.cardGroups()
	for c := range groups {
		for _, g := range groups[c] {
			if g.folded && slices.ContainsFunc(g.tasks, func(t apiclient.Task) bool { return t.ID == id }) {
				b.setCardFold(c, g.key, false)
				b.cardHeader = nil
				return true
			}
		}
	}
	return false
}

// cardKey is the card board's own keys; handled is false for anything else.
func (b *board) cardKey(key string) bool {
	switch key {
	case "up", "k":
		b.moveCard(-1)
	case "down", "j":
		b.moveCard(1)
	case "pgup":
		b.moveCard(-max(b.cardPage, 1))
	case "pgdown":
		b.moveCard(max(b.cardPage, 1))
	case "home":
		b.moveCardEdge(false)
	case "end":
		b.moveCardEdge(true)
	case "left":
		b.moveColumn(-1)
	case "right":
		b.moveColumn(1)
	case "f":
		b.toggleCardFold()
	case "C":
		b.foldAllCards(true)
	case "O":
		b.foldAllCards(false)
	case opKey(keymap.OpenRow):
		// A task's enter belongs to whoever opens tasks; a folded group's is here.
		s, ok := b.cardStop()
		if !ok || !s.header {
			return false
		}
		b.toggleCardFold()
	default:
		return false
	}
	return true
}

// renderCards draws the board in exactly width×height.
func (b *board) renderCards(width, height int) string {
	b.width, b.height = width, height
	b.previewOn = width >= taskPreviewMinW
	lines := b.cardFilterBox(width)
	lines = append(lines, b.cardNotices(width)...)
	room := height - len(lines)
	b.cardHits = nil
	if room < 3 {
		return strings.Join(lines, "\n")
	}
	if body, ok := b.emptyBody(b.rowsForEmpty()); ok {
		text := strings.TrimSpace(ansi.Strip(body))
		box := drawBox(toneBlue.Bold(true).Render(ic("▶ Tasks")), "", styled(wrapText(text, max(width-4, 1), wrapOpts{}), styleDim), width, room, edgesRounded, styleDim)
		return strings.Join(append(lines, box...), "\n")
	}
	boardW := width
	var pane []string
	if b.previewOn {
		w := min(width*26/100, taskPreviewMaxW)
		boardW = width - w
		pane = b.taskPreviewPane(w, room)
	}
	out := b.cardColumns(boardW, room, len(lines))
	if pane != nil {
		out = joinColumns(out, pane)
	}
	return strings.Join(append(lines, out...), "\n")
}

// rowsForEmpty stands in for the table's rows when asking whether the board has
// nothing to show: one row per shown task is all emptyBody reads.
func (b *board) rowsForEmpty() []boardRow {
	tasks := b.shownTasks()
	out := make([]boardRow, len(tasks))
	for i := range tasks {
		out[i] = boardRow{task: tasks[i]}
	}
	return out
}

// cardFilterBox is the rounded box above the columns.
func (b *board) cardFilterBox(width int) []string {
	body := []string{toneBlue.Render(ic("⌕ ")) + styleDim.Render(b.filter.Placeholder())}
	if b.filtering || b.filter.Value() != "" {
		b.filter.SetWidth(max(width-6, 10))
		body = fieldRows("", b.filter)
	}
	agents := ""
	if b.infoOK {
		agents = b.agentsSummary()
	}
	if b.archived {
		agents = archiveSwitches(true, b.window)
	}
	return drawBox(agents, b.cardSummary(), body, width, len(body)+2, edgesRounded, styleDim)
}

// cardSummary says what the board is showing, for the filter box's edge.
func (b *board) cardSummary() string {
	if b.archived {
		return styleDim.Render(b.pageCount(plural(len(b.tasks), "task", "tasks")))
	}
	tasks := b.projectTasks()
	parts := []string{plural(len(tasks), "task", "tasks"), strconv.Itoa(countRunning(tasks)) + " running"}
	if b.infoOK && b.info.MaxParallelTasks > 0 {
		parts = append(parts, "cap "+strconv.Itoa(b.info.MaxParallelTasks))
	}
	if b.attentionOnly {
		parts = append(parts, "needs you only")
	}
	if !b.lastLoad.IsZero() {
		parts = append(parts, "updated "+b.lastLoad.Format("15:04:05"))
	}
	out := styleDim.Render(strings.Join(parts, " · "))
	if n := b.attentionTally().here; n > 0 {
		clause := fmt.Sprintf("%s %d need attention", attentionBadge, n)
		if b.filter.Value() != "" {
			clause += " (all tasks)"
		}
		out = styleAsk.Render(clause) + styleDim.Render(" · ") + out
	}
	if n := len(b.projectMarks()); n > 0 {
		out = styleFocus.Render(strconv.Itoa(n)+" selected") + styleDim.Render(" · ") + out
	}
	return out
}

// cardNotices is what must be said above the columns: a stale board.
func (b *board) cardNotices(width int) []string {
	var out []string
	if b.archived {
		out = b.archivedStatus()
	}
	if b.loadErr != nil {
		out = append(out, styleBad.Render(ansi.Truncate(" ⚠ "+b.staleNote(), width, "…")))
	}
	return out
}

// archiveSwitch is the archive's Tasks and Chats, the one on screen highlighted.
func archiveSwitch(tasks bool) string {
	on, off := styleTabActive, styleDim
	if tasks {
		return on.Render(ic(" ▶ Tasks ")) + " " + off.Render(ic(" ◌ Chats "))
	}
	return off.Render(ic(" ▶ Tasks ")) + " " + on.Render(ic(" ◌ Chats "))
}

// cardColumns draws the columns that fit, the cursor's always among them.
func (b *board) cardColumns(width, height, top int) []string {
	groups := b.cardGroups()
	stops := b.cardStops(groups)
	cursor, pos, ok := b.cardCursor(stops, groups)
	var at taskStop
	if ok {
		at = stops[cursor][pos]
	}
	if b.archived {
		// An archive is one column: everything in it has finished.
		return b.cardColumn(taskColumnFinished, groups[taskColumnFinished], at, at.key, 0, top, width, height, issueLaneMore{})
	}
	// The selected task's issue is lit in every column it has work in.
	litIssue := at.key
	fit := min(max(width/taskLaneMinW, 1), taskColumnCount)
	first := max(min(cursor-(fit-1)/2, taskColumnCount-fit), 0)
	laneW := width / fit
	out := make([]string, height)
	x := 0
	for c := first; c < first+fit; c++ {
		w := laneW
		if c == first+fit-1 {
			w = width - x
		}
		more := issueLaneMore{left: c == first && first > 0, right: c == first+fit-1 && first+fit < taskColumnCount}
		column := b.cardColumn(c, groups[c], at, litIssue, x, top, w, height, more)
		for y := range out {
			if y < len(column) {
				out[y] += column[y]
			}
		}
		x += w
	}
	return out
}

// cardColumn draws one column, scrolled to keep the cursor on screen.
func (b *board) cardColumn(c int, groups []taskCardGroup, at taskStop, lit string, x, top, w, h int, more issueLaneMore) []string {
	look := taskColumnLooks[c]
	title := look.tone.Bold(true).Render(ic(look.glyph) + " " + look.name)
	if b.archived {
		title = toneBlue.Bold(true).Render(ic("▣ Archived tasks"))
	}
	if more.left {
		title = styleDim.Render("‹ ") + title
	}
	total := 0
	for _, g := range groups {
		total += len(g.tasks)
	}
	right := look.tone.Render(strconv.Itoa(total))
	if more.right {
		right += styleDim.Render(" ›")
	}
	inner := max(w-4, 1)
	var body []string
	type span struct {
		from, h int
		stop    taskStop
		// x0 and x1 are the card's cells across the column; a zero x1 is all of it.
		x0, x1 int
	}
	perRow := 1
	if b.archived {
		perRow = archivePerRow(inner)
		b.cardPerRow = perRow
	}
	cardW := (inner - 2) / perRow
	var spans []span
	from, to := 0, 0
	rail := styleDim.Render("│ ")
	for i, g := range groups {
		if i > 0 {
			body = append(body, "")
		}
		stop := taskStop{header: true, column: c, key: g.key}
		selected := g.folded && at.header && at.column == c && at.key == g.key
		if selected {
			from, to = len(body), len(body)+2
		}
		if g.folded {
			spans = append(spans, span{from: len(body), h: 2, stop: stop})
		} else {
			// An open header folds on a click too (#1065).
			spans = append(spans, span{from: len(body), h: 1, stop: stop})
		}
		body = append(body, cardGroupHeader(g, selected, g.key == lit && lit != groupNoIssue, inner))
		if g.folded {
			summary := styleDim.Render(cardFoldSummary(g.tasks))
			// What a selection holds behind the fold (#1066): `V` marks the
			// tasks a filter shows, folded or not, and A then acts on them.
			if n := b.markedIn(g.tasks); n > 0 {
				summary += styleDim.Render(" · ") + styleFocus.Render(ic("✔ ")+strconv.Itoa(n)+" selected")
			}
			body = append(body, rail+ansi.Truncate(summary, max(inner-ansi.StringWidth(rail), 1), "…"))
			continue
		}
		for r := 0; r < len(g.tasks); r += perRow {
			row := make([]string, taskCardH)
			for k, t := range g.tasks[r:min(r+perRow, len(g.tasks))] {
				chosen := !at.header && t.ID == at.id
				if chosen {
					from, to = len(body), len(body)+taskCardH
				}
				s := span{from: len(body), h: taskCardH, stop: taskStop{id: t.ID, column: c, key: g.key}}
				if perRow > 1 {
					s.x0, s.x1 = 2+k*cardW, 2+(k+1)*cardW
				}
				spans = append(spans, s)
				row = joinColumns(row, b.taskCard(t, chosen, cardW))
			}
			for _, line := range row {
				body = append(body, rail+line)
			}
			if perRow > 1 {
				continue // the archive's grid draws no lanes
			}
			for _, e := range b.cardLaneEntries(g.tasks[r]) {
				chosen := !at.header && e.lane.ID == at.id
				if chosen {
					from, to = len(body), len(body)+laneCardH
				}
				indent := laneIndent(e.depth)
				lw := laneCardWidth(cardW, e.depth)
				spans = append(spans, span{
					from: len(body), h: laneCardH, stop: taskStop{id: e.lane.ID, column: c, key: g.key},
					x0: 2 + len(indent), x1: 2 + len(indent) + lw,
				})
				for _, line := range b.laneCard(e.lane, chosen, lw) {
					body = append(body, rail+indent+line)
				}
			}
		}
	}
	if len(groups) == 0 {
		body = append(body, styleDim.Render("no tasks"))
	}
	room := max(h-2, 1)
	if at.column == c {
		b.cardPage = max(room/taskCardH, 1)
	}
	start := 0
	if len(body) > room {
		start = min(max(to-room, 0), len(body)-room)
		start = min(start, from)
	}
	for _, s := range spans {
		// What of the card is on screen takes the click, a card the column
		// edge cuts included (#1065).
		vis0, vis1 := max(s.from, start), min(s.from+s.h, start+room)
		if vis0 >= vis1 {
			continue
		}
		y0, y1 := top+1+vis0-start, top+1+vis1-start
		x0, x1 := x+2, x+2+inner
		if s.x1 > 0 {
			x0, x1 = x+2+s.x0, x+2+s.x1
		}
		b.cardHits = append(b.cardHits, taskCardHit{x0, x1, y0, y1, s.stop})
	}
	return drawBox(title, right, body[start:], w, h, edgesRounded, styleDim)
}

// cardGroupHeader is an issue's line in a column.
func cardGroupHeader(g taskCardGroup, selected, lit bool, w int) string {
	fold := "▾ "
	if g.folded {
		fold = "▸ "
	}
	mark, name := ic("○ "), "No issue"
	style, markStyle := styleDim, styleDim
	if g.issue != nil {
		mark = ic("▦ #") + strconv.FormatInt(g.issue.ID, 10) + " "
		name = oneLine(g.issue.Title)
		if strings.TrimSpace(g.issue.State) == issueStateClosed {
			name += " · closed"
		}
		style, markStyle = lipgloss.NewStyle(), toneCyan
	}
	if lit {
		style, markStyle = toneBlue.Bold(true), toneBlue.Bold(true)
	}
	if selected {
		style, markStyle = styleFocus.Bold(true).Reverse(true), styleFocus.Bold(true).Reverse(true)
	}
	count := strconv.Itoa(len(g.tasks))
	room := max(w-ansi.StringWidth(fold+mark)-ansi.StringWidth(count)-1, 1)
	return padBetweenCells(styleDim.Render(fold)+markStyle.Render(mark)+style.Render(ansi.Truncate(name, room, "…")), styleDim.Render(count), w)
}

// cardFoldSummary says what a folded group holds.
func cardFoldSummary(tasks []apiclient.Task) string {
	counts := map[string]int{}
	var order []string
	for _, t := range tasks {
		word := stateWords(t.State)
		if counts[word] == 0 {
			order = append(order, word)
		}
		counts[word]++
	}
	parts := make([]string, 0, len(order))
	for _, word := range order {
		parts = append(parts, strconv.Itoa(counts[word])+" "+word)
	}
	return strings.Join(parts, "  ")
}

// taskCard draws one task.
func (b *board) taskCard(t apiclient.Task, selected bool, w int) []string {
	inner := max(w-4, 1)
	titleStyle := lipgloss.NewStyle()
	edges, border := edgesRounded, styleDim
	if selected {
		titleStyle, edges, border = styleTitle, edgesThick, styleFocus
	}
	head := styleKey.Render("#" + strconv.FormatInt(t.ID, 10))
	if label := b.laneCountLabel(t); label != "" {
		// A fan-out parent's disclosure: `L` hangs its lanes under the card.
		head += " " + toneCyan.Render(label)
	}
	if b.marks.has(t.ID) {
		head = styleFocus.Render(ic("✔ ")) + head
	}
	if t.Workflow != "" {
		head += "  " + toneMagenta.Render(ic("⟡ ")+oneLine(t.Workflow))
	}
	age := styleDim.Render(issueAge(b.now(), t.UpdatedAt))
	if room := inner - ansi.StringWidth(age) - 1; ansi.StringWidth(head) > room {
		head = ansi.Truncate(head, max(room, 1), "…")
	}
	body := []string{padBetweenCells(head, age, inner)}
	title := wrapText(valueOr(t.Title, "untitled task"), inner, wrapOpts{maxLines: 2})
	for i := range 2 {
		line := ""
		if i < len(title) {
			line = titleStyle.Render(title[i])
		}
		body = append(body, line)
	}
	body = append(body, padBetweenCells(taskCardProgress(t), toneCyan.Render(ic(formatPR(t.GitHubPull))), inner))
	status, style := taskCardStatus(t, b.now())
	body = append(body, style.Render(status))
	return drawBox("", "", body, w, taskCardH, edges, border)
}

// taskCardProgress is the step dots and where the task has got to.
func taskCardProgress(t apiclient.Task) string {
	if t.StepTotal <= 0 {
		return ""
	}
	current := min(t.CurrentStep+1, t.StepTotal)
	label := strconv.Itoa(current) + "/" + strconv.Itoa(t.StepTotal)
	if t.StepName != "" {
		label += " " + oneLine(t.StepName)
	}
	if t.StepTotal > taskDotsMax {
		return styleDim.Render(label)
	}
	return taskStepDots(t) + "  " + styleDim.Render(label)
}

// taskStepDots is one glyph per step, so progress survives NO_COLOR.
func taskStepDots(t apiclient.Task) string {
	var b strings.Builder
	for i := range t.StepTotal {
		switch {
		case i < t.CurrentStep || t.State == stateDone:
			b.WriteString(styleOK.Render(ic("✓")))
		case i > t.CurrentStep:
			b.WriteString(styleDim.Render(ic("○")))
		case t.State == stateBlocked || t.State == stateAborted:
			b.WriteString(styleBad.Render(ic("✗")))
		case t.State == stateQueued:
			b.WriteString(styleDim.Render(ic("○")))
		default:
			b.WriteString(styleOK.Render(ic("●")))
		}
	}
	return b.String()
}

// taskCardStatus is a card's last line, led by a glyph.
func taskCardStatus(t apiclient.Task, now time.Time) (string, lipgloss.Style) {
	switch t.State {
	case stateBlocked:
		reason := "blocked"
		if t.BlockReason != nil && *t.BlockReason != "" {
			reason = reasons.Explain(*t.BlockReason).Title
		}
		return attentionBadge + " " + reason, styleBad
	case stateAwaitingInput:
		return attentionBadge + " has a question", styleWarn
	case stateAwaitingGate:
		return attentionBadge + " needs your approval", styleWarn
	case stateAwaitingChildren:
		if needs := lanesNeedYou(t); needs.any() {
			// What the lanes need leads, in the attention colour, with the
			// key that hangs the lanes under the card beside it, then the
			// done count, which is what a narrow card cuts first (#1067).
			line := needs.text() + " (L)"
			if c := t.Children; c != nil {
				line += fmt.Sprintf(" · %d of %d done", c.Settled, c.Total)
			}
			style := styleWarn
			if needs.blocked > 0 {
				style = styleBad
			}
			return line, style
		}
		if c := t.Children; c != nil {
			return fmt.Sprintf(ic("⑂ %d of %d lanes done"), c.Settled, c.Total), toneMagenta
		}
		return ic("⑂ waiting on its lanes"), toneMagenta
	case stateRunning:
		if t.StatusMessage != nil && strings.TrimSpace(*t.StatusMessage) != "" {
			return ic("▶ ") + oneLine(*t.StatusMessage), toneCyan
		}
		if d, ok := t.Elapsed(now); ok {
			return ic("▶ running · ") + formatElapsed(d), toneCyan
		}
		return ic("▶ running"), toneCyan
	case stateQueued:
		if t.AdmitNotBefore != nil {
			return ic("◷ ") + boardStateLabel(t), styleWarn
		}
		return ic("◷ waiting for a slot"), styleDim
	case statePaused:
		return ic("⏸ paused"), styleWarn
	case stateDone:
		if t.CostUSD != nil {
			return ic("✓ done · ") + formatCost(t.CostUSD), styleOK
		}
		return ic("✓ done"), styleOK
	case stateAborted:
		return ic("✗ ") + stateWords(t.State), styleDim
	}
	return "· " + stateWords(t.State), styleDim
}

// The preview: the selected task's steps, its issue across the board, and what
// can be done about it.

// taskPreview is one read of a task's steps.
type taskPreview struct {
	updated time.Time
	steps   []apiclient.StepRun
	err     string
}

// taskPreviewKey names what a read is for: a task as of a change.
type taskPreviewKey struct {
	id      int64
	updated int64
}

type (
	// taskPreviewDueMsg says the selection may have rested on key long enough.
	taskPreviewDueMsg struct{ key taskPreviewKey }
	// taskPreviewMsg is the task read for key.
	taskPreviewMsg struct {
		key     taskPreviewKey
		updated time.Time
		steps   []apiclient.StepRun
		err     error
	}
)

// previewCmd arms the read of the selected task's steps.
func (b *board) previewCmd() tea.Cmd {
	if !b.cards || !b.previewOn || b.client == nil {
		return nil
	}
	id, ok := b.cardSelected()
	if !ok {
		return nil
	}
	t, ok := b.taskByID(id)
	if !ok {
		return nil
	}
	key := taskPreviewKey{id, t.UpdatedAt.UnixNano()}
	if p, held := b.previews[id]; (held && p.updated.Equal(t.UpdatedAt)) || b.previewArmed == key {
		return nil
	}
	b.previewArmed = key
	return tea.Tick(taskPreviewDebounce, func(time.Time) tea.Msg { return taskPreviewDueMsg{key} })
}

// previewDue reads the task if the selection is still where it was.
func (b *board) previewDue(key taskPreviewKey) tea.Cmd {
	id, ok := b.cardSelected()
	if !ok || id != key.id || b.previewReading == key || b.client == nil {
		return nil
	}
	t, ok := b.taskByID(id)
	if !ok || t.UpdatedAt.UnixNano() != key.updated {
		return nil
	}
	client, updated := b.client, t.UpdatedAt
	b.previewReading = key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		detail, err := client.GetTask(ctx, key.id)
		return taskPreviewMsg{key: key, updated: updated, steps: detail.Steps, err: err}
	}
}

// applyPreview keeps a read's answer; a failure is kept until the task changes.
func (b *board) applyPreview(msg taskPreviewMsg) {
	if b.previewReading == msg.key {
		b.previewReading = taskPreviewKey{}
	}
	if b.previewArmed == msg.key {
		b.previewArmed = taskPreviewKey{}
	}
	if b.previews == nil {
		b.previews = map[int64]taskPreview{}
	}
	p := taskPreview{updated: msg.updated, steps: msg.steps}
	if msg.err != nil {
		p.err = errString(msg.err)
	}
	b.previews[msg.key.id] = p
}

// taskPreviewPane is the selected task in more detail.
func (b *board) taskPreviewPane(w, h int) []string {
	id, ok := b.cardSelected()
	t, known := b.taskByID(id)
	if !ok || !known {
		body := []string{styleDim.Render("Select a task to preview it.")}
		return drawBox(styleTitle.Render("Preview"), "", body, w, h, edgesRounded, styleDim)
	}
	inner := max(w-4, 1)
	body := styled(wrapText(valueOr(t.Title, "untitled task"), inner, wrapOpts{maxLines: 3}), styleTitle)
	status, style := taskCardStatus(t, b.now())
	body = append(body, "", taskStatePill(t.State), style.Render(ansi.Truncate(status, inner, "…")), "")
	if meta := taskPreviewMeta(t, b.now()); meta != "" {
		body = append(body, styleDim.Render(ansi.Truncate(meta, inner, "…")), "")
	}
	body = append(body, styleTitle.Render(ic("▤ Steps")))
	body = append(body, b.previewSteps(t, inner)...)
	if t.Issue != nil {
		body = append(body, "", toneBlue.Bold(true).Render(ic("▦ Issue #")+strconv.FormatInt(t.Issue.ID, 10))+" "+
			ansi.Truncate(oneLine(t.Issue.Title), max(inner-ansi.StringWidth(ic("▦ Issue #")+strconv.FormatInt(t.Issue.ID, 10))-1, 1), "…"))
		body = append(body, b.issueTally(t.Issue.ID, inner))
	}
	if actions := taskPreviewActions(t, inner); len(actions) > 0 {
		body = append(append(body, "", styleTitle.Render(ic("⚡ What you can do"))), actions...)
	}
	return drawBox(styleKey.Render("#"+strconv.FormatInt(t.ID, 10)), "", body, w, h, edgesRounded, styleDim)
}

// taskStatePill is a task's state as a filled label.
func taskStatePill(state string) string {
	style := styleDim
	switch taskColumnOf(state) {
	case taskColumnNeedsYou:
		style = styleWarn
	case taskColumnRunning:
		style = toneCyan
	case taskColumnFinished:
		style = styleOK
	}
	if state == stateBlocked {
		style = styleBad
	}
	return style.Bold(true).Reverse(true).Render(" " + stateWords(state) + " ")
}

// taskPreviewMeta is the workflow and when the task started.
func taskPreviewMeta(t apiclient.Task, now time.Time) string {
	var parts []string
	if t.Workflow != "" {
		parts = append(parts, oneLine(t.Workflow))
	}
	if t.StartedAt != nil {
		if age := strings.TrimPrefix(issueAge(now, *t.StartedAt), ic("◷ ")); age == "now" {
			parts = append(parts, "started just now")
		} else if age != "" {
			parts = append(parts, "started "+age+" ago")
		}
	}
	return strings.Join(parts, " · ")
}

// previewSteps is each step's latest attempt.
func (b *board) previewSteps(t apiclient.Task, inner int) []string {
	p, held := b.previews[t.ID]
	switch {
	case !held:
		return []string{styleDim.Render("Loading…")}
	case p.err != "":
		return styled(wrapText("Could not read the steps: "+p.err, inner, wrapOpts{maxLines: 2}), styleWarn)
	case len(p.steps) == 0:
		return []string{styleDim.Render("No step has run yet.")}
	}
	latest := map[int]apiclient.StepRun{}
	var order []int
	for _, r := range p.steps {
		cur, seen := latest[r.StepIndex]
		if !seen {
			order = append(order, r.StepIndex)
		}
		if !seen || r.ID > cur.ID {
			latest[r.StepIndex] = r
		}
	}
	slices.Sort(order)
	out := make([]string, 0, len(order))
	for _, i := range order {
		r := latest[i]
		glyph, style := taskStepGlyph(r.State)
		line := style.Render(glyph+" ") + strconv.Itoa(i+1) + " " + oneLine(stepLabel(r))
		if r.Attempt > 1 {
			line += styleDim.Render("  attempt " + strconv.Itoa(r.Attempt))
		}
		out = append(out, ansi.Truncate(line, inner, "…"))
	}
	return out
}

// taskStepGlyph is an attempt's outcome as a glyph.
func taskStepGlyph(state string) (string, lipgloss.Style) {
	switch state {
	case "succeeded":
		return ic("✓"), styleOK
	case "failed", "timed_out", "interrupted":
		return ic("✗"), styleBad
	case "skipped", stepStateStopped:
		return "–", styleDim
	case "running":
		return ic("●"), toneCyan
	}
	return ic("○"), styleDim
}

// issueTally counts an issue's tasks across the columns.
func (b *board) issueTally(issue int64, inner int) string {
	var counts [taskColumnCount]int
	for _, t := range b.projectTasks() {
		if t.Issue != nil && t.Issue.ID == issue {
			counts[taskColumnOfTask(t)]++
		}
	}
	words := [taskColumnCount]string{"need you", "running", "queued", "finished"}
	var parts []string
	for c, n := range counts {
		if n > 0 {
			look := taskColumnLooks[c]
			parts = append(parts, look.tone.Render(ic(look.glyph)+" "+strconv.Itoa(n)+" "+words[c]))
		}
	}
	return ansi.Truncate(strings.Join(parts, "  "), inner, "…")
}

// taskPreviewActions lists the keys that act on this task right now.
func taskPreviewActions(t apiclient.Task, inner int) []string {
	target := taskActions{id: t.ID, state: t.State, actions: t.AvailableActions}
	var parts []string
	for _, o := range footerActionOps {
		key := opKey(o.op)
		if key == "" || !target.has(o.action) {
			continue
		}
		label := o.label
		if label == "" {
			label = actionLabel(target, o.action)
		}
		parts = append(parts, styleKey.Render(key)+" "+label)
	}
	if opKey(keymap.Chat) != "" && target.offersChat() {
		parts = append(parts, styleKey.Render(opKey(keymap.Chat))+" chat")
	}
	var out []string
	line := ""
	for _, p := range parts {
		switch {
		case line == "":
			line = p
		case ansi.StringWidth(line)+3+ansi.StringWidth(p) <= inner:
			line += "   " + p
		default:
			out = append(out, line)
			line = p
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// cardClick is a left click on the board: a card selects, a group's header
// folds. open reports a click on the card that was already selected, which
// opens it as enter does (#1065): the first click picks, the second goes in.
func (b *board) cardClick(x, y int) (open bool) {
	for _, h := range b.cardHits {
		if x < h.x0 || x >= h.x1 || y < h.y0 || y >= h.y1 {
			continue
		}
		cur, selected := b.cardSelected()
		again := !h.stop.header && selected && cur == h.stop.id
		if h.stop.header && !b.cardFolded(h.stop.column, h.stop.key) {
			// An open group's header is not a stop: it folds, and the
			// cursor rests on the folded header (#1065).
			b.setCardFold(h.stop.column, h.stop.key, true)
			b.restOn(h.stop)
			return false
		}
		b.restOn(h.stop)
		if h.stop.header {
			b.toggleCardFold()
		}
		return again
	}
	return false
}

// markedIn is how many of tasks the board's selection holds.
func (b *board) markedIn(tasks []apiclient.Task) int {
	n := 0
	for _, t := range tasks {
		if b.marks.has(t.ID) {
			n++
		}
	}
	return n
}
