package tui

import (
	"context"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Expandable fan-out parents on the task board (§7.6, §15, issue #316).
//
// Task 014 decision 13 keeps lanes out of the task list: a list is the work
// someone asked for, and a 64-task tree buries it. That decision is kept here
// — what changes is that a parent row is now a disclosure control. Pressing
// `L` on one hangs its lanes underneath it as indented task rows, and pressing
// it again takes them away.
//
// The rows stay out of the counts *by construction* rather than by filtering:
// the lanes come from their own `GET /v1/tasks?parent_id=N` (§13.2), one per
// expanded parent, and never enter b.tasks. So the flat count, every group
// header's count and its `! n` attention badge are computed from exactly the
// list they were computed from before, and a board with nothing expanded
// issues exactly the request it issued before and renders exactly the rows it
// rendered before.
//
// The expanded set is **session-only** and deliberately not written to
// {data_dir}/tui.json beside task 054's folds. A fold is a label path, which
// survives a restart meaning what it meant; a task id is not — and 054's rule
// for dropping a path whose project or workflow has left the board (decision
// 4) has no honest counterpart for a task that was archived while the TUI was
// down. Reopening the TUI onto a board of expanded trees whose parents are
// months old is not a view anybody asked to keep.

// laneDepthDefault is the nesting the board composes to before the daemon's
// config arrives, and what it keeps when no daemon is reachable. It matches
// config.Default().FanOut.MaxDepth; boardlanes_test.go holds the two together.
const laneDepthDefault = 3

// boardLanesMsg carries one expanded parent's lanes, in merge order. seq
// orders concurrent fetches per parent the way boardLoadedMsg does for the
// board itself: commands run on their own goroutines, so an older response
// can land after a newer one and must not clobber it (zero = untracked, for
// tests that build the message directly).
type boardLanesMsg struct {
	seq      uint64
	parentID int64
	lanes    []apiclient.Task
	err      error
}

// expandSet is the parents showing their lanes. A slice rather than a map for
// the reason markSet and foldSet are slices: it is a handful of entries, and a
// stable order makes what it feeds deterministic under test.
type expandSet []int64

func (e expandSet) has(id int64) bool { return slices.Contains(e, id) }

// toggle expands a collapsed parent and collapses an expanded one.
func (e expandSet) toggle(id int64) expandSet {
	if i := slices.Index(e, id); i >= 0 {
		out := slices.Delete(slices.Clone(e), i, i+1)
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return append(slices.Clone(e), id)
}

func (e expandSet) without(id int64) expandSet {
	if i := slices.Index(e, id); i < 0 {
		return e
	}
	return e.toggle(id)
}

// laneTree is the board's fan-out state: which parents are open, and the lanes
// each of them last served. It is one field on the board so that everything
// this feature remembers is in one place — and so that "none of it reaches
// tui.json" is a property of a type rather than of a habit.
type laneTree struct {
	expanded expandSet
	// rows is each parent's lanes in merge order, kept across a collapse so
	// re-expanding is instant rather than another round trip.
	rows map[int64][]apiclient.Task
	// seq and applied stamp the per-parent fetches.
	seq     map[int64]uint64
	applied map[int64]uint64
	// maxDepth is `fan_out.max_depth` as the daemon reports it — how many
	// levels of lane one tree may have (§7.6), and therefore how deep the
	// board composes. Zero before the config lands, which reads as the
	// default.
	maxDepth int
}

// depth is the nesting the board renders to.
func (l *laneTree) depth() int {
	if l.maxDepth < 1 {
		return laneDepthDefault
	}
	return l.maxDepth
}

// lanesOf is a parent's lanes as last fetched.
func (l *laneTree) lanesOf(parentID int64) []apiclient.Task { return l.rows[parentID] }

// known reports that the board has been told this task has lanes — because it
// fetched some, or because a parent parked on its join says so. It is what
// decides whether the row wears a disclosure glyph, so it must never be a
// guess: a `▸` on a row with nothing under it is a promise the board cannot
// keep.
func (l *laneTree) known(t apiclient.Task) bool {
	if len(l.rows[t.ID]) > 0 {
		return true
	}
	if t.State == stateAwaitingChildren {
		return true
	}
	// A parent that has already left awaiting_children carries no rollup on a
	// list row — `children` is served on the detail endpoint only (§13.2) —
	// so the block reason is the one thing a list row says about a fan-out
	// that failed. These four are raised by nothing else (§7.6): the join
	// (`lane_failed`), the merge (`merge_conflict`) and the derive
	// (`fan_out_limit`, `fan_out_invalid`).
	if t.BlockReason == nil {
		return false
	}
	switch *t.BlockReason {
	case reasonLaneFailed, reasonMergeConflict, reasonFanOutLimit, reasonFanOutInvalid:
		return true
	default:
		return false
	}
}

// apply installs a lane fetch.
func (l *laneTree) apply(msg boardLanesMsg) {
	if msg.err != nil {
		// Keep whatever is already hanging under the parent, for the reason
		// updateLoaded keeps the board's rows: a failed refresh is not news
		// that the lanes went away.
		return
	}
	if msg.seq != 0 && msg.seq <= l.applied[msg.parentID] {
		return
	}
	if l.rows == nil {
		l.rows = make(map[int64][]apiclient.Task, 2)
	}
	if l.applied == nil {
		l.applied = make(map[int64]uint64, 2)
	}
	l.rows[msg.parentID] = msg.lanes
	l.applied[msg.parentID] = msg.seq
	if len(msg.lanes) == 0 {
		// The probe came back empty: this task is not a fan-out parent after
		// all. Drop it rather than leaving an open marker over nothing, so a
		// second `L` re-asks instead of toggling an expansion that shows
		// nothing.
		l.expanded = l.expanded.without(msg.parentID)
	}
}

// next stamps an outgoing fetch for one parent.
func (l *laneTree) next(parentID int64) uint64 {
	if l.seq == nil {
		l.seq = make(map[int64]uint64, 2)
	}
	l.seq[parentID]++
	return l.seq[parentID]
}

// toggleLanes is `L`: open or close the fan-out under the cursor.
//
// It acts on any task row rather than on a state whitelist. A parent's lanes
// are worth reading in every state it passes through — `awaiting_children`
// while they run, `blocked` when the join or the merge failed, `done`
// afterwards — and a list row carries no field that says "this task once had
// lanes" in the last two (§13.2 serves `children` on the detail endpoint
// only). So the press asks, and a task with no lanes answers with none: the
// fetch comes back empty, laneTree.apply drops the id again, and nothing on
// screen moved.
func (b *board) toggleLanes() tea.Cmd {
	id, ok := b.selected()
	if !ok {
		return nil
	}
	b.lanes.expanded = b.lanes.expanded.toggle(id)
	if !b.lanes.expanded.has(id) {
		// A collapse needs nothing from the daemon: the rows stay cached so
		// the next `L` is instant.
		return nil
	}
	// The cursor stays where it is. `L` is a statement about *this* row, and
	// moving onto the first lane would take the reader off the row they were
	// reading — the same argument `space` already makes.
	return b.laneCmd(id)
}

// laneCmd fetches one parent's lanes. §13.2 already returns them in merge
// order, so the board sorts nothing here: lane order is the order the join
// will merge in, which is the order a human reading a fan-out wants.
func (b *board) laneCmd(parentID int64) tea.Cmd {
	client := b.client
	if client == nil {
		return nil
	}
	seq := b.lanes.next(parentID)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		lanes, err := client.ListTasks(ctx, apiclient.ListTasksOptions{ParentID: parentID})
		return boardLanesMsg{seq: seq, parentID: parentID, lanes: lanes, err: err}
	}
}

// laneCmds refreshes every parent the board is currently showing lanes for.
// It rides the board's own refresh, so a lane that started running, blocked or
// finished updates on the same event the parent's row does.
//
// Only *reachable* parents are asked: an expansion whose own parent has since
// been collapsed renders nothing, and a request for rows nobody can see is
// pure load on the daemon. With nothing expanded this is empty, which is what
// makes the ordinary board's traffic identical to what it was.
func (b *board) laneCmds() []tea.Cmd {
	if len(b.lanes.expanded) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	for _, id := range b.expandedParents() {
		if cmd := b.laneCmd(id); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// expandedParents is every expanded task reachable from the board's own list
// through the expansion chain, outermost first. level is the row's own lane
// depth — 0 for a task the daemon listed — so a row whose lanes would land
// past fan_out.max_depth is not asked about at all: those rows cannot be
// drawn, and fetching for them is load with nothing on screen to show for it.
func (b *board) expandedParents() []int64 {
	out := make([]int64, 0, len(b.lanes.expanded))
	var walk func(t apiclient.Task, level int)
	walk = func(t apiclient.Task, level int) {
		if level+1 > b.lanes.depth() || !b.lanes.expanded.has(t.ID) {
			return
		}
		out = append(out, t.ID)
		for _, lane := range b.lanes.lanesOf(t.ID) {
			walk(lane, level+1)
		}
	}
	for _, t := range b.tasks {
		walk(t, 0)
	}
	return out
}

// spliceLanes hangs each expanded parent's lanes under its row, as indented
// task rows.
//
// It runs after groupRows and before applyFolds, which is what makes the two
// halves of task 014 decision 13 hold at once: the header counts groupRows
// computed never saw a lane, and a lane row carries its parent's group depth
// so a collapsed group swallows the whole subtree with it.
//
// A lane is not filtered. The filter is a question about the board's task
// list; the lanes are a disclosure of one row of it, and hiding half of an
// opened fan-out because the lane titles do not contain what was typed would
// make the expansion lie about what it opened.
func (b *board) spliceLanes(rows []boardRow) []boardRow {
	if len(b.lanes.expanded) == 0 {
		return rows
	}
	out := make([]boardRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
		if r.header {
			continue
		}
		out = b.appendLanes(out, r, 1)
	}
	return out
}

// appendLanes walks one row's subtree. level is the lane nesting of the rows
// it is about to append: 1 directly under a board row, and never past
// fan_out.max_depth — the engine refuses to derive a deeper tree (§7.6), so a
// board that drew one would be drawing something that cannot exist.
func (b *board) appendLanes(out []boardRow, parent boardRow, level int) []boardRow {
	if level > b.lanes.depth() || !b.lanes.expanded.has(parent.task.ID) {
		return out
	}
	for _, lane := range b.lanes.lanesOf(parent.task.ID) {
		row := boardRow{task: lane, depth: parent.depth, lane: level}
		out = append(out, row)
		out = b.appendLanes(out, row, level+1)
	}
	return out
}

// laneGlyph is the disclosure marker on a row that has lanes: `▸` closed, `▾`
// open, the same two the group headers use because they mean the same thing.
// Empty for every other row, which is what keeps a board with no fan-out on it
// identical to the board before this feature.
func (b *board) laneGlyph(t apiclient.Task) string {
	if !b.lanes.known(t) {
		return ""
	}
	if b.lanes.expanded.has(t.ID) {
		return groupGlyphOpen
	}
	return groupGlyphFolded
}

// orderedTasks is every task the board is showing — its own list plus the
// lanes hanging under it — in render order, with no filter applied. It is what
// a bulk action dispatches through (markedTargets): a lane is an ordinary task
// to the daemon, so a marked one has to travel with the rest.
func (b *board) orderedTasks() []apiclient.Task {
	sorted := slices.Clone(b.tasks)
	sortTasks(sorted)
	out := make([]apiclient.Task, 0, len(sorted))
	// level is the row's own lane depth, the same convention appendLanes
	// walks by — so this list holds exactly the rows the board renders.
	var walk func(t apiclient.Task, level int)
	walk = func(t apiclient.Task, level int) {
		out = append(out, t)
		if level+1 > b.lanes.depth() || !b.lanes.expanded.has(t.ID) {
			return
		}
		for _, lane := range b.lanes.lanesOf(t.ID) {
			walk(lane, level+1)
		}
	}
	for _, t := range sorted {
		walk(t, 0)
	}
	return out
}

// knownTasks is every task id the board can name, lanes included and in no
// particular order. Pruning the bulk selection against the board's own list
// alone would drop a marked lane on the next refresh — the lane is live, it is
// simply not in a listing that excludes lanes by design.
func (b *board) knownTasks() []apiclient.Task {
	out := slices.Clone(b.tasks)
	for _, id := range b.lanes.expanded {
		out = append(out, b.lanes.lanesOf(id)...)
	}
	return out
}

// taskByID finds a task the board knows, lane or not.
func (b *board) taskByID(id int64) (apiclient.Task, bool) {
	for _, t := range b.knownTasks() {
		if t.ID == id {
			return t, true
		}
	}
	return apiclient.Task{}, false
}
