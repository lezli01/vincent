package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The diff tab inside the real shell (task 012): the surface it claims for the
// footer and the help, and the mouse acting on the pane the eye is on.

// diffShell is a shell with a task open, the output pane focused, and the diff
// tab live with a two-file diff in it.
func diffShell(t *testing.T) *shell {
	t.Helper()
	s, _ := newShellFixture(t, task(1, stateRunning))
	s.settle()
	s.focus = panelOutput
	s.syncDetailFocus()
	if cmd := s.detail.toggleTab(); cmd != nil {
		// The fetch this returns wants a daemon; the fixture supplies the
		// answer directly instead.
		_ = cmd
	}
	s.detail.diff.apply(diffLoadedMsg{taskID: s.detail.taskID, text: twoFileDiff})
	s.render(120, 37)
	return s
}

// TestDiffTabIsItsOwnSurface: the two tabs answer to different keys, so a
// footer still offering "↑/↓ scroll" over a list of files would be a footer
// that lies.
func TestDiffTabIsItsOwnSurface(t *testing.T) {
	s := diffShell(t)
	if got := s.focusedContext(); got != ctxDiff {
		t.Fatalf("focused context = %q on the diff tab, want %q", got, ctxDiff)
	}
	line := ansi.Strip(renderFooter(120, bindingsFor(ctxDiff), s.bar, s.board.target(), 0, false))
	for _, want := range []string{"files", "fold"} {
		if !strings.Contains(line, want) {
			t.Errorf("the diff footer does not mention %q:\n%s", want, line)
		}
	}
	if help := helpText(ctxDiff); !strings.Contains(help, "expand every file") ||
		!strings.Contains(help, "approve the gate") {
		t.Errorf("? on the diff tab lost the tab's own keys or the task's actions:\n%s", help)
	}

	// Back on the output tab the surface is the output again.
	s.detail.toggleTab()
	if got := s.focusedContext(); got != ctxOutput {
		t.Fatalf("focused context = %q back on the output tab, want %q", got, ctxOutput)
	}
}

// TestDiffClickFoldsThroughTheShell derives the row from the rendered frame
// rather than from the arithmetic the click path uses, so the two cannot agree
// on a wrong answer — the mistake TestShellClickSelectsTheRowItPointsAt was
// written for.
func TestDiffClickFoldsThroughTheShell(t *testing.T) {
	s := diffShell(t)
	lines := strings.Split(s.render(120, 37), "\n")
	y := -1
	for i, line := range lines {
		if strings.Contains(ansi.Strip(line), diffFoldClosed+" README.md") {
			y = i
		}
	}
	if y < 0 {
		t.Fatalf("no folded README.md row in the frame:\n%s", strings.Join(lines, "\n"))
	}

	s.update(tea.MouseClickMsg{X: s.lastBoxes[2].x + 4, Y: y, Button: tea.MouseLeft})
	if !s.detail.diff.open["README.md"] {
		t.Fatalf("clicking the README.md header did not expand it (folds %v)", s.detail.diff.open)
	}
	if !strings.Contains(ansi.Strip(s.render(120, 37)), "one more") {
		t.Error("the expanded file's body is not on screen")
	}
}

// TestDiffWheelScrollsTheDiff: whichever tab the pane shows is the one the
// wheel moves. It used to scroll the output tail underneath, which nobody
// looking at a diff can see.
func TestDiffWheelScrollsTheDiff(t *testing.T) {
	s := diffShell(t)
	// A file long enough that the pane has somewhere to scroll to.
	long := "diff --git a/long.go b/long.go\n--- a/long.go\n+++ b/long.go\n@@ -1,100 +1,100 @@\n" +
		strings.Repeat("+a line\n", 100)
	s.detail.diff.apply(diffLoadedMsg{taskID: s.detail.taskID, text: long})
	s.detail.diff.foldAll(true)
	s.render(120, 37)

	before := s.detail.diff.vp.YOffset()
	s.update(tea.MouseWheelMsg{X: s.lastBoxes[2].x + 4, Y: s.lastBoxes[2].y + 2, Button: tea.MouseWheelDown})
	s.render(120, 37)
	if s.detail.diff.vp.YOffset() == before {
		t.Fatalf("the wheel left the diff at offset %d", before)
	}
	if !s.detail.following {
		t.Error("the wheel dropped the output tab's follow while the diff was on screen")
	}
}
