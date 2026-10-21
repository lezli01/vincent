package tui

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

// popupPageKey moves a popup list's cursor for the paging keys (task 135.10
// decision 5): pgup and pgdown by a page, home and end to either end. n is
// the number of rows the cursor can reach and page the rows the last render
// showed. The list never wraps, as ↑ and ↓ do not; any other key leaves
// the cursor where it is.
func popupPageKey(key string, cursor, n, page int) int {
	page = max(page, 1)
	last := max(n-1, 0)
	switch key {
	case "pgup":
		return max(cursor-page, 0)
	case "pgdown":
		return min(cursor+page, last)
	case "home":
		return 0
	case "end":
		return last
	}
	return cursor
}

// popupWindow is a popup list's rows windowed around the cursor row with the
// issues list's overflow cues (#796): `▲ n more` while the list continues
// above and `▼ n more` while it continues below. page is how many rows were
// shown, which is what pgup and pgdown move by.
func popupWindow(rows []string, cursorRow, height int) (out []string, page int) {
	out, _, page, _ = cuedWindow(rows, cursorRow, height)
	return out, page
}
