package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func numbered(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	return lines
}

// Each of the pane's own keys and the wheel moves the window, clamped to the
// content (task 135.1).
func TestScrollPaneKeys(t *testing.T) {
	p := newScrollPane(20, 5, true) // 4 rows of content and the cue
	p.SetContentLines(numbered(20))
	for _, step := range []struct {
		msg  tea.Msg
		want int
	}{
		{tea.KeyPressMsg{Code: tea.KeyDown}, 1},
		{tea.KeyPressMsg{Code: tea.KeyUp}, 0},
		{tea.KeyPressMsg{Code: tea.KeyUp}, 0},
		{tea.KeyPressMsg{Code: tea.KeyPgDown}, 3},
		{tea.KeyPressMsg{Code: tea.KeyPgUp}, 0},
		{tea.KeyPressMsg{Code: tea.KeyEnd}, 16},
		{tea.KeyPressMsg{Code: tea.KeyDown}, 16},
		{tea.KeyPressMsg{Code: tea.KeyHome}, 0},
		{tea.MouseWheelMsg{Button: tea.MouseWheelDown}, wheelStep},
		{tea.MouseWheelMsg{Button: tea.MouseWheelUp}, 0},
	} {
		if !p.Update(step.msg) {
			t.Fatalf("%v was not consumed", step.msg)
		}
		if got := p.Offset(); got != step.want {
			t.Fatalf("after %v the offset is %d, want %d", step.msg, got, step.want)
		}
	}
	view := p.View()
	if len(view) != 5 || view[0] != "line 0" || !strings.Contains(ansi.Strip(view[4]), "↓ more · lines 1–4 of 20") {
		t.Errorf("view at the top:\n%s", strings.Join(view, "\n"))
	}
}

// The app's keys are not the pane's: the viewport keymap's j/k/f/b/u/d are
// never consumed, so a surface's own bindings keep working inside one.
func TestScrollPaneLeavesAppKeys(t *testing.T) {
	p := newScrollPane(20, 5, true)
	p.SetContentLines(numbered(20))
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	for _, k := range []string{"j", "k", "f", "b", "u", "d", "space"} {
		if p.Update(key(k)) {
			t.Errorf("%q was consumed", k)
		}
	}
	if p.Offset() != 1 {
		t.Errorf("an app key moved the window to %d", p.Offset())
	}
}

// The cue is only there when there is something to say: content that fits
// gets every row and no cue.
func TestScrollPaneCueOnlyWhenClipped(t *testing.T) {
	p := newScrollPane(20, 5, true)
	p.SetContentLines(numbered(5))
	if view := p.View(); len(view) != 5 || strings.Contains(ansi.Strip(strings.Join(view, "\n")), "more") {
		t.Errorf("content that fits drew a cue:\n%s", strings.Join(view, "\n"))
	}
	p.SetContentLines(numbered(6))
	view := p.View()
	if len(view) != 5 || !strings.Contains(ansi.Strip(view[4]), "lines 1–4 of 6") {
		t.Errorf("clipped content drew no cue:\n%s", strings.Join(view, "\n"))
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if cue := ansi.Strip(p.View()[4]); !strings.Contains(cue, "↑ more · lines 3–6 of 6") {
		t.Errorf("cue at the end: %q", cue)
	}
}

// A refresh that inserts lines above the reader keeps the window on the line
// they were reading; one that drops the anchor keeps the offset instead.
func TestScrollPaneHoldsTheAnchor(t *testing.T) {
	p := newScrollPane(20, 4, false)
	lines := numbered(20)
	p.SetContentKeyed(lines, lines)
	p.EnsureVisible(10, 10)
	top := p.View()[0]

	grown := append([]string{"new a", "new b", "new c"}, lines...)
	p.SetContentKeyed(grown, grown)
	if got := p.View()[0]; got != top {
		t.Errorf("after an insert above, the top line is %q, want %q", got, top)
	}

	off := p.Offset()
	replaced := numbered(30)
	keys := make([]string, len(replaced))
	p.SetContentKeyed(replaced, keys)
	if p.Offset() != off {
		t.Errorf("an unanchored refresh moved the window from %d to %d", off, p.Offset())
	}
}

// EnsureVisible scrolls the least it takes, favouring the first line.
func TestScrollPaneEnsureVisible(t *testing.T) {
	p := newScrollPane(20, 4, false)
	p.SetContentLines(numbered(20))
	p.EnsureVisible(5, 6)
	if p.Offset() != 3 {
		t.Errorf("rows 5–6 below the window: offset %d, want 3", p.Offset())
	}
	p.EnsureVisible(4, 4)
	if p.Offset() != 3 {
		t.Errorf("a visible row moved the window to %d", p.Offset())
	}
	p.EnsureVisible(1, 9)
	if p.Offset() != 1 {
		t.Errorf("a block taller than the pane: offset %d, want its first row at the top", p.Offset())
	}
}
