package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The chat workspace's user turn: a right-aligned bubble, so a conversation
// reads as two voices rather than as one undifferentiated column.
//
// stylePrompt is a foreground accent, not a background band, which is what
// the original report asked for. Every style in this package is a 16-colour
// foreground; the one background here — styleSelected's grey 237 — means
// "this row is selected" and collapses to nothing at 16 colours. §15's Colour
// section requires the palette to degrade under NO_COLOR and at 16 colours,
// and the point of the bubble is a distinction that survives monochrome.
// Right alignment plus the marker on every line carry it with the colour
// stripped entirely; a band would either reuse the selection colour for
// something unselected or invent a palette role §15 does not have.
//
// It is composed from styleFocus's colour 6, bolded, rather than declared
// with a colour of its own: the accent already in the palette is the accent.
var stylePrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)

// promptMarker opens every line of the bubble, not just the first — it is the
// half of the distinction that survives with no colour at all, and a wrapped
// prompt whose continuations were unmarked would be indistinguishable from
// agent prose that happened to be indented.
const promptMarker = "› "

// promptBubbleNum and promptBubbleDen cap the bubble at two thirds of the
// pane. Wide enough that an ordinary question is not shredded into a column,
// narrow enough that the gutter on the left is unmistakably a gutter — a
// bubble allowed the full width is not a bubble.
const (
	promptBubbleNum = 2
	promptBubbleDen = 3
)

// promptBubbleLines draws a turn's prompt as a right-aligned bubble: shrink-
// to-fit, capped at promptBubbleNum/promptBubbleDen of the pane, wrapped
// inside itself, with promptMarker on every line. Agent output is untouched
// by this — it stays flush left at the full width.
//
// The prompt's own line breaks survive. The composer is multi-line by design
// (task 071 decision 4 keeps ↑/↓ for editing a draft), so a pasted snippet or
// a numbered list is prompt content, not incidental whitespace, and there is
// no line cap either: a truncated prompt puts the tail of what a human asked
// out of reach on a body that already scrolls. That is the call task 073
// decision 5 made when it dropped the 40-line cap from the §17 fallback.
//
// The text is plain while it is wrapped and measured, and each produced line
// is styled afterwards (task 050 decision 8, v0 T4.16): no break can land
// inside an escape sequence and no measurement ever sees one.
func promptBubbleLines(prompt string, width int) []string {
	if width <= 0 {
		return nil
	}
	marker := cols(promptMarker)
	// The bubble is at most the pane, however narrow the pane is: the cap is
	// a share of the width, and a share of a small width is smaller still.
	limit := min(width*promptBubbleNum/promptBubbleDen, width)
	inner := max(limit-marker, 1)

	wrapped := make([]string, 0, 4)
	for _, para := range strings.Split(normalizeBreaks(prompt), "\n") {
		if cols(para) <= inner {
			// An empty line stays an empty line: a blank between paragraphs
			// is how the human spaced what they wrote.
			wrapped = append(wrapped, para)
			continue
		}
		wrapped = append(wrapped, strings.Split(ansi.Wrap(para, inner, "-"), "\n")...)
	}

	// Shrink to fit: the bubble is as wide as its widest line, not as wide as
	// it was allowed to be.
	widest := 0
	for _, line := range wrapped {
		widest = max(widest, cols(line))
	}
	left := max(width-marker-widest, 0)
	pad := strings.Repeat(" ", left)

	out := make([]string, len(wrapped))
	for i, line := range wrapped {
		out[i] = pad + stylePrompt.Render(promptMarker+line)
	}
	return out
}

// normalizeBreaks folds the line endings a prompt can arrive with into "\n"
// and expands tabs, so every break the human typed becomes a line of the
// bubble and every remaining cell is one the width arithmetic can count. A
// literal tab is the one character whose rendered width is the terminal's
// business rather than ours.
func normalizeBreaks(s string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n", "\t", "    ").Replace(s)
}
