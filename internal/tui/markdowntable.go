package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Markdown tables in the output pane (task 075, §15).
//
// A table cannot go through wrapLine, which is a one-dimensional layout of one
// paneLine. It gets its own block kind and its own renderer, emitting composed
// lines directly the way mdRule already does.
//
// The layout rule is arithmetic rather than a heuristic, so the same source at
// the same width always draws the same table and the width at which it
// degrades is a stated number rather than an observed one:
//
//	Σ natural ≤ available            → natural widths, drawn aligned
//	Σ minimum ≤ available < Σ natural → minimum widths plus the surplus,
//	                                    shared in proportion to each column's
//	                                    remaining demand (natural − minimum)
//	Σ minimum > available            → the stacked form
//
// Neither fallback is a clipped table and neither is a second scrolling axis:
// the first loses cells the reader came for, and the second makes keyboard and
// mouse behaviour ambiguous inside a pane that has only ever scrolled one way.
//
// The wrap-plain-then-style invariant (task 073 decision 2) is untouched here.
// A cell's segments are measured by the summed cols() of their *plain* text,
// wrapped plain, padded plain, and each run is styled only once the line is
// laid out.

// mdTableGap is the space between two columns. Two cells rather than a border:
// a drawn grid is visually expensive in a narrow terminal and spends width
// that belongs to the content.
const mdTableGap = 2

// mdAlign is a column's alignment, taken from the delimiter row's colons.
type mdAlign int

const (
	mdAlignLeft mdAlign = iota
	mdAlignCenter
	mdAlignRight
)

// mdCell is one table cell: its inline segments, the plain text they measure
// as, and whether it may be broken across lines at all.
type mdCell struct {
	segs  []segment
	plain string
	// unbreakable marks a cell that is a single inline-code span. A code
	// cell wrapped mid-token is no longer the literal the author wrote, so
	// its whole width is the column's minimum.
	unbreakable bool
}

// mdTableBlock is a parsed table. Header and body cells are already inline
// segments: everything a cell may hold — emphasis, strong text, an inline code
// span, a link — is resolved at parse time, so the reference numbering a link
// in a cell produces follows document order like any other.
type mdTableBlock struct {
	header []mdCell
	rows   [][]mdCell
	align  []mdAlign
}

// tableAt recognizes a table starting at src[i]: a header row of pipe cells,
// then a delimiter row of the same width, then body rows until a blank line or
// a line that is not a row. It returns the block and the index of the first
// line after it.
//
// The delimiter row is required, which is the whole point: prose containing a
// pipe stays a paragraph, and a lone `| a | b |` is a line of text.
func tableAt(src []string, i int, refs *mdRefs) (*mdTableBlock, int, bool) {
	if i+1 >= len(src) || !isTableRow(src[i]) {
		return nil, 0, false
	}
	align, ok := delimiterRow(src[i+1])
	if !ok {
		return nil, 0, false
	}
	// The header must have exactly the delimiter's columns. A row of a
	// different width is not a table's header — it is a line of prose above
	// something that looks like a rule.
	if len(splitTableCells(strings.TrimSpace(src[i]))) != len(align) {
		return nil, 0, false
	}
	header := tableCells(src[i], len(align), refs)
	tbl := &mdTableBlock{header: header, align: align}
	n := i + 2
	for ; n < len(src) && isTableRow(src[n]); n++ {
		tbl.rows = append(tbl.rows, tableCells(src[n], len(align), refs))
	}
	return tbl, n, true
}

// isTableRow reports a line that could be a row: non-blank and containing a
// pipe that is not inside an inline code span.
func isTableRow(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	return len(splitTableCells(t)) > 0 && strings.Count(t, "|") > 0
}

// delimiterRow parses `|---|:--:|---:|` into per-column alignments.
func delimiterRow(line string) ([]mdAlign, bool) {
	cells := splitTableCells(strings.TrimSpace(line))
	if len(cells) == 0 || !strings.Contains(line, "|") {
		return nil, false
	}
	out := make([]mdAlign, 0, len(cells))
	for _, c := range cells {
		c = strings.TrimSpace(c)
		left := strings.HasPrefix(c, ":")
		right := strings.HasSuffix(c, ":")
		body := strings.Trim(c, ":")
		if body == "" || strings.Trim(body, "-") != "" {
			return nil, false
		}
		switch {
		case left && right:
			out = append(out, mdAlignCenter)
		case right:
			out = append(out, mdAlignRight)
		default:
			out = append(out, mdAlignLeft)
		}
	}
	return out, true
}

// tableCells splits a row and resolves each cell's inline Markdown. A short
// row is padded with empty cells and a long one is cut, so the grid stays
// rectangular whatever the author wrote.
func tableCells(line string, want int, refs *mdRefs) []mdCell {
	raw := splitTableCells(strings.TrimSpace(line))
	out := make([]mdCell, want)
	base := lipgloss.NewStyle()
	for i := range out {
		src := ""
		if i < len(raw) {
			src = strings.TrimSpace(raw[i])
		}
		src = strings.ReplaceAll(src, "\\|", "|")
		segs := inlineSegments(src, base, 0, refs)
		out[i] = mdCell{
			segs:        segs,
			plain:       segsPlain(segs),
			unbreakable: isCodeSpanCell(src),
		}
	}
	return out
}

// splitTableCells splits on the pipes that separate cells: not an escaped
// `\|`, and not one inside an inline code span, so a cell may hold `a|b`. A
// leading and a trailing pipe are the row's own frame and produce no cell.
func splitTableCells(line string) []string {
	var (
		cells []string
		cur   strings.Builder
	)
	for i := 0; i < len(line); {
		switch line[i] {
		case '\\':
			if i+1 < len(line) && line[i+1] == '|' {
				cur.WriteString("\\|")
				i += 2
				continue
			}
		case '`':
			if span, next, ok := codeSpanAt(line, i); ok {
				cur.WriteString(span)
				i = next
				continue
			}
		case '|':
			cells = append(cells, cur.String())
			cur.Reset()
			i++
			continue
		}
		cur.WriteByte(line[i])
		i++
	}
	cells = append(cells, cur.String())
	// Drop the frame: an empty first cell when the row opened with a pipe,
	// an empty last one when it closed with one.
	if len(cells) > 0 && strings.TrimSpace(cells[0]) == "" && strings.HasPrefix(line, "|") {
		cells = cells[1:]
	}
	if len(cells) > 0 && strings.TrimSpace(cells[len(cells)-1]) == "" && strings.HasSuffix(line, "|") {
		cells = cells[:len(cells)-1]
	}
	return cells
}

// isCodeSpanCell reports a cell that is one inline code span and nothing else.
func isCodeSpanCell(src string) bool {
	if !strings.HasPrefix(src, "`") {
		return false
	}
	_, next, ok := codeSpanAt(src, 0)
	return ok && next == len(src)
}

// segsPlain is the text a run of segments measures as — the wrap-plain-then-
// style invariant's "plain".
func segsPlain(segs []segment) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.text)
	}
	return b.String()
}

// mdTableWidths computes the per-column widths for a table in avail cells, and
// reports whether an aligned table fits at all. A false means the stacked form.
func mdTableWidths(t *mdTableBlock, avail int) ([]int, bool) {
	n := len(t.align)
	if n == 0 {
		return nil, false
	}
	natural := make([]int, n)
	minimum := make([]int, n)
	rows := make([][]mdCell, 0, len(t.rows)+1)
	rows = append(rows, t.header)
	rows = append(rows, t.rows...)
	for _, row := range rows {
		for c, cell := range row {
			natural[c] = max(natural[c], cols(cell.plain))
			minimum[c] = max(minimum[c], cellMinWidth(cell))
		}
	}
	gaps := mdTableGap * (n - 1)
	sumNat, sumMin := gaps, gaps
	for c := range n {
		natural[c] = max(natural[c], 1)
		minimum[c] = max(min(minimum[c], natural[c]), 1)
		sumNat += natural[c]
		sumMin += minimum[c]
	}
	if sumNat <= avail {
		return natural, true
	}
	if sumMin > avail {
		return nil, false
	}
	// The surplus goes where the demand is, in proportion to it, rather than
	// to whichever column happens to come first.
	widths := append([]int(nil), minimum...)
	demand := make([]int, n)
	total := 0
	for c := range n {
		demand[c] = natural[c] - minimum[c]
		total += demand[c]
	}
	surplus := avail - sumMin
	if total == 0 {
		return widths, true
	}
	given := 0
	for c := range n {
		share := surplus * demand[c] / total
		widths[c] += share
		given += share
	}
	// The division's remainder, handed out one cell at a time to the columns
	// still short of their natural width. Left to right, so the outcome is
	// reproducible rather than dependent on a map's iteration order.
	for rest := surplus - given; rest > 0; {
		moved := false
		for c := range n {
			if rest == 0 {
				break
			}
			if widths[c] < natural[c] {
				widths[c]++
				rest--
				moved = true
			}
		}
		if !moved {
			break
		}
	}
	return widths, true
}

// cellMinWidth is the narrowest column a cell can live in: its longest
// unbreakable token, or the whole cell when it is a code span.
func cellMinWidth(c mdCell) int {
	if c.unbreakable {
		return cols(c.plain)
	}
	n := 1
	for _, w := range strings.Fields(c.plain) {
		n = max(n, cols(w))
	}
	return n
}

// renderMDTable draws a table, aligned when it fits and stacked when it does
// not.
func renderMDTable(t *mdTableBlock, width int) []string {
	if t == nil || len(t.align) == 0 {
		return nil
	}
	avail := max(width-cols(gutterNone), 1)
	widths, ok := mdTableWidths(t, avail)
	if !ok {
		return renderMDTableStacked(t, width)
	}
	out := make([]string, 0, len(t.rows)+2)
	out = append(out, mdTableRow(t.header, widths, t.align)...)
	// A per-column rule rather than a grid: the header stays legible with the
	// styling stripped, and no width is spent on vertical borders.
	rule := make([]string, len(widths))
	for c, w := range widths {
		rule[c] = strings.Repeat("─", w)
	}
	out = append(out, gutterNone+styleMDRule.Render(strings.Join(rule, strings.Repeat(" ", mdTableGap))))
	for _, row := range t.rows {
		out = append(out, mdTableRow(row, widths, t.align)...)
	}
	return out
}

// mdTableRow lays one row out. A row is as tall as its tallest cell and a
// wrapped cell's continuation stays inside its own column, which is the
// hanging alignment a wrapped table needs to stay readable.
func mdTableRow(row []mdCell, widths []int, align []mdAlign) []string {
	wrapped := make([][][]segment, len(widths))
	height := 1
	for c := range widths {
		var segs []segment
		if c < len(row) {
			segs = row[c].segs
		}
		wrapped[c] = wrapSegments(segs, widths[c])
		height = max(height, len(wrapped[c]))
	}
	out := make([]string, 0, height)
	for ln := range height {
		var b strings.Builder
		b.WriteString(gutterNone)
		for c, w := range widths {
			if c > 0 {
				b.WriteString(strings.Repeat(" ", mdTableGap))
			}
			var line []segment
			if ln < len(wrapped[c]) {
				line = wrapped[c][ln]
			}
			b.WriteString(padSegments(line, w, align[c]))
		}
		// Padding is plain, so trimming it cannot cut an escape sequence —
		// and a line that ended in spaces would hand them to the terminal's
		// own text selection.
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}

// padSegments renders one wrapped cell line into exactly width cells,
// positioned by the column's alignment. The padding is plain text and the runs
// are styled individually, which is the invariant restated at the one place a
// table could have broken it.
func padSegments(line []segment, width int, align mdAlign) string {
	var b strings.Builder
	n := 0
	for _, s := range line {
		n += cols(s.text)
	}
	pad := max(width-n, 0)
	left, right := 0, pad
	switch align {
	case mdAlignRight:
		left, right = pad, 0
	case mdAlignCenter:
		left, right = pad/2, pad-pad/2
	}
	b.WriteString(strings.Repeat(" ", left))
	for _, s := range line {
		b.WriteString(s.style.Render(s.text))
	}
	b.WriteString(strings.Repeat(" ", right))
	return b.String()
}

// wrapSegments word-wraps a run of segments to width cells, returning one
// list of styled runs per produced line. It is wrapLine's word loop without a
// gutter: measured on plain text, styled afterwards.
func wrapSegments(segs []segment, width int) [][]segment {
	width = max(width, 1)
	var (
		lines [][]segment
		cur   []segment
	)
	col := 0
	pendingSpace := false
	push := func(text string, st lipgloss.Style) {
		if n := len(cur); n > 0 && sameStyle(cur[n-1].style, st) {
			cur[n-1].text += text
			return
		}
		cur = append(cur, segment{text: text, style: st})
	}
	flush := func() {
		lines = append(lines, cur)
		cur = nil
		col = 0
		pendingSpace = false
	}
	for _, seg := range segs {
		for _, tok := range splitWords(sanitizeText(seg.text), width) {
			if tok == " " {
				pendingSpace = col > 0
				continue
			}
			need := cols(tok)
			if pendingSpace {
				need++
			}
			if col+need > width && col > 0 {
				flush()
				need = cols(tok)
			}
			if pendingSpace {
				push(" ", seg.style)
				pendingSpace = false
			}
			push(tok, seg.style)
			col += need
		}
	}
	if col > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

// renderMDTableStacked is the fallback: one record per row, `column: value`
// one pair per line.
//
// It is a record rather than a narrower table because below the minimum widths
// there is no table left — every column would be wrapping mid-token. The row
// boundary is a glyph and a blank line rather than a colour, so it survives a
// monochrome terminal and an empty cell, and the key is dim because it is the
// header repeating rather than the value the reader is after.
func renderMDTableStacked(t *mdTableBlock, width int) []string {
	keys := make([]string, len(t.header))
	for i, h := range t.header {
		keys[i] = h.plain
	}
	out := make([]string, 0, len(t.rows)*len(keys))
	for r, row := range t.rows {
		if r > 0 {
			out = append(out, "")
		}
		for c, cell := range row {
			marker := "  "
			if c == 0 {
				marker = mdBulletsByDepth[len(mdBulletsByDepth)-1]
			}
			key := ""
			if c < len(keys) {
				key = keys[c]
			}
			segs := []segment{{text: key + ": ", style: styleMDRef}}
			segs = append(segs, cell.segs...)
			out = append(out, wrapLine(paneLine{
				gutter:      gutterNone + marker,
				gutterStyle: styleMDMarker,
				segs:        segs,
			}, width)...)
		}
	}
	if len(out) == 0 {
		// A table with a header and no body rows still says what its columns
		// were.
		out = append(out, gutterNone+styleMDRef.Render(strings.Join(keys, ", ")))
	}
	return out
}
