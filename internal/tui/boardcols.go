package tui

import (
	"charm.land/bubbles/v2/table"
)

// Column widths. The table adds one space of padding either side of every
// cell, so a column occupies its width plus two.
const (
	colPadding = 2
	// widthMark holds the bulk-selection glyph (task 011). It is one cell and
	// it only exists while something is marked, so an unmarked board is the
	// board every earlier version rendered, to the column.
	widthMark      = 1
	widthID        = 5
	widthProject   = 14
	widthWorkflow  = 14
	widthState     = 18 // fits "! awaiting_input"
	widthStepShort = 7  // "12/12"
	widthStepLong  = 18
	widthElapsed   = 9
	widthCost      = 8
	// widthStatus holds a step's own status message (task 036) — enough for a
	// clause. It is the *base* width: a board with a surplus to spend takes
	// this column up to widthStatusMax (boardColumns), and whatever still
	// does not fit wraps onto the row's further lines rather than being cut
	// (task 050). It is not wider by default because this column is
	// affordable only on a board with room to spare, which is the deal it is
	// admitted on.
	widthStatus = 28
	minTitle    = 16
	// maxTitle is two facts at one value, because they are the same fact: the
	// ceiling on the title column, and the title width below which the status
	// column is not worth its cells.
	//
	// As a ceiling (task 050 decision 1): the title is the only flexible
	// column, so without one it takes every cell a wide terminal offers —
	// 100 of them at 200 columns — while STEP is still cutting `3/7 green ·
	// loop 4/10` at widthStepLong and STATUS is still cutting a clause at
	// widthStatus. Past this width a title is mostly trailing blanks, so
	// boardColumns spends the surplus on those two first and gives back only
	// what neither has an appetite for.
	//
	// As the status column's gate it is the old minTitleWithStatus at the
	// same value, which leaves columnsFor byte-identical at every width.
	// minTitle alone is the wrong gate: a 120-column board is the common
	// case, and admitting the status there would take a 50-cell title down to
	// 20 — still "legal", and still a board whose titles no longer identify
	// anything. Set high enough, too, that the column cannot eat the width a
	// grouped board frees by dropping PROJECT and WORKFLOW.
	//
	// Amended 2026-08-29 (task 050 decision 1): task 036 decision 9 recorded
	// that a grouped board's title stays *strictly* wider than a flat board's
	// at every width. A ceiling cannot keep that — above it both boards'
	// titles are equal — so the invariant is now that a grouped board is
	// never worse off: the width it frees is still spent on the row, it just
	// lands wherever the allocation order below puts it. The reasoning that a
	// *new column* must not silently re-spend that width is untouched, which
	// is what this gate still enforces.
	maxTitle = 64
	// widthStepMax and widthStatusMax are how far a surplus may take those
	// two columns before the rest goes back to the title (task 050 decision
	// 3). The step's ceiling fits `3/7 green · loop 4/10` — 21 cells — with
	// room for a step name longer than the sample's; the status' is a couple
	// of the board's lines of prose, past which wrapping is the better answer
	// than a column nothing else can see around.
	widthStepMax   = 32
	widthStatusMax = 96
)

// maxBoardColumns is every column the widest board can carry — the size a row
// is built at, so rowsFor and groupHeaderRow never grow their slice mid-loop.
const maxBoardColumns = 10

// columnSet records which optional columns survived the current width.
type columnSet struct {
	// mark is the bulk-selection marker (task 011). Unlike the others it is
	// not a width decision and is never shed: it is on exactly while something
	// is marked, because a selection you cannot see is worse than a narrow
	// title.
	mark    bool
	project bool
	// workflow answers "what is this task actually running", which the step
	// name alone cannot: "survey" means nothing without knowing it belongs to
	// docs-update.
	workflow bool
	stepName bool
	cost     bool
	// status is the step's own status message (§5.4, task 036). It is the
	// first column shed and the last admitted: it is a luxury of a wide
	// terminal, and a board narrow enough to drop it renders exactly as it
	// did before the column existed.
	status bool
}

// fixedWidth is everything a set costs except the title, padding included.
func (s columnSet) fixedWidth() int {
	total := widthID + widthState + widthElapsed
	count := 4 // id, title, state, elapsed
	if s.mark {
		total += widthMark
		count++
	}
	if s.project {
		total += widthProject
		count++
	}
	if s.workflow {
		total += widthWorkflow
		count++
	}
	if s.stepName {
		total += widthStepLong
	} else {
		total += widthStepShort
	}
	count++
	if s.cost {
		total += widthCost
		count++
	}
	if s.status {
		total += widthStatus
		count++
	}
	return total + count*colPadding
}

// titleWidth is the space left for the title under this set.
func (s columnSet) titleWidth(width int) int { return width - s.fixedWidth() }

// columnsFor decides which optional columns a terminal width can carry.
//
// §15's columns do not fit a narrow terminal, and truncating all of them
// proportionally leaves a row of unreadable stubs — a 6-character title
// tells you nothing. Whole columns are dropped instead, in increasing order
// of how much you navigate by them: the status, then cost, then the step
// name, then the workflow, then the project. Dropping continues until the
// title clears its minimum, so the thresholds follow from the widths rather
// than being second-guessed as constants that can silently disagree with
// them.
//
// The status goes first (task 036) because it is the only column that is
// prose: a task is found by its project, its workflow and its title, and a
// step's self-report is what you read once you have found it. Shedding it
// first is also what keeps the addition free — a board too narrow for it
// renders byte-for-byte as it did before.
//
// The workflow outranks the step name: "survey" is meaningless without
// knowing it belongs to docs-update, while the workflow alone still tells you
// what a task is doing.
// A grouped level costs no column: the header above the rows already names
// it, and repeating it down every row of the group is fourteen characters
// spent saying what the reader just read. The width that frees is spent on
// the row — first on the title, which is where a grouped board needs it
// because the titles are indented under their headers, and then, once the
// title has reached maxTitle, on STEP and STATUS in that order (boardColumns,
// task 050 decision 3).
// The marker column is outside the shedding order entirely: it is three cells
// wide with its padding, it exists only while a selection does, and it is the
// one column whose absence would make the keys lie about what they act on.
func columnsFor(width int, g grouping, marking bool) columnSet {
	set := columnSet{
		mark:     marking,
		project:  !g.has(groupProject),
		workflow: !g.has(groupWorkflow),
		stepName: true,
		cost:     true,
		status:   true,
	}
	// The status has a gate of its own, stricter than the shedding ladder
	// below, which is what makes it a luxury of a wide terminal rather than
	// something every board pays for. Anything that clears this gate has room
	// to spare; anything that does not renders exactly as it did before the
	// column existed.
	if set.titleWidth(width) < maxTitle {
		set.status = false
	}
	for set.titleWidth(width) < minTitle {
		switch {
		case set.status:
			set.status = false
		case set.cost:
			set.cost = false
		case set.stepName:
			set.stepName = false
		case set.workflow:
			set.workflow = false
		case set.project:
			set.project = false
		default:
			// Nothing left to shed: a terminal this narrow gets the minimum
			// title and will wrap, which beats hiding the id or the state.
			return set
		}
	}
	return set
}

// boardColumns builds the table columns for a terminal width.
//
// The title takes whatever space the fixed columns leave, but only up to
// maxTitle. Past that the surplus is spent in a fixed order — STEP to
// widthStepMax, then STATUS to widthStatusMax, then the remainder back to the
// title (task 050 decision 3). Those two are the columns whose content
// demonstrably outgrows them; STATE is deliberately not among them, because
// the recorded reason for keeping a hold's reason out of that cell is that
// widening a column for a rare state costs every board the columns that shed
// first (§15, task 036) — the wrap is what makes its overflow readable now.
//
// The give-back is not a softening of the ceiling, it is what stops the board
// leaving dead cells: with STATUS gated off — the default grouping at 160
// columns — STEP fills at +14 and nothing else has any appetite, so without
// it twelve cells on the right would render blank. It only lets the title
// exceed maxTitle once both other columns are full.
func boardColumns(width int, g grouping, marking bool) ([]table.Column, columnSet) {
	set := columnsFor(width, g, marking)
	title := max(set.titleWidth(width), minTitle)
	stepWidth := widthStepShort
	if set.stepName {
		stepWidth = widthStepLong
	}
	statusWidth := widthStatus
	if surplus := title - maxTitle; surplus > 0 {
		title = maxTitle
		if set.stepName {
			take := min(surplus, widthStepMax-stepWidth)
			stepWidth += take
			surplus -= take
		}
		if set.status {
			take := min(surplus, widthStatusMax-statusWidth)
			statusWidth += take
			surplus -= take
		}
		title += surplus
	}

	cols := make([]table.Column, 0, maxBoardColumns)
	if set.mark {
		// No heading: a one-cell column has no room for one, and "✓" over a
		// column of blanks reads as a state the rows are failing.
		cols = append(cols, table.Column{Title: "", Width: widthMark})
	}
	cols = append(cols, table.Column{Title: "ID", Width: widthID})
	if set.project {
		cols = append(cols, table.Column{Title: "PROJECT", Width: widthProject})
	}
	if set.workflow {
		cols = append(cols, table.Column{Title: "WORKFLOW", Width: widthWorkflow})
	}
	cols = append(cols,
		table.Column{Title: "TITLE", Width: title},
		table.Column{Title: "STATE", Width: widthState},
		table.Column{Title: "STEP", Width: stepWidth},
		table.Column{Title: "ELAPSED", Width: widthElapsed},
	)
	if set.cost {
		cols = append(cols, table.Column{Title: "COST", Width: widthCost})
	}
	if set.status {
		cols = append(cols, table.Column{Title: "STATUS", Width: statusWidth})
	}
	return cols, set
}
