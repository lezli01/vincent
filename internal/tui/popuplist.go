package tui

import "fmt"

// The root's popups — the palette and the project, reader and link pickers —
// share one box and one way of moving through a list that does not fit it
// (task 135.10).

// popupBox is the size of the root's popup box on a width×height terminal
// whose body starts top rows down (task 135.10 decision 1). It grows with
// the terminal, from 64×18 at 80×24 to at most 100×30, and keeps the narrow
// fallbacks it always had: under 24 columns it takes nearly the whole width,
// and it is never taller than the terminal less four rows, nor than what is
// left below the chrome above the footer.
func popupBox(width, height, top int) (w, h int) {
	w = min(min(max(width-16, 64), 100), width-8)
	if w < 24 {
		w = max(width-2, 10)
	}
	h = min(min(max(height-6, 18), 30), max(height-4, 6))
	if room := height - top - 1; room >= 3 {
		h = min(h, room)
	}
	return w, h
}

// popupPage is what the last render of a popup list laid out, which pgup
// and pgdown page through: the row each entry sits on and the rows the
// list had. A grouped list's section headers and the blank lines between
// groups are rows but not entries, so a page is measured in what the
// window shows rather than in a fixed count (review F1).
type popupPage struct {
	entryRow []int
	height   int
}

// span is the entries [first,last] a render with the cursor on entry c
// would show.
func (pg popupPage) span(c int) (first, last int) {
	n := len(pg.entryRow)
	rows := pg.entryRow[n-1] + 1
	start, h, _ := cuedSpan(rows, pg.entryRow[c], pg.height)
	first, last = c, c
	for first > 0 && pg.entryRow[first-1] >= start {
		first--
	}
	for last < n-1 && pg.entryRow[last+1] < start+h {
		last++
	}
	return first, last
}

// popupPageKey moves a popup list's cursor for the paging keys (task 135.10
// decision 5): pgup and pgdown by a page, home and end to either end. n is
// the number of entries the cursor can reach and pg the last render's
// layout. A page moves as far as it can while the window it lands on still
// meets the one it leaves, so every entry is on screen at some point of a
// walk from one end to the other (review F1). The list never wraps, as ↑
// and ↓ do not; any other key leaves the cursor where it is.
func popupPageKey(key string, cursor, n int, pg popupPage) int {
	last := max(n-1, 0)
	cursor = min(max(cursor, 0), last)
	switch key {
	case "home":
		return 0
	case "end":
		return last
	case "pgup", "pgdown":
	default:
		return cursor
	}
	if len(pg.entryRow) != n || n == 0 || pg.height <= 0 {
		// The list changed since the render that laid it out: one entry per
		// row is the best guess until the next one.
		pg = popupPage{entryRow: make([]int, n), height: max(pg.height, 1)}
		for i := range n {
			pg.entryRow[i] = i
		}
		if n == 0 {
			return 0
		}
	}
	first, shownLast := pg.span(cursor)
	at := cursor
	if key == "pgdown" {
		for c := cursor + 1; c <= last; c++ {
			if f, _ := pg.span(c); f > shownLast+1 {
				break
			}
			at = c
		}
		return at
	}
	for c := cursor - 1; c >= 0; c-- {
		if _, l := pg.span(c); l < first-1 {
			break
		}
		at = c
	}
	return at
}

// popupWindow is a popup list's rows windowed around the cursor row with the
// issues list's overflow cues (#796): `▲ n more` while the list continues
// above and `▼ n more` while it continues below. entryRow is the row each
// entry sits on, in order; the cues count those entries, never a section
// header or the blank line before one (review F2). The returned page is
// what pgup and pgdown move through.
func popupWindow(rows []string, entryRow []int, cursorRow, height int) (out []string, page popupPage) {
	page = popupPage{entryRow: entryRow, height: height}
	start, h, _ := cuedSpan(len(rows), cursorRow, height)
	end := min(start+h, len(rows))
	if height <= 0 {
		return nil, page
	}
	before, after := 0, 0
	for _, r := range entryRow {
		switch {
		case r < start:
			before++
		case r >= end:
			after++
		}
	}
	cued := len(rows) > height && height >= 3
	if cued && start > 0 {
		out = append(out, styleDim.Render(fmt.Sprintf("  ▲ %d more", before)))
	}
	out = append(out, rows[start:end]...)
	if cued && end < len(rows) {
		out = append(out, styleDim.Render(fmt.Sprintf("  ▼ %d more", after)))
	}
	return out, page
}
