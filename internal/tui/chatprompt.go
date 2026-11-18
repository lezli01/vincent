package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The chat workspace's user turn: a right-aligned card titled You, so a
// conversation reads as two voices rather than as one undifferentiated column
// (task 150.5). The border, the title and the alignment carry the distinction
// with the colour stripped entirely, as §15's Colour section requires.

// promptCardNum and promptCardDen cap the card at two thirds of the pane. Wide
// enough that an ordinary question is not shredded into a column, narrow enough
// that the gutter on the left is unmistakably a gutter.
const (
	promptCardNum = 2
	promptCardDen = 3
)

// promptCardMinW keeps the card wide enough for its title and age.
const promptCardMinW = 16

// promptCardLines draws a turn's prompt as a right-aligned card: shrink-to-fit,
// capped at promptCardNum/promptCardDen of the pane, wrapped inside itself, with
// how long ago it was sent on its top edge. Agent output is untouched by this —
// it stays flush left at the full width.
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
func promptCardLines(prompt, age string, width int) []string {
	if width <= 0 {
		return nil
	}
	// The card is at most the pane, however narrow the pane is: the cap is a
	// share of the width, and a share of a small width is smaller still.
	limit := min(max(width*promptCardNum/promptCardDen, promptCardMinW), width)
	inner := max(limit-promptCardChrome, 1)

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

	// Shrink to fit: the card is as wide as its widest line, not as wide as it
	// was allowed to be.
	widest := 0
	for _, line := range wrapped {
		widest = max(widest, cols(line))
	}
	cardW := min(max(widest+promptCardChrome, promptCardMinW), width)
	right := ""
	if age != "" {
		right = styleDim.Render(age)
	}
	card := drawBox(toneBlue.Bold(true).Render("You"), right, wrapped, cardW, len(wrapped)+2, edgesRounded, toneBlue)
	pad := strings.Repeat(" ", width-cardW)
	for i, line := range card {
		card[i] = pad + line
	}
	return card
}

// promptCardChrome is the columns the card's border and padding take.
const promptCardChrome = 4

// normalizeBreaks folds the line endings a prompt can arrive with into "\n"
// and expands tabs, so every break the human typed becomes a line of the
// card and every remaining cell is one the width arithmetic can count. A
// literal tab is the one character whose rendered width is the terminal's
// business rather than ours.
func normalizeBreaks(s string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n", "\t", "    ").Replace(s)
}
