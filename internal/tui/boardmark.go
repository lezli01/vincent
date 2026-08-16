package tui

import (
	"slices"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The task table's bulk selection (§15, task 011).
//
// Triage is the board's job and triage arrives in batches: a sweep of finished
// tasks to archive, a run of queued ones to cancel after a bad workflow edit.
// One row at a time, that is the same keypress N times with a confirmation
// between each — which is where a human either stops tidying up or stops
// reading the confirmations.
//
// The selection is a set of **tasks**, not of rows: it survives a filter, a `g`
// regroup and a refresh, because all three are ways of looking at the board
// rather than statements about what was chosen (task 011 decision). Narrowing
// it to what is currently visible would mean typing a filter silently changes
// what a confirmed archive destroys. The count in the panel title is what keeps
// a marked task the filter is hiding honest.

// markSet is the marked task ids. A slice rather than a map: a selection is
// tens of rows at most, and a stable order makes what it feeds — the targets
// of a bulk action, and the test that pins them — deterministic.
type markSet []int64

func (m markSet) has(id int64) bool { return slices.Contains(m, id) }

// toggle marks an unmarked task and unmarks a marked one.
func (m markSet) toggle(id int64) markSet {
	if i := slices.Index(m, id); i >= 0 {
		return slices.Delete(slices.Clone(m), i, i+1)
	}
	return append(m, id)
}

// add marks every id not already marked.
func (m markSet) add(ids ...int64) markSet {
	for _, id := range ids {
		if !m.has(id) {
			m = append(m, id)
		}
	}
	return m
}

// drop unmarks ids. It is what a finished bulk action does with the tasks the
// daemon accepted: what it refused stays marked, so a retry needs no
// re-selection and the rows still marked are the ones still to deal with.
func (m markSet) drop(ids ...int64) markSet {
	out := make(markSet, 0, len(m))
	for _, id := range m {
		if !slices.Contains(ids, id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// keep prunes marks for tasks the daemon no longer lists — archived away, or
// belonging to a project that was removed. A mark for a task that is gone would
// be counted in the panel title and dispatched to a 404.
func (m markSet) keep(tasks []apiclient.Task) markSet {
	if len(m) == 0 {
		return nil
	}
	live := make([]int64, 0, len(tasks))
	for _, t := range tasks {
		live = append(live, t.ID)
	}
	out := make(markSet, 0, len(m))
	for _, id := range m {
		if slices.Contains(live, id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// hasMarks reports that a bulk selection is driving the board.
func (b *board) hasMarks() bool { return len(b.marks) > 0 }

// toggleMark is `space`: mark or unmark the row under the cursor. A group
// header resolves to the first task under it the way every other key does, so
// the press is never a silent no-op.
func (b *board) toggleMark() {
	id, ok := b.selected()
	if !ok {
		return
	}
	b.marks = b.marks.toggle(id)
}

// markVisible is `V`: mark every task the filter is showing, or unmark them
// when they are all marked already. One key for "select all" and for undoing
// it, because those are the same intention twice — and it unmarks only what is
// visible, so a selection built before the filter was typed is not thrown away
// by a key that was aimed at the rows on screen.
func (b *board) markVisible() {
	visible := b.visible()
	if len(visible) == 0 {
		return
	}
	ids := make([]int64, 0, len(visible))
	all := true
	for _, t := range visible {
		ids = append(ids, t.ID)
		if !b.marks.has(t.ID) {
			all = false
		}
	}
	if all {
		b.marks = b.marks.drop(ids...)
		return
	}
	b.marks = b.marks.add(ids...)
}

func (b *board) clearMarks() { b.marks = nil }

// markedTargets is the selection as the action bar sees it: what the daemon
// says can be done to each marked task (§6), in board order — top to bottom, so
// a bulk action runs in the order the rows were read. Filtering is deliberately
// not applied; the selection is not a view.
func (b *board) markedTargets() []markedTask {
	if len(b.marks) == 0 {
		return nil
	}
	sorted := make([]apiclient.Task, len(b.tasks))
	copy(sorted, b.tasks)
	sortTasks(sorted)
	out := make([]markedTask, 0, len(b.marks))
	for _, t := range sorted {
		if b.marks.has(t.ID) {
			out = append(out, markedTask{id: t.ID, actions: t.AvailableActions})
		}
	}
	return out
}

// markGlyph marks a selected row. A glyph rather than colour alone, so the
// selection survives NO_COLOR and a 16-colour terminal (§15 Colour).
const markGlyph = "✓"

// markCell is the row's cell in the marker column. styleKey is reused rather
// than duplicated: the glyph means the same thing a key hint does — this is the
// thing the next press acts on.
func (b *board) markCell(id int64) string {
	if b.marks.has(id) {
		return styleKey.Render(markGlyph)
	}
	return " "
}
