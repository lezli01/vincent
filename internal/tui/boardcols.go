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
	// widthPR holds the pull request marker (task 129.16): the glyph and a
	// five-digit number, `⇡#12345`. It never wraps — it is an identifier,
	// like the ID (task 050 decision 6) — and a longer number is cut.
	widthPR = 7
	// widthStatus holds a step's own status message (task 036) — enough for a
	// clause. It is the *base* width: a board with a surplus to spend takes
	// this column up to widthStatusMax (boardColumns), and whatever still
	// does not fit wraps onto the row's further lines rather than being cut
	// (task 050). It is not wider by default because this column is
	// affordable only on a board with room to spare, which is the deal it is
	// admitted on.
	widthStatus = 28
	minTitle    = 16
	// minTitleWithStatus is the title width below which the status column is
	// not worth its cells: the status' gate.
	//
	// Amended 2026-09-29 (task 129.16, decision 3 of task 129): this is the
	// gate again, separate from maxTitle, and half of it. Task 050 decision 2
	// had folded it into maxTitle (64), which kept STATUS off below 164
	// columns on the default grouped board — the board's one changing signal
	// hidden at the most common width while constant columns stayed. At 32 a
	// 120-column grouped board keeps STATUS, a short STEP and a 41-cell
	// title. minTitle alone is still the wrong gate: a title of 16 beside a
	// status identifies nothing.
	minTitleWithStatus = 32
	// maxTitle is the ceiling on the title column, and the width above which
	// the status column may wrap.
	//
	// As a ceiling (task 050 decision 1): the title is the only flexible
	// column, so without one it takes every cell a wide terminal offers —
	// 100 of them at 200 columns — while STEP is still cutting `3/7 green ·
	// loop 4/10` at widthStepLong and STATUS is still cutting a clause at
	// widthStatus. Past this width a title is mostly trailing blanks, so
	// boardColumns spends the surplus on those two first and gives back only
	// what neither has an appetite for.
	//
	// As the wrap line (task 129.16): a status admitted on a board whose
	// title is under maxTitle — admitted only because minTitleWithStatus is
	// lower than the old gate — is cut to one line with `…` rather than
	// wrapped, so it cannot raise every row's height (task 050 decision 4) on
	// a board that had no room for it before. Above it the status wraps as
	// task 050 decisions 6 and 7 describe.
	//
	// Amended 2026-09-29 (task 129.16): until then this was also the status'
	// gate (task 050 decision 2, "maxTitle replaces minTitleWithStatus").
	// That role went back to minTitleWithStatus; this one is its successor.
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
	// 3). The step's ceiling is measured off its widest content, a task in a
	// `for_each` loop reporting its body step: `3/7 green · loop 4/10 ·
	// repair 2/3` is 34 cells, and formatStep drops clauses from the tail
	// below that rather than wrapping. The status' is a couple of the board's
	// lines of prose, past which wrapping is the better answer than a column
	// nothing else can see around.
	//
	// Amended 2026-09-03 (issue #317): 32 was the same measurement taken
	// against `3/7 green · loop 4/10` — 21 cells — plus room for a longer
	// step name. The body-step clause is 13 cells more, so a ceiling of 34
	// now buys the full sample exactly and the slack goes to the title, which
	// is the column that always has an appetite for it.
	widthStepMax   = 34
	widthStatusMax = 96
)

// columnWidth is the width the current column set gave the column headed
// title, or 0 when this board is not carrying it. cellsFor and boardColumns
// already share one shape (the same optional columns in the same order), so
// the heading is the one stable name a cell can find its own width by — a
// cell's index moves with every optional column ahead of it.
func columnWidth(cols []table.Column, title string) int {
	for i := range cols {
		if cols[i].Title == title {
			return cols[i].Width
		}
	}
	return 0
}

// maxBoardColumns is every column the widest board can carry — the size a row
// is built at, so rowsFor and groupHeaderRow never grow their slice mid-loop.
const maxBoardColumns = 11

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
	// pr is the pull request marker (task 129.16): on only while some row the
	// board holds has a live link, and the first column shed.
	pr bool
	// status is the step's own status message (§5.4, task 036).
	//
	// Amended 2026-09-29 (task 129.16): it was the first column shed and the
	// last admitted (task 036 decision 9). It is on only while some row the
	// board holds has a message, and then it outranks COST and the step name
	// — the board's one changing signal is worth more than a constant column.
	status bool
	// statusLine says the status was admitted below maxTitle, so its cell is
	// cut to one line rather than wrapped (task 129.16, amending task 050
	// decision 6 in part).
	statusLine bool
}

// boardContent is what the rows the board holds have to say, as far as the
// column set cares: whether any of them has a status message, a cost, or a
// live pull request link (task 129.16). "The rows the board holds" is every
// task row after the filter and the archive view — scrolled out of view and
// folded into a collapsed group included — so a column never appears or
// vanishes while the cursor moves.
type boardContent struct {
	status bool
	cost   bool
	pr     bool
}

// contentOf computes boardContent over rows. Group headers carry no task and
// say nothing.
func contentOf(rows []boardRow) boardContent {
	var c boardContent
	for _, r := range rows {
		if r.header {
			continue
		}
		t := r.task
		c.status = c.status || formatStatus(t.StatusMessage) != ""
		c.cost = c.cost || t.CostUSD != nil
		c.pr = c.pr || formatPR(t.GitHubPull) != ""
	}
	return c
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
	if s.pr {
		total += widthPR
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
// of how much you navigate by them: the pull request marker, then cost, then
// the step name, then the status, then the workflow, then the project.
// Dropping continues until the title clears its minimum — minTitleWithStatus
// while the status is on, minTitle once it is off — so the thresholds follow
// from the widths rather than being second-guessed as constants that can
// silently disagree with them.
//
// Three columns are candidates only while a row has something to put in
// them (task 129.16): COST when some row has a cost, PR when some row has a
// live pull request link, STATUS when some row has a status message. A
// column of dashes or blanks is width taken from the title to say nothing,
// and codex and several adapters never report a cost at all.
//
// Amended 2026-09-29 (task 129.16): the status used to go first, gated at
// maxTitle (task 036 decision 9, task 050 decisions 1–2), which kept it off
// below 164 columns on the default grouped board. It now outranks COST and
// the step name, is gated at minTitleWithStatus, and is cut to one line when
// admitted below maxTitle (statusLine) so it cannot raise the row height.
// The step counter is never shed; only its name is. With no status content
// the ladder is the one it was, apart from COST and PR.
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
func columnsFor(width int, g grouping, marking bool, content boardContent) columnSet {
	set := columnSet{
		mark:     marking,
		project:  !g.has(groupProject),
		workflow: !g.has(groupWorkflow),
		stepName: true,
		cost:     content.cost,
		pr:       content.pr,
		status:   content.status,
	}
	floor := func() int {
		if set.status {
			return minTitleWithStatus
		}
		return minTitle
	}
	for set.titleWidth(width) < floor() {
		switch {
		case set.pr:
			set.pr = false
		case set.cost:
			set.cost = false
		case set.stepName:
			set.stepName = false
		case set.status:
			set.status = false
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
	set.statusLine = set.status && set.titleWidth(width) < maxTitle
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
// leaving dead cells: with no STATUS — no row has a message — STEP fills at +14 and nothing else has any appetite, so without
// it twelve cells on the right would render blank. It only lets the title
// exceed maxTitle once both other columns are full.
func boardColumns(width int, g grouping, marking bool, content boardContent) ([]table.Column, columnSet) {
	set := columnsFor(width, g, marking, content)
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
	if set.pr {
		cols = append(cols, table.Column{Title: "PR", Width: widthPR})
	}
	if set.status {
		cols = append(cols, table.Column{Title: "STATUS", Width: statusWidth})
	}
	return cols, set
}
