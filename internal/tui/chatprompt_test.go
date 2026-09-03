package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The chat workspace's user turn is a right-aligned bubble and the agent's
// turn is not, which is the whole of the distinction: a conversation that
// drew both halves flush left at the full width read as one column of text
// with a `›` somewhere in it.
//
// Every assertion here is against ANSI-stripped output, because that is the
// requirement rather than a convenience: §15's Colour section has the palette
// degrading under NO_COLOR and at 16 colours, so the alignment and the marker
// have to carry the distinction on their own.

// bubbleBox is a bubble's left column, right column and marked-line count,
// measured in cells off the stripped lines.
func bubbleBox(t *testing.T, lines []string) (left, right, marked int) {
	t.Helper()
	left = -1
	for i, line := range plainLines(lines) {
		trimmed := strings.TrimLeft(line, " ")
		if !strings.HasPrefix(trimmed, promptMarker) {
			t.Errorf("bubble line %d is not marked with %q: %q", i, promptMarker, line)
			continue
		}
		marked++
		l := ansi.StringWidth(line) - ansi.StringWidth(trimmed)
		if left < 0 || l < left {
			left = l
		}
		right = max(right, ansi.StringWidth(line))
	}
	return left, right, marked
}

// TestChatPromptBubbleIsRightAlignedBoldAndMarked is the shape of the thing:
// flush against the right edge of the pane, bold, and carrying the marker on
// every line rather than only the first.
func TestChatPromptBubbleIsRightAlignedBoldAndMarked(t *testing.T) {
	const width = 60
	lines := promptBubbleLines("what changed in the scheduler?", width)
	if len(lines) != 1 {
		t.Fatalf("a prompt that fits produced %d lines: %q", len(lines), plainLines(lines))
	}
	left, right, marked := bubbleBox(t, lines)
	if right != width {
		t.Errorf("the bubble's right edge is at column %d in a %d-column pane, so it is not right-aligned: %q",
			right, width, plainLines(lines)[0])
	}
	if left <= 0 {
		t.Errorf("the bubble starts at column %d: there is no gutter left of it and nothing distinguishes it from agent prose", left)
	}
	if marked != len(lines) {
		t.Errorf("%d of %d bubble lines carry the marker", marked, len(lines))
	}
	// Bold as well as coloured: the colour is the part NO_COLOR takes away.
	if !strings.Contains(lines[0], "\x1b[1") {
		t.Errorf("the bubble is not bold: %q", lines[0])
	}
	if plain := plainLines(lines)[0]; !strings.Contains(plain, "what changed in the scheduler?") {
		t.Errorf("the prompt did not survive stripping: %q", plain)
	}
}

// TestChatPromptBubbleShrinksToFitAndCapsAtTwoThirds holds both halves of
// "shrink-to-fit, capped": a short prompt is as wide as it is, and a long one
// wraps inside the cap instead of growing to the pane.
func TestChatPromptBubbleShrinksToFitAndCapsAtTwoThirds(t *testing.T) {
	const width = 90
	limit := width * promptBubbleNum / promptBubbleDen

	short := "hi"
	lines := promptBubbleLines(short, width)
	left, right, _ := bubbleBox(t, lines)
	if got := right - left; got != ansi.StringWidth(promptMarker+short) {
		t.Errorf("a two-character prompt drew a %d-column bubble; it should shrink to fit, not stretch to the cap", got)
	}

	long := strings.Repeat("scheduler admission ", 12)
	lines = promptBubbleLines(long, width)
	if len(lines) < 2 {
		t.Fatalf("a %d-cell prompt in a %d-column pane produced %d lines: it did not wrap",
			ansi.StringWidth(long), width, len(lines))
	}
	left, right, marked := bubbleBox(t, lines)
	if got := right - left; got > limit {
		t.Errorf("the bubble is %d columns wide, past the %d-column cap in a %d-column pane", got, limit, width)
	}
	if right != width {
		t.Errorf("the wrapped bubble's right edge is at column %d, want the pane's %d", right, width)
	}
	if marked != len(lines) {
		t.Errorf("%d of %d wrapped lines carry the marker: a continuation with no marker is indistinguishable from agent prose", marked, len(lines))
	}
	for i, line := range plainLines(lines) {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("bubble line %d is %d columns in a %d-column pane: %q", i, w, width, line)
		}
	}
	// Nothing was dropped on the way in.
	var joined strings.Builder
	for _, line := range plainLines(lines) {
		joined.WriteString(strings.TrimPrefix(strings.TrimLeft(line, " "), promptMarker))
		joined.WriteString(" ")
	}
	if !strings.Contains(joined.String(), "admission") {
		t.Errorf("the wrapped prompt lost its words: %q", joined.String())
	}
}

// TestChatPromptBubbleKeepsItsOwnLineBreaks is the composer being multi-line
// by design (task 071 decision 4): a pasted snippet or a numbered list is
// prompt content, and the old call folded every break into a space.
func TestChatPromptBubbleKeepsItsOwnLineBreaks(t *testing.T) {
	lines := plainLines(promptBubbleLines("one\ntwo\n\nthree", 60))
	if len(lines) != 4 {
		t.Fatalf("a prompt with three breaks produced %d lines, want 4: %q", len(lines), lines)
	}
	for i, want := range []string{"one", "two", "", "three"} {
		got := strings.TrimPrefix(strings.TrimLeft(lines[i], " "), promptMarker)
		if got != want {
			t.Errorf("line %d is %q, want %q — the prompt's own breaks did not survive", i, got, want)
		}
	}
}

// TestChatPromptBubbleHasNoLineCap is the other half of the old call: six
// lines and an ellipsis put the tail of what a human asked out of reach on a
// body that already scrolls. Task 073 decision 5 made the same call for §17's
// retention fallback.
func TestChatPromptBubbleHasNoLineCap(t *testing.T) {
	rows := make([]string, 20)
	for i := range rows {
		rows[i] = "step " + strings.Repeat("x", i+1)
	}
	lines := plainLines(promptBubbleLines(strings.Join(rows, "\n"), 60))
	if len(lines) != len(rows) {
		t.Fatalf("a %d-line prompt rendered as %d lines", len(rows), len(lines))
	}
	last := strings.TrimPrefix(strings.TrimLeft(lines[len(lines)-1], " "), promptMarker)
	if last != rows[len(rows)-1] {
		t.Errorf("the last line of the prompt is %q, want %q", last, rows[len(rows)-1])
	}
	if strings.Contains(strings.Join(lines, "\n"), "…") {
		t.Errorf("a twenty-line prompt was cut with an ellipsis:\n%s", strings.Join(lines, "\n"))
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
	limit := width * promptBubbleNum / promptBubbleDen
	var flushLeft, wide int
	for _, line := range plainLines(v.bodyLines(width)) {
		if strings.Contains(line, "word word") {
			// The output pane's own gutter, not an alignment: a couple of
			// columns, nowhere near the third of the pane a bubble sits past.
			if indent := ansi.StringWidth(line) - ansi.StringWidth(strings.TrimLeft(line, " ")); indent <= 2 {
				flushLeft++
			}
			if ansi.StringWidth(line) > limit {
				wide++
			}
		}
	}
	if flushLeft == 0 {
		t.Error("no agent output line is flush left: the bubble's alignment leaked onto the agent's half of the turn")
	}
	if wide == 0 {
		t.Errorf("no agent output line is wider than %d columns: the agent's half was capped like the prompt's", limit)
	}
}

// TestChatPromptBubbleKeepsAPausedReadersPlace is the anchor claim. Prompt
// lines carry the zero anchor and cannot be anchored *to*; what matters is
// that the pad is by the bubble's actual height, so every record line below a
// prompt keeps the anchor it had and a growing prompt above the reader does
// not slide the pane out from under them (#291).
func TestChatPromptBubbleKeepsAPausedReadersPlace(t *testing.T) {
	const width, height = 60, 20
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

	// A multi-line prompt above the reader: the bubble grows by four lines,
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
