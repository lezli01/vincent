package tui

import "time"

// The in-progress indicator (issue #330, task 089): one moving glyph and one
// advancing clock, shown for as long as a chat turn or a step attempt is in
// `running`.
//
// It is a pure renderer and not a `bubbles/spinner`: that widget is a
// tea.Model with a ticker and a message identity of its own, so the three
// views that want this would each need one of each, and routing three tick
// identities is more machinery than a frame table deserves. A function of
// (frame, elapsed) is how outputlines.go, formatElapsed and every other
// renderer in this package are built, and it is table-testable without
// running a program.
//
// It is exported for exactly one caller outside this package —
// `vincent chat send`, which waits on the same running turn and must not
// carry a second copy of the frames or a second spelling of the duration
// (internal/cli/chat.go). `cli` → `tui` is the sanctioned direction.

// SpinnerTick is how often the frame advances.
//
// It is deliberately not the board's one-second elapsedTick: a glyph that
// moves once per second is the frozen screen this indicator exists to rule
// out. The cost is affordable because the expensive work is already gated —
// chatView.bodyView rebuilds only on bodyDirty or a width change, and
// detail.renderOutputPane only on outputDirty — so a tick that changes
// nothing but the frame repaints without re-rendering any Markdown.
const SpinnerTick = 120 * time.Millisecond

// spinnerFrames is the braille cycle, used unconditionally on all three
// platforms. There is no ASCII fallback because there is nothing to switch on:
// a program can probe whether it is attached to a terminal, never whether that
// terminal has a glyph. This TUI already draws `⏸`, `▸`, `▾` and the box
// characters around this indicator on the same terms.
var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerFrame is the glyph for a frame counter, which wraps and tolerates a
// negative value rather than panicking on one.
func spinnerFrame(frame int) string {
	n := len(spinnerFrames)
	return spinnerFrames[((frame%n)+n)%n]
}

// ProgressLabel renders the indicator: the frame, the word, and how long the
// turn or attempt has been running.
//
// The elapsed half is load-bearing on its own. A number that advances is proof
// of life in a screenshot or a scrollback, where an animation is not, and it
// answers "how long has this been going" without a second affordance. It is
// spelled with formatElapsed — `14s`, `2m03s`, `1h04m` — because this TUI has
// one duration vocabulary and a second spelling of the same fact across three
// views is worth more than matching the `0:14` in the issue body.
//
// A negative duration clamps to zero: `now` and the daemon's clock are two
// clocks, and a turn that appears to have started in the future must read as
// having just started, not as `-1s`.
func ProgressLabel(frame int, since time.Duration) string {
	if since < 0 {
		since = 0
	}
	return spinnerFrame(frame) + " working… " + formatElapsed(since)
}
