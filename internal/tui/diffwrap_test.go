package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// longLineDiff is one file whose only added line is 300 columns wide and ends
// in a marker no narrower pane can show without panning.
func longLineDiff() string {
	long := "+" + strings.Repeat("x", 290) + "END-MARKER"
	return "diff --git a/wide.go b/wide.go\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/wide.go\n" +
		"+++ b/wide.go\n" +
		"@@ -0,0 +1 @@\n" +
		long + "\n"
}

// TestDiffPanReachesTheEndOfALongLine (#799 W9): the plain arrows are fold
// keys, so the end of a line wider than the pane is reached with shift+→, and
// shift+← brings the pane back to the gutter.
func TestDiffPanReachesTheEndOfALongLine(t *testing.T) {
	p := loadedDiff(t, longLineDiff())
	diffPress(p, "right") // open the file
	if strings.Contains(diffView(p, 20), "END-MARKER") {
		t.Fatal("the end of a 300-column line is visible in an 80-column pane before panning")
	}
	right := tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift}
	if right.String() != "shift+right" {
		t.Fatalf("the shifted arrow reads as %q", right.String())
	}
	for range 300 / diffPanStep {
		p.updateKey(right)
	}
	if !strings.Contains(diffView(p, 20), "END-MARKER") {
		t.Fatalf("the end of the line is not reachable with shift+right:\n%s", diffView(p, 20))
	}
	if !p.open[p.cursorPath] {
		t.Error("panning folded the file shut")
	}
	for range 300 / diffPanStep {
		p.updateKey(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
	}
	if got := p.vp.XOffset(); got != 0 {
		t.Errorf("shift+left back from the end left the pane at column %d", got)
	}
}

// TestDiffNeverFetchedSaysHowToLoad (#799 W9): the never-fetched state names
// the action that loads the diff, and that action is not `d`.
func TestDiffNeverFetchedSaysHowToLoad(t *testing.T) {
	p := newDiffPane()
	p.openTask(1)
	got := diffView(&p, 10)
	if strings.Contains(got, "press d") {
		t.Errorf("the empty state still says to press d: %q", got)
	}
	if !strings.Contains(got, "reopen the tab") {
		t.Errorf("the empty state = %q, want it to say how the diff loads", got)
	}
}

var diffAsOf = regexp.MustCompile(`as of \d\d:\d\d:\d\d — reopen the tab to refresh`)

// TestDiffDrawsAFreshnessCue (#799 W9): the pane follows no events, so it
// says when its content was fetched and how to refresh it — over a diff and
// over an empty one alike, and not before anything has been fetched.
func TestDiffDrawsAFreshnessCue(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)
	if got := diffView(p, 10); !diffAsOf.MatchString(got) {
		t.Errorf("a loaded diff draws no freshness cue:\n%s", got)
	}
	empty := loadedDiff(t, "")
	if got := diffView(empty, 10); !diffAsOf.MatchString(got) || !strings.Contains(got, "no changes") {
		t.Errorf("an empty diff draws no freshness cue:\n%s", got)
	}
	never := newDiffPane()
	never.openTask(1)
	if got := diffView(&never, 10); strings.Contains(got, "as of") {
		t.Errorf("a never-fetched pane claims a fetch time: %q", got)
	}
	p.openTask(2)
	if !p.asOf.IsZero() {
		t.Error("another task's fetch time survived switching tasks")
	}
}

// TestDiffFreshnessCueYieldsToTheTotals: on a pane too narrow for the whole
// cue the summary keeps its totals and the cue shrinks or goes, rather than
// wrapping the pinned line.
func TestDiffFreshnessCueYieldsToTheTotals(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)
	for _, w := range []int{30, 40, 60, 80} {
		head := ansi.Strip(strings.Split(p.render(w, 10), "\n")[0])
		if got := ansi.StringWidth(head); got > w {
			t.Errorf("at width %d the summary line is %d columns wide", w, got)
		}
		if !strings.Contains(head, "+3") {
			t.Errorf("at width %d the totals are gone: %q", w, head)
		}
	}
}
