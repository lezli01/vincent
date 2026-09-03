package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// paneBody is the shape a workflow's `prompt:` actually has: blank lines,
// leading indentation, a markdown list, a line with a trailing space, a tab,
// an embedded YAML snippet, and a final newline. It is built from parts rather
// than written as one raw literal so the invisible characters are visible to
// whoever reads the test, and so no formatter can quietly trim them.
var paneBody = strings.Join([]string{
	"Review the diff on this branch and report what changed.",
	"",
	"Rules:",
	"  - one line per finding",
	"  - severity first  ", // two trailing spaces: a markdown hard break
	"\t- tabs are indentation here, not four spaces",
	"",
	"Emit YAML shaped like this:",
	"    steps:",
	"      - type: agent",
	"        prompt: |",
	"          a nested block scalar",
	"",
}, "\n")

func TestTextPaneRoundTripsMultilineValue(t *testing.T) {
	p := newTextPane()
	p.SetSize(60, 8)
	p.SetValue(paneBody)

	if got := p.Value(); got != paneBody {
		t.Fatalf("value did not round-trip\n got %q\nwant %q", got, paneBody)
	}
	if p.Dirty() {
		t.Fatal("pane is dirty after SetValue")
	}
	// The pane being drawn, focused and resized is not the pane being edited.
	p.Focus()
	p.SetSize(30, 4)
	_ = p.View()
	if got := p.Value(); got != paneBody {
		t.Fatalf("value changed by focus/resize/draw\n got %q\nwant %q", got, paneBody)
	}
}

func TestTextPaneRoundTripsLongBody(t *testing.T) {
	body := paneLines(60)

	p := newTextPane()
	p.SetSize(40, 10)
	p.SetValue(body)

	if got := p.Value(); got != body {
		t.Fatalf("60-line body did not round-trip byte for byte")
	}
	if got, want := p.LineCount(), 60; got != want {
		t.Fatalf("LineCount() = %d, want %d", got, want)
	}
}

func TestTextPaneEnterInsertsNewline(t *testing.T) {
	p := newTextPane()
	p.SetSize(40, 6)
	p.SetValue("alpha")
	p.Focus()

	// SetValue leaves the cursor at the top of the body, so enter splits ahead
	// of it: the assertion pins the newline, not the cursor policy.
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if got, want := p.Value(), "\nalpha"; got != want {
		t.Fatalf("enter did not insert a newline: value = %q, want %q", got, want)
	}
	if got, want := p.LineCount(), 2; got != want {
		t.Fatalf("LineCount() = %d, want %d", got, want)
	}
	if !p.Dirty() {
		t.Fatal("pane is clean after enter changed the value")
	}
}

func TestTextPaneDirtyOnlyAfterAnEdit(t *testing.T) {
	p := newTextPane()
	p.SetSize(40, 6)
	p.SetValue(paneBody)
	p.Focus()

	if p.Dirty() {
		t.Fatal("dirty immediately after SetValue")
	}

	// Pure navigation. A host writes only a dirty pane, so a key that moves
	// the cursor must not cost the file its block scalar.
	for _, c := range []rune{tea.KeyDown, tea.KeyRight, tea.KeyUp, tea.KeyLeft, tea.KeyDown} {
		p, _ = p.Update(tea.KeyPressMsg{Code: c})
	}
	if p.Dirty() {
		t.Fatalf("dirty after a cursor move alone")
	}
	if got := p.Value(); got != paneBody {
		t.Fatalf("value changed by a cursor move\n got %q\nwant %q", got, paneBody)
	}

	p, _ = p.Update(key("Z"))
	if !p.Dirty() {
		t.Fatal("clean after a typed rune")
	}
	if got := p.Value(); !strings.Contains(got, "Z") {
		t.Fatalf("typed rune did not reach the value: %q", got)
	}
}

func TestTextPaneLeavesEscapeToItsHost(t *testing.T) {
	p := newTextPane()
	p.SetSize(40, 6)
	p.SetValue(paneBody)
	p.Focus()

	// The host intercepts esc and whatever it means by save before delegating;
	// were the pane to blur or clear on esc it would fight that decision.
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if !p.Focused() {
		t.Fatal("esc took the keyboard away from the pane")
	}
	if p.Dirty() {
		t.Fatal("esc marked the pane dirty")
	}
	if got := p.Value(); got != paneBody {
		t.Fatalf("esc changed the value\n got %q\nwant %q", got, paneBody)
	}
}

func TestTextPaneLineCount(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{
		{"", 1},
		{"one line", 1},
		{"two\nlines", 2},
		{"trailing newline counts its empty line\n", 2},
		{"\n\n", 3},
		{paneBody, strings.Count(paneBody, "\n") + 1},
	} {
		p := newTextPane()
		p.SetValue(tc.value)
		if got := p.LineCount(); got != tc.want {
			t.Errorf("LineCount(%q) = %d, want %d", tc.value, got, tc.want)
		}
	}
}

func TestTextPaneScrollsInsteadOfOverflowing(t *testing.T) {
	const height = 8

	p := newTextPane()
	p.SetSize(40, height)
	p.SetValue(paneLines(60))
	p.Focus()

	if got := paneViewHeight(p); got != height {
		t.Fatalf("a 60-line body drew %d rows, want %d", got, height)
	}
	view := paneViewText(p)
	if !strings.Contains(view, "line 01") {
		t.Fatalf("pane does not start at the top of the body:\n%s", view)
	}

	// Walk the cursor to the last line: the pane must follow it by scrolling,
	// not by growing.
	for range 59 {
		p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if got := paneViewHeight(p); got != height {
		t.Fatalf("after scrolling the pane drew %d rows, want %d", got, height)
	}
	view = paneViewText(p)
	if !strings.Contains(view, "line 60") {
		t.Fatalf("pane did not scroll to the cursor:\n%s", view)
	}
	if strings.Contains(view, "line 01") {
		t.Fatalf("pane still shows its first line after scrolling to the last:\n%s", view)
	}
}

func TestTextPaneFocused(t *testing.T) {
	p := newTextPane()
	if p.Focused() {
		t.Fatal("a fresh pane holds the keyboard")
	}
	p.Focus()
	if !p.Focused() {
		t.Fatal("Focus() did not take the keyboard")
	}
	p.Blur()
	if p.Focused() {
		t.Fatal("Blur() did not give the keyboard back")
	}
}

// paneLines is an n-line body whose lines are individually identifiable.
func paneLines(n int) string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("line %02d of the prompt", i+1))
	}
	return strings.Join(out, "\n")
}

// paneViewHeight is the number of rows the pane actually drew.
func paneViewHeight(p textPane) int {
	return len(strings.Split(strings.TrimSuffix(p.View(), "\n"), "\n"))
}

// paneViewText is the drawn pane without its styling, which is what a test
// asking *which* lines are on screen wants: the cursor cell carries its own
// escape sequences, so the raw view splits the word it sits on.
func paneViewText(p textPane) string { return ansi.Strip(p.View()) }
