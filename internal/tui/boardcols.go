package tui

import (
	"charm.land/bubbles/v2/table"
)

// Column widths. The table adds one space of padding either side of every
// cell, so a column occupies its width plus two.
const (
	colPadding     = 2
	widthID        = 5
	widthProject   = 14
	widthWorkflow  = 14
	widthState     = 18 // fits "! awaiting_input"
	widthStepShort = 7  // "12/12"
	widthStepLong  = 18
	widthElapsed   = 9
	widthCost      = 8
	minTitle       = 16
)

// columnSet records which optional columns survived the current width.
type columnSet struct {
	project bool
	// workflow answers "what is this task actually running", which the step
	// name alone cannot: "survey" means nothing without knowing it belongs to
	// docs-update.
	workflow bool
	stepName bool
	cost     bool
}

// fixedWidth is everything a set costs except the title, padding included.
func (s columnSet) fixedWidth() int {
	total := widthID + widthState + widthElapsed
	count := 4 // id, title, state, elapsed
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
	return total + count*colPadding
}

// titleWidth is the space left for the title under this set.
func (s columnSet) titleWidth(width int) int { return width - s.fixedWidth() }

// columnsFor decides which optional columns a terminal width can carry.
//
// §15's columns do not fit a narrow terminal, and truncating all of them
// proportionally leaves a row of unreadable stubs — a 6-character title
// tells you nothing. Whole columns are dropped instead, in increasing order
// of how much you navigate by them: cost, then the step name, then the
// workflow, then the project. Dropping continues until the title clears its
// minimum, so the thresholds follow from the widths rather than being
// second-guessed as constants that can silently disagree with them.
//
// The workflow outranks the step name: "survey" is meaningless without
// knowing it belongs to docs-update, while the workflow alone still tells you
// what a task is doing.
// A grouped level costs no column: the header above the rows already names
// it, and repeating it down every row of the group is fourteen characters
// spent saying what the reader just read. The width that frees goes to the
// title, which is where a grouped board needs it — the titles are indented
// under their headers.
func columnsFor(width int, g grouping) columnSet {
	set := columnSet{
		project:  !g.has(groupProject),
		workflow: !g.has(groupWorkflow),
		stepName: true,
		cost:     true,
	}
	for set.titleWidth(width) < minTitle {
		switch {
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

// boardColumns builds the table columns for a terminal width, giving the
// title whatever space the fixed columns leave.
func boardColumns(width int, g grouping) ([]table.Column, columnSet) {
	set := columnsFor(width, g)
	title := max(set.titleWidth(width), minTitle)
	stepWidth := widthStepShort
	if set.stepName {
		stepWidth = widthStepLong
	}

	cols := make([]table.Column, 0, 8)
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
	return cols, set
}
