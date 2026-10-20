package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// wrapOpts shapes what wrapText lays out around the text (task 135.1).
type wrapOpts struct {
	// prefix leads the first line — a label, a bullet, an indent — and is
	// counted in display cells against the width like the text after it.
	prefix string
	// indent is the hanging indent, in cells, of every line after the
	// first: set it to the prefix's width to align continuation lines under
	// the text rather than under the label.
	indent int
	// maxLines caps the result, zero meaning no cap. A capped result ends
	// on "…" so a cut is never mistaken for the whole text.
	maxLines int
}

// wrapText lays plain text out in lines of at most width display cells: the
// one wrap of the TUI (task 135.1, spec §15 "Text fields wrap"). A view wraps
// its prose here and styles the lines it gets back — wrap plain text, then
// style (task 050 decision 8) — so a break can never land inside an escape
// sequence, and nothing is left for frame() to cut on its "…".
//
// Paragraphs ("\n") and blank lines are kept; words wrap by display width,
// wide runes and emoji included; a token longer than the room left is
// hard-split by cell. It returns lines, never a joined string: a "\n" inside
// a measured line is the width hazard #299 fixed.
//
// The width is honoured all the way down (decision 1): it is clamped to 1,
// never floored at a comfortable minimum, because a floor only handed the
// overflow to frame(). The one line that can exceed it is a single grapheme
// wider than the width itself — a wide rune in a one-cell column.
//
// No line ends in whitespace: a blank paragraph is the lead it would have had
// with its trailing spaces trimmed, so an indent alone never makes a line.
func wrapText(text string, width int, opts wrapOpts) []string {
	width = max(width, 1)
	hang := strings.Repeat(" ", max(opts.indent, 0))
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(para) == "" {
			lead := hang
			if len(out) == 0 {
				lead = opts.prefix
			}
			out = append(out, strings.TrimRight(lead, " "))
			continue
		}
		out = wrapParagraph(out, para, width, opts.prefix, hang)
	}
	if opts.maxLines > 0 && len(out) > opts.maxLines {
		out = out[:opts.maxLines]
		last := strings.TrimRight(out[len(out)-1], " ")
		if cols(last)+1 > width {
			last = ansi.Truncate(last, width-1, "")
		}
		out[len(out)-1] = last + "…"
	}
	return out
}

// wrapParagraph appends one paragraph's lines to out, the very first line of
// the text led by prefix and every other by hang.
func wrapParagraph(out []string, para string, width int, prefix, hang string) []string {
	var cur strings.Builder
	start := func() int {
		cur.Reset()
		l := hang
		if len(out) == 0 {
			l = prefix
		}
		cur.WriteString(l)
		return cols(l)
	}
	col := start()
	room := func(c int) int { return max(width-c, 1) }
	fresh := true // nothing but the lead on the line yet
	pendingSpace := false
	for _, word := range strings.Fields(para) {
		if !fresh {
			pendingSpace = true
		}
		need := cols(word)
		if pendingSpace {
			need++
		}
		if !fresh && col+need > width {
			out = append(out, cur.String())
			col, pendingSpace = start(), false
		}
		if pendingSpace {
			cur.WriteString(" ")
			col++
			pendingSpace = false
		}
		// A word wider than what is left of a fresh line is hard-split
		// across as many lines as it takes.
		for cols(word) > room(col) {
			head := cutCells(word, room(col))
			rest := ansi.TruncateLeft(word, cols(head), "")
			if rest == "" {
				break // one grapheme wider than the room: written whole below
			}
			cur.WriteString(head)
			out = append(out, cur.String())
			col = start()
			word = rest
		}
		cur.WriteString(word)
		col += cols(word)
		fresh = false
	}
	return append(out, cur.String())
}

// cutCells is the head of s that fits n cells, cut by grapheme (task 073
// decision 2) so a ZWJ emoji is never split into its parts. A first grapheme
// wider than n is returned whole: there is no narrower way to show it.
func cutCells(s string, n int) string {
	for w := n; w <= cols(s); w++ {
		if head := ansi.Cut(s, 0, w); head != "" {
			return head
		}
	}
	return s
}

// hanging is the options for a line led by prefix with every later line
// aligned under the text after it: a label's value, an indented block.
func hanging(prefix string) wrapOpts {
	return wrapOpts{prefix: prefix, indent: cols(prefix)}
}

// styled renders each wrapped line in style — the "then style" half of
// wrapping plain text first.
func styled(lines []string, style lipgloss.Style) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = style.Render(l)
	}
	return out
}
