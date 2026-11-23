package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Fan-out lanes on the card board (§7.6, §15, issue #316).
//
// The board keeps lanes out of its columns (task 014 decision 13): a column
// is the work someone asked for, and a 64-lane fan-out would bury it. What it
// offers instead is the parent card as a disclosure. A parent wears `▸N` after
// its number; `L` hangs its lanes underneath it as small one-line boxes, in
// the parent's column whatever state each lane is in — a lane is part of its
// parent's work, not a card of its own — and `L` again takes them away.
//
// The lane boxes are cursor stops like cards: ↑/↓ walk onto them, `enter`
// opens the lane's workspace (esc comes back), and `L` on a lane that fanned
// out itself unfolds its own lanes one level deeper, up to fan_out.max_depth.
// `L` on a lane with none folds its parent and lands on the parent's card, so
// the key that opened a fan-out is also the way out of it.
//
// It is the row board's machinery (boardlanes.go) unchanged: the same
// session-only expanded set, the same per-parent fetch, refreshed on the
// board's own refresh, and the same rule that a lane never enters b.tasks —
// so no column count, group count or attention badge ever counts one.

// laneCardH is a lane box's height: its borders and two lines — which lane,
// then where it is. One line truncated the second half away in a
// four-column board, and where a lane is is the reason to look at it.
const laneCardH = 4

// cardLaneEntries is a parent's lanes in drawing order, each followed by its
// own lanes when it is expanded, with the nesting each sits at (0 for the
// parent's own lanes).
func (b *board) cardLaneEntries(parent apiclient.Task) []laneEntry {
	var out []laneEntry
	var walk func(t apiclient.Task, level int)
	walk = func(t apiclient.Task, level int) {
		if level+1 > b.lanes.depth() || !b.lanes.expanded.has(t.ID) {
			return
		}
		for _, lane := range b.lanes.lanesOf(t.ID) {
			out = append(out, laneEntry{lane: lane, depth: level})
			walk(lane, level+1)
		}
	}
	walk(parent, 0)
	return out
}

// hasLanes reports that a task fanned out, from what the board knows: the
// lanes it fetched, a parked or fan-out-blocked parent, or the subtree rollup
// a list row carries (task 129.18).
func (b *board) hasLanes(t apiclient.Task) bool {
	return b.lanes.known(t) || laneHasLanes(t)
}

// laneCountLabel is the `▸N` on a parent card: the lanes it has fetched, or
// the subtree count before it has.
func (b *board) laneCountLabel(t apiclient.Task) string {
	if !b.hasLanes(t) {
		return ""
	}
	glyph := groupGlyphFolded
	if b.lanes.expanded.has(t.ID) {
		glyph = groupGlyphOpen
	}
	n := len(b.lanes.lanesOf(t.ID))
	if n == 0 && t.Children != nil {
		n = t.Children.Total
	}
	if n == 0 {
		return glyph
	}
	return glyph + strconv.Itoa(n)
}

// toggleCardLanes is `L`: unfold or fold the lanes of the card under the
// cursor — a parent, or a lane that fanned out itself — and, on a lane with
// none of its own, fold its parent and land there.
//
// The row board (narrow terminals) answers the same key: its rows are
// spliced from the same expanded set (spliceLanes).
func (b *board) toggleCardLanes() tea.Cmd {
	id, ok := b.selected()
	if !ok {
		return nil
	}
	t, ok := b.taskByID(id)
	if !ok {
		return nil
	}
	if !b.hasLanes(t) {
		if t.ParentTaskID == nil || !b.lanes.expanded.has(*t.ParentTaskID) {
			return nil
		}
		parent := *t.ParentTaskID
		b.lanes.expanded = b.lanes.expanded.without(parent)
		b.selectedID, b.cardHeader = parent, nil
		if !b.cards {
			b.restoreSelection(b.rows())
		}
		return nil
	}
	b.lanes.expanded = b.lanes.expanded.toggle(t.ID)
	if b.lanes.expanded.has(t.ID) {
		return b.laneCmd(t.ID)
	}
	return nil
}

// laneCard draws one lane box: where the lane is, and its number.
func (b *board) laneCard(lane apiclient.Task, selected bool, w int) []string {
	inner := max(w-4, 1)
	edges, border := edgesRounded, styleDim
	if selected {
		edges, border = edgesThick, styleFocus
	}
	glyph := taskStateGlyph(lane.State)
	if glyph == "" {
		glyph = " "
	}
	name := laneName(lane)
	if label := b.laneCountLabel(lane); label != "" {
		name = label + " " + name
	}
	parts := []string{stateWords(lane.State)}
	if step := laneStepClause(lane); step != "" {
		parts = append(parts, step)
	}
	if lane.BlockReason != nil && *lane.BlockReason != "" {
		parts = append(parts, *lane.BlockReason)
	}
	left := laneRowStyle(lane).Render(glyph+" ") + name
	if b.marks.has(lane.ID) {
		// The tick a marked task card carries (#1066): space on a lane
		// marks it, and the count rose with nothing on the box to show it.
		left = styleFocus.Render(ic("✔ ")) + left
	}
	right := styleKey.Render("#" + strconv.FormatInt(lane.ID, 10))
	if room := inner - ansi.StringWidth(right) - 1; ansi.StringWidth(left) > room {
		left = ansi.Truncate(left, max(room, 1), "…")
	}
	where := laneRowStyle(lane).Render(ansi.Truncate(strings.Join(parts, " · "), inner, "…"))
	return drawBox("", "", []string{padBetweenCells(left, right, inner), where}, w, laneCardH, edges, border)
}

// laneIndent is how far a lane box sits in from its parent card: one step
// per level, so a nested fan-out reads as a tree.
func laneIndent(depth int) string {
	return strings.Repeat(" ", 2*(depth+1))
}

// laneCardWidth is a lane box's width at a nesting, never narrower than
// something that can still say which lane it is.
func laneCardWidth(cardW, depth int) int {
	return max(cardW-len(laneIndent(depth)), 12)
}
