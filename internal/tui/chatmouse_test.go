package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// wheelTick builds one wheel event. Its coordinates are arbitrary on purpose:
// §15's Mouse rule scrolls the focused panel, not the hovered one (PR S), and
// the chat workspace has exactly one scrollable pane. A test that fed a tick
// at composer coordinates and expected no scroll would encode the position
// gate this design rejects.
func wheelTick(up bool) tea.MouseWheelMsg {
	button := tea.MouseWheelDown
	if up {
		button = tea.MouseWheelUp
	}
	return tea.MouseWheelMsg{X: 10, Y: 4, Button: button}
}

// scrollableChat is a chat long enough that its conversation does not fit the
// body, laid out once so the viewport has content to move over.
func scrollableChat(t *testing.T) *chatView {
	t.Helper()
	v := finishedChat(t, 9)
	v.fetchTranscripts()
	v.bodyDirty = true
	v.render(60, 14)
	if v.vp.AtTop() {
		t.Fatal("the fixture conversation fits its body; nothing here can scroll")
	}
	return v
}

// TestChatWheelScrollsOneLinePerTick is issue #300's first acceptance
// criterion: the wheel reaches the conversation, at the output pane's step.
func TestChatWheelScrollsOneLinePerTick(t *testing.T) {
	v := scrollableChat(t)
	before := v.vp.YOffset()

	v.update(wheelTick(true))
	if got := v.vp.YOffset(); got != before-1 {
		t.Fatalf("a wheel-up moved the body from %d to %d, want one line back", before, got)
	}
	v.update(wheelTick(false))
	if got := v.vp.YOffset(); got != before {
		t.Fatalf("a wheel-down moved the body to %d, want it back at %d", got, before)
	}
}

// TestChatWheelUpPausesFollowAndCtrlGReArmsIt holds the wheel to the same pair
// pgup is held to (§15 view 9): a manual scroll pauses follow, ctrl+g re-arms
// it and jumps to the end.
func TestChatWheelUpPausesFollowAndCtrlGReArmsIt(t *testing.T) {
	v := scrollableChat(t)
	if !v.following {
		t.Fatal("the fixture is not following; the pause has nothing to prove")
	}

	v.update(wheelTick(true))
	if v.following {
		t.Fatal("a wheel-up left the body following; a manual scroll pauses it")
	}

	v.updateKey(registryKey(t, "ctrl+g"))
	if !v.following {
		t.Fatal("ctrl+g did not re-arm follow after a wheel scroll")
	}
	if !v.vp.AtBottom() {
		t.Fatalf("ctrl+g re-armed follow at offset %d without jumping to the end", v.vp.YOffset())
	}
}

// TestChatWheelFetchesOlderTurnsWhenScrolledTo mirrors
// TestChatFetchesOlderTurnsWhenScrolledTo with wheel ticks in place of pgup:
// the two scroll routes fetch identically, so wheeling back into turns whose
// transcripts were never fetched fills them in rather than scrolling into
// blank space (task 071 decision 6).
func TestChatWheelFetchesOlderTurnsWhenScrolledTo(t *testing.T) {
	v := scrollableChat(t)
	if v.fetched[1] {
		t.Fatal("turn 1 was fetched eagerly; the lazy half has nothing to prove")
	}

	// One line per tick, so the wheel walks where pgup paged.
	for range 200 {
		if v.vp.AtTop() {
			break
		}
		v.update(wheelTick(true))
	}
	if !v.vp.AtTop() {
		t.Fatal("200 wheel ticks did not reach the top of a nine-turn conversation")
	}
	if !v.fetched[1] {
		t.Fatalf("wheeling to the top fetched %v, never turn 1", v.fetched)
	}
}

// TestChatWheelIgnoredBehindTheAnswerPopup: a popup owns the surface. PR S
// gave the palette and the §7.4 popup clicks; the wheel is the same rule.
func TestChatWheelIgnoredBehindTheAnswerPopup(t *testing.T) {
	v := scrollableChat(t)
	v.form = newAnswerForm(questionRequest())
	before := v.vp.YOffset()

	v.update(wheelTick(true))
	v.update(wheelTick(false))
	if got := v.vp.YOffset(); got != before {
		t.Fatalf("a wheel tick behind the popup moved the body from %d to %d", before, got)
	}

	// Closing it hands the wheel back, so the gate is not simply "nothing
	// scrolls in this fixture".
	v.form = nil
	v.update(wheelTick(true))
	if got := v.vp.YOffset(); got == before {
		t.Fatalf("the body stayed at %d with no popup open", got)
	}
}

// TestTaskWheelIgnoredBehindAPopup is the same rule on the task workspace,
// which had the gate on clicks (updateClick) and not on the wheel.
func TestTaskWheelIgnoredBehindAPopup(t *testing.T) {
	v := popupTaskView(t, func(d *detail) { d.form = newAnswerForm(questionRequest()) })
	v.tab = taskTabOutput
	v.detail.vp.SetWidth(60)
	v.detail.vp.SetHeight(5)
	v.detail.vp.SetContent(strings.Repeat("a line of output\n", 40))
	v.detail.vp.GotoBottom()
	before := v.detail.vp.YOffset()

	v.updateWheel(wheelTick(true))
	if got := v.detail.vp.YOffset(); got != before {
		t.Fatalf("a wheel tick behind the popup moved the output pane from %d to %d", before, got)
	}

	v.popup = false
	v.updateWheel(wheelTick(true))
	if got := v.detail.vp.YOffset(); got != before-1 {
		t.Fatalf("the output pane moved from %d to %d with no popup, want one line back", before, got)
	}
}

// TestChatRenderFillsExactlyItsHeight is the regression test for the footer
// clip: footerLines appended the composer as one element while bubbles pads
// its View to SetHeight(3), so render returned height+2 lines, the frame kept
// the first h-2, and the in-view hint line was never drawn.
func TestChatRenderFillsExactlyItsHeight(t *testing.T) {
	for _, height := range []int{10, 14, 24, 40} {
		v := finishedChat(t, 9)
		v.bodyDirty = true
		got := strings.Count(v.render(80, height), "\n") + 1
		if got != height {
			t.Errorf("render(80, %d) produced %d lines", height, got)
		}
	}
}

// TestChatFooterHintIsOnScreen is the other half of it: the hint naming the
// scroll keys is what the clip cost, and it is drawn inside the frame.
func TestChatFooterHintIsOnScreen(t *testing.T) {
	v := finishedChat(t, 9)
	v.bodyDirty = true
	// Framed the way root.framedView does it, because that is where the clip
	// happened: render was asked for h-2 lines, returned h, and frame kept
	// h-2 — dropping the hint off the bottom of the screen.
	const w, h = 200, 24
	got := ansi.Strip(frame(v.title(), v.render(w-2, h-2), w, h, true))
	for _, want := range []string{"enter send", "pgup/pgdown scroll", "ctrl+g live"} {
		if !strings.Contains(got, want) {
			t.Errorf("the footer hint lost %q on screen:\n%s", want, got)
		}
	}
}
