package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The chat workspace's user turn is a right-aligned card and the agent's turn
// is not, which is the whole of the distinction. Every assertion here is
// against ANSI-stripped output, because §15's Colour section has the palette
// degrading under NO_COLOR and at 16 colours: the border, the title and the
// alignment have to carry the distinction on their own.

// promptCardText is what a card's rows say, without the border.
func promptCardText(lines []string) []string {
	plain := plainLines(lines)
	out := make([]string, 0, len(plain))
	for _, line := range plain[1 : len(plain)-1] {
		line = strings.TrimSpace(line)
		line = strings.TrimSuffix(strings.TrimPrefix(line, "│"), "│")
		out = append(out, strings.TrimSpace(line))
	}
	return out
}

// The card is titled, dated, and flush against the pane's right edge.
func TestChatPromptCardIsRightAlignedAndTitled(t *testing.T) {
	const width = 60
	lines := promptCardLines("what changed in the scheduler?", "◷ 4m", width)
	plain := plainLines(lines)
	if len(plain) != 3 {
		t.Fatalf("a prompt that fits drew %d lines: %q", len(plain), plain)
	}
	for _, want := range []string{"You", "◷ 4m"} {
		if !strings.Contains(plain[0], want) {
			t.Errorf("the top edge is missing %q: %q", want, plain[0])
		}
	}
	for i, line := range plain {
		if got := ansi.StringWidth(line); got != width {
			t.Errorf("line %d ends at column %d, want the pane's %d: %q", i, got, width, line)
		}
		if !strings.HasPrefix(line, " ") {
			t.Errorf("line %d has no gutter left of the card: %q", i, line)
		}
	}
	if got := promptCardText(lines)[0]; got != "what changed in the scheduler?" {
		t.Errorf("the prompt reads %q", got)
	}
}

// A short prompt shrinks the card, and a long one wraps inside the cap.
func TestChatPromptCardShrinksToFitAndCapsAtTwoThirds(t *testing.T) {
	const width = 90
	limit := width * promptCardNum / promptCardDen
	cardWidth := func(lines []string) int {
		top := plainLines(lines)[0]
		return ansi.StringWidth(strings.TrimLeft(top, " "))
	}
	if got := cardWidth(promptCardLines("hi", "", width)); got != promptCardMinW {
		t.Errorf("a two-character prompt drew a %d-column card, want %d", got, promptCardMinW)
	}
	long := strings.Repeat("scheduler admission ", 12)
	lines := promptCardLines(long, "", width)
	if len(lines) < 4 {
		t.Fatalf("a long prompt did not wrap: %q", plainLines(lines))
	}
	if got := cardWidth(lines); got > limit {
		t.Errorf("the card is %d columns wide, past the %d-column cap", got, limit)
	}
	if joined := strings.Join(promptCardText(lines), " "); strings.Count(joined, "admission") != 12 {
		t.Errorf("the wrapped prompt lost its words: %q", joined)
	}
}

// The composer is multi-line by design (task 071 decision 4): a pasted snippet
// or a numbered list is prompt content.
func TestChatPromptCardKeepsItsOwnLineBreaks(t *testing.T) {
	got := promptCardText(promptCardLines("one\ntwo\n\nthree", "", 60))
	want := []string{"one", "two", "", "three"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("the rows are %q, want %q", got, want)
	}
}

// A truncated prompt puts the tail of what a human asked out of reach on a body
// that already scrolls (task 073 decision 5).
func TestChatPromptCardHasNoLineCap(t *testing.T) {
	rows := make([]string, 20)
	for i := range rows {
		rows[i] = "step " + strings.Repeat("x", i+1)
	}
	got := promptCardText(promptCardLines(strings.Join(rows, "\n"), "", 60))
	if len(got) != len(rows) || got[len(got)-1] != rows[len(rows)-1] {
		t.Fatalf("a %d-line prompt rendered as %d rows ending %q", len(rows), len(got), got[len(got)-1])
	}
}

// A pane narrower than the card's floor still gets a card that fits it.
func TestChatPromptCardFitsANarrowPane(t *testing.T) {
	for _, line := range plainLines(promptCardLines("a question", "◷ 4m", 10)) {
		if got := ansi.StringWidth(line); got != 10 {
			t.Errorf("a line is %d wide in a 10-column pane: %q", got, line)
		}
	}
}

// TestChatAgentOutputStaysFlushLeftAndFullWidth is the control: only the
// prompt half of a turn changed.
func TestChatAgentOutputStaysFlushLeftAndFullWidth(t *testing.T) {
	const width = 60
	v := finishedChat(t, 1)
	v.applyTranscript(chatTranscriptMsg{chatID: 1, seq: 1, records: []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: strings.Repeat("word ", 40)},
	}})
	limit := width * promptCardNum / promptCardDen
	var flushLeft, wide int
	for _, line := range plainLines(v.bodyLines(width)) {
		if strings.Contains(line, "word word") {
			// The output pane's own gutter, not an alignment: a couple of
			// columns, nowhere near the third of the pane a card sits past.
			if indent := ansi.StringWidth(line) - ansi.StringWidth(strings.TrimLeft(line, " ")); indent <= 2 {
				flushLeft++
			}
			if ansi.StringWidth(line) > limit {
				wide++
			}
		}
	}
	if flushLeft == 0 {
		t.Error("no agent output line is flush left: the card's alignment leaked onto the agent's half of the turn")
	}
	if wide == 0 {
		t.Errorf("no agent output line is wider than %d columns: the agent's half was capped like the prompt's", limit)
	}
}

// TestChatPromptCardKeepsAPausedReadersPlace is the anchor claim. Prompt
// lines carry the zero anchor and cannot be anchored *to*; what matters is
// that the pad is by the card's actual height, so every record line below a
// prompt keeps the anchor it had and a growing prompt above the reader does
// not slide the pane out from under them (#291).
func TestChatPromptCardKeepsAPausedReadersPlace(t *testing.T) {
	// 19 rows: the workspace's own hint line is gone (issue #801 F5), and
	// at 20 the whole conversation fits with nothing to page back to.
	const width, height = 60, 19
	v := finishedChat(t, 3)
	for seq := 1; seq <= 3; seq++ {
		recs := make([]apiclient.TranscriptRecord, 6)
		for i := range recs {
			recs[i] = apiclient.TranscriptRecord{
				Type: "agent.output",
				Text: "turn " + string(rune('0'+seq)) + " line " + string(rune('0'+i)),
			}
		}
		v.applyTranscript(chatTranscriptMsg{chatID: 1, seq: seq, records: recs})
	}
	v.render(width, height)
	v.updateKey(registryKey(t, "pgup"))
	if v.following {
		t.Fatal("pgup did not pause the pane")
	}
	top := func() string {
		body := plainLines(strings.Split(v.bodyView(width, height-8), "\n"))
		return strings.TrimSpace(body[0])
	}
	before := top()

	// A multi-line prompt above the reader: the card grows by four lines,
	// and a pad that still counted one would shift every anchor below it.
	v.turns[0].Prompt = "one\ntwo\nthree\nfour\nfive"
	v.bodyDirty = true
	v.render(width, height)
	if after := top(); after != before {
		t.Errorf("the paused pane moved from %q to %q when a prompt above it grew", before, after)
	}

	// And across a plain rebuild at the same width, which is what a level or
	// raw toggle is.
	v.bodyDirty = true
	v.render(width, height)
	if after := top(); after != before {
		t.Errorf("a rebuild moved the paused pane from %q to %q", before, after)
	}
}
