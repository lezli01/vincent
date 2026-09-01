package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The two halves of issue #299, both of them the same claim: a value longer
// than the pane is wide belongs on further rows of the pane, never off the
// side of it and never off the bottom. §15's row-height rule (task 052) and
// its code-block rule already say that for the surfaces that read text — "a
// cell too long for its column wraps onto further lines of the same row
// rather than being truncated away", "not truncation, which would put the
// tail of a long line out of reach of the TUI entirely" — and a field being
// typed into is the one surface where losing the tail also loses the cursor.

// panePad is the padding a value gets so it is unambiguously longer than the
// pane, whatever chrome the field draws around itself.
const panePad = 40

// countRune is how many times r survives into the drawn frame, ANSI stripped.
func countRune(frame string, r rune) int { return strings.Count(ansi.Strip(frame), string(r)) }

// TestPaneWidthChatComposerKeepsEveryWrappedRow is the reported symptom: a
// message longer than the composer is wide wraps inside the textarea, and
// the chat pane draws exactly one row of it.
//
// chatrender.go:228 appends `v.composer.View()` — three rows, `ta.SetHeight(3)`
// at chatview.go:136 — as a single element of a slice whose every other
// element is one line. render (chatrender.go:49-63) then counts it as one
// line in `room`, so the frame is two lines taller than the terminal it was
// given, and runs `ansi.Truncate(line, width, "…")` over all three rows at
// once: `ansi.StringWidth` never resets across the `\n`s, so the composer is
// measured as the sum of its rows and everything past the pane width — the
// remaining rows, newlines included — is dropped.
func TestPaneWidthChatComposerKeepsEveryWrappedRow(t *testing.T) {
	const (
		width  = 80
		height = 24
	)
	v := chatViewFixture()
	v.update(tea.WindowSizeMsg{Width: width, Height: height})

	// Long enough to wrap, short enough to still fit the composer's three
	// rows: what is asserted is that the wrapped rows are drawn, not that an
	// over-full composer scrolls.
	msg := strings.Repeat("Z", width+panePad)
	v.composer.SetValue(msg)

	frame := v.render(width, height)

	if got := len(strings.Split(frame, "\n")); got != height {
		t.Errorf("the chat frame is %d lines at height %d: the composer's rows are not counted in room, so its tail falls off the bottom of the terminal", got, height)
	}
	if got := countRune(frame, 'Z'); got != len(msg) {
		t.Errorf("%d of the %d typed characters reached the screen: the composer is truncated to its first row and the rest is typed blind", got, len(msg))
	}
	for i, line := range strings.Split(frame, "\n") {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("line %d is %d columns wide in a %d-column pane: %q", i, w, width, ansi.Strip(line))
		}
	}
}

// TestPaneWidthNewChatTitleWraps is the other half: every single-line row in
// the TUI is a `textinput.Model` that is never given a width, and bubbles/v2
// turns horizontal scrolling off entirely when `Width() <= 0`
// (textinput.go:356, `handleOverflow` returns early). The field draws its
// whole value on one row, so the row runs past the right edge of the pane —
// carrying the cursor with it — and whatever hosts the form either truncates
// it with `…` or lets the terminal reflow it.
//
// newchat.go's `title` is the field the reproduction names, and its render
// discards the width it is handed outright (`_ = width`, newchat.go:554).
func TestPaneWidthNewChatTitleWraps(t *testing.T) {
	const (
		width  = 80
		height = 24
	)
	f := newNewChatForm(nil, 0)
	title := strings.Repeat("Z", width+panePad)
	f.title.SetValue(title)

	frame := f.render(width, height)

	for i, line := range strings.Split(frame, "\n") {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("line %d is %d columns wide in a %d-column pane, so its tail and the cursor are off screen: %q",
				i, w, width, ansi.Strip(line))
		}
	}
	if got := countRune(frame, 'Z'); got != len(title) {
		t.Errorf("%d of the %d typed characters reached the screen, want the whole value wrapped onto further rows", got, len(title))
	}
	if got := len(strings.Split(frame, "\n")); got > height {
		t.Errorf("the form is %d lines at height %d", got, height)
	}
}
