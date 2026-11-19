package tui

import "strings"

// The wide archive's grid and switches (task 150.7). Everything in an archive
// has finished, so its one column of cards is laid out two to a row where the
// width allows.

// archiveCardMinW is the narrowest card worth a second one beside it.
const archiveCardMinW = 40

// archivePerRow is how many cards fit across a column of inner cells, past the
// rail that ties them to their issue.
func archivePerRow(inner int) int {
	return max(min((inner-2)/archiveCardMinW, 2), 1)
}

// stepArchive moves the cursor through the grid as it is drawn: rows up and
// down, across groups, and cards left and right within a row.
func (b *board) stepArchive(rows, cards int) {
	groups := b.cardGroups()
	stops := cardStops(groups)
	column, at, ok := b.cardCursor(stops, groups)
	if !ok {
		return
	}
	var grid [][]taskStop
	r, c := 0, 0
	for i, s := range stops[column] {
		last := len(grid) - 1
		if last < 0 || len(grid[last]) == b.cardPerRow || grid[last][0].key != s.key {
			grid = append(grid, nil)
			last++
		}
		grid[last] = append(grid[last], s)
		if i == at {
			r, c = last, len(grid[last])-1
		}
	}
	r = min(max(r+rows, 0), len(grid)-1)
	c = min(max(c+cards, 0), len(grid[r])-1)
	b.restOn(grid[r][c])
}

// archiveSwitches is the archive's two switches: what it lists, and how far back.
func archiveSwitches(tasks bool, window int) string {
	parts := make([]string, 0, len(archivedWindows))
	for i, w := range archivedWindows {
		style := styleDim
		if i == window {
			style = styleTabActive
		}
		parts = append(parts, style.Render(" "+w.label+" "))
	}
	return archiveSwitch(tasks) + styleDim.Render("  │  ") + strings.Join(parts, " ")
}
