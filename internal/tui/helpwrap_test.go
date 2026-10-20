package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// assertNoFrameClip fails for every framed line that ends on frame()'s
// ellipsis: the §15 wrap-or-truncate convention (task 135.1) makes that
// cut a last-resort guard, so a screen that reaches it left prose unwrapped.
// Each screen issue under task 135 reuses it on its own surface.
func assertNoFrameClip(t *testing.T, screen string) {
	t.Helper()
	for i, line := range strings.Split(ansi.Strip(screen), "\n") {
		if strings.Contains(line, "…│") {
			t.Errorf("line %d was cut by frame(): %q", i, line)
		}
	}
}

// At 80×24 the help sheet wraps every label rather than leaving it to the
// frame, on the board and in the workspace, at every scroll position.
func TestHelpSheetNeverClipsAt80x24(t *testing.T) {
	m := connectedRoot(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(key("?"))
	if !m.help {
		t.Fatal("? did not open help")
	}
	for range 40 {
		assertNoFrameClip(t, content(m))
		m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	assertNoFrameClip(t, content(m))
}

// A narrow sheet wraps a label under its own column, never under the key;
// the column stops at its cap, and a key wider than that takes a line of its
// own with the label under it.
func TestHelpLabelsHangAtTheLabelColumn(t *testing.T) {
	lines := layoutHelp([]helpSection{{title: "T", rows: []helpRow{
		{key: "x", label: "one two three four five six seven"},
		{key: "ctrl+shift+alt+x", label: "a long chord"},
	}}}, 24)
	want := []string{
		"", " T", "",
		"  x           one two",
		"              three four",
		"              five six",
		"              seven",
		"  ctrl+shift+alt+x",
		"              a long",
		"              chord",
	}
	got := make([]string, len(lines))
	for i, l := range lines {
		got[i] = ansi.Strip(l)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("layout:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The footer fits its width with the close keys whole: the description goes
// first.
func TestHelpFooterFits(t *testing.T) {
	for _, w := range []int{60, 80, 100} {
		f := ansi.Strip(helpFooter(w))
		if c := ansi.StringWidth(f); c > w {
			t.Errorf("width %d: the footer is %d cells: %q", w, c, f)
		}
		if !strings.Contains(f, "esc close") || !strings.Contains(f, "? close") {
			t.Errorf("width %d: a close key was cut: %q", w, f)
		}
	}
	if f := ansi.Strip(helpFooter(140)); !strings.Contains(f, "the keys of the surface") {
		t.Errorf("a wide footer shed its description: %q", f)
	}
}

// The wheel scrolls the sheet while it is open (it was dropped before).
func TestHelpWheelScrolls(t *testing.T) {
	m := connectedRoot(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 16})
	m.Update(key("?"))
	m.Update(tea.MouseWheelMsg{X: 10, Y: 5, Button: tea.MouseWheelDown})
	if m.helpPane.Offset() != wheelStep {
		t.Errorf("a wheel notch scrolled help to %d, want %d", m.helpPane.Offset(), wheelStep)
	}
	m.Update(tea.MouseWheelMsg{X: 10, Y: 5, Button: tea.MouseWheelUp})
	if m.helpPane.Offset() != 0 {
		t.Errorf("wheel up left help at %d", m.helpPane.Offset())
	}
}

// wheelSpy records the wheel events a view is delivered.
type wheelSpy struct{ got []tea.MouseWheelMsg }

func (w *wheelSpy) title() string { return "spy" }

func (w *wheelSpy) update(msg tea.Msg) (panel, tea.Cmd) {
	if wm, ok := msg.(tea.MouseWheelMsg); ok {
		w.got = append(w.got, wm)
	}
	return w, nil
}

func (w *wheelSpy) render(_, _ int) string { return "" }

// The wheel's Y reaches a view in body coordinates, offset by every header
// line the way a click's is — the status line included, which the fixed
// offset of 1 it used to subtract did not know about.
func TestWheelYIsOffsetByTheChrome(t *testing.T) {
	m := connectedRoot(t)
	m.keysNotice = "a status line"
	if m.chromeTop() != 2 {
		t.Fatalf("chromeTop = %d with the status line up, want 2", m.chromeTop())
	}
	spy := &wheelSpy{}
	m.views[m.active] = spy
	m.Update(tea.MouseWheelMsg{X: 3, Y: 7, Button: tea.MouseWheelDown})
	if len(spy.got) != 1 || spy.got[0].Y != 5 {
		t.Fatalf("the view got %+v, want one wheel event at Y 5", spy.got)
	}
}
