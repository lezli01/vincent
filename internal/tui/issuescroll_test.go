package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// Issue #792: the issue detail could not be scrolled by the keys a reader
// reaches for, nor by the wheel, and neither issue screen took the mouse
// (spec §15 Mouse: "click a row to select it, wheel to scroll the focused
// panel").

// longLocalIssue is a local issue with no linked tasks and a body of forty
// numbered paragraphs, far taller than an 80×24 pane.
func longLocalIssue() *issueView {
	v := newIssueView(nil, nil)
	v.client = offlineClient()
	v.id = 9
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "para%02d of the description.\n\n", i)
	}
	v.issue = apiclient.Issue{ID: 9, ProjectID: 1, Title: "Long one", State: "open", Body: b.String(), Commentable: true}
	v.loaded = true
	return v
}

func renderedLines(v panel, w, h int) []string {
	return strings.Split(ansi.Strip(v.render(w, h)), "\n")
}

func shows(lines []string, s string) bool {
	for _, l := range lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func lineOf(lines []string, s string) int {
	for i, l := range lines {
		if strings.Contains(l, s) {
			return i
		}
	}
	return -1
}

// On a local issue with no tasks and a long body at 80×24, ↓/j scroll by one
// line, pgdown/space by a page, end shows the last line and home the first.
func TestIssueDetailKeysScrollTheLongBody(t *testing.T) {
	v := longLocalIssue()
	const w, h = 80, 24
	renderedLines(v, w, h)
	for _, k := range []string{"down", "j"} {
		before := v.scroll
		v.update(keyPress(k))
		renderedLines(v, w, h)
		if v.scroll != before+1 {
			t.Errorf("%q: scroll %d -> %d, want one line down", k, before, v.scroll)
		}
	}
	for _, k := range []string{"pgdown", "space"} {
		before := v.scroll
		v.update(keyPress(k))
		renderedLines(v, w, h)
		if v.scroll-before < h/2 {
			t.Errorf("%q: scroll %d -> %d, want about a page (%d rows)", k, before, v.scroll, h)
		}
	}
	v.update(keyPress("end"))
	if !shows(renderedLines(v, w, h), "para40") {
		t.Error("end does not show the last paragraph")
	}
	v.update(keyPress("home"))
	if !shows(renderedLines(v, w, h), "para01") {
		t.Error("home does not show the first paragraph")
	}
}

// With linked tasks, the first ↓ from the top keeps the description on
// screen: the selection moves only among visible rows, and none is visible.
func TestIssueDetailFirstDownKeepsTheDescription(t *testing.T) {
	v := issueFixture()
	const w, h = 80, 24
	if !shows(renderedLines(v, w, h), "Why") {
		t.Fatal("fixture does not open on the description")
	}
	v.update(keyPress("down"))
	if lines := renderedLines(v, w, h); !shows(lines, "more") || v.scroll > 1 {
		t.Fatalf("first ↓ jumped to scroll=%d, past the description:\n%s", v.scroll, strings.Join(lines, "\n"))
	}
}

// A cue marks content continuing below the viewport, and above it once
// scrolled.
func TestIssueDetailShowsAContinuationCue(t *testing.T) {
	v := longLocalIssue()
	if !shows(renderedLines(v, 80, 24), "▼") {
		t.Error("no ▼ cue while the body continues below")
	}
	v.scroll = 10
	if !shows(renderedLines(v, 80, 24), "▲") {
		t.Error("no ▲ cue while the body continues above")
	}
}

// The footer shows a scroll hint, and never `enter task` with no row to
// open or `o browser` on a local issue.
func TestIssueDetailFooterBindings(t *testing.T) {
	v := longLocalIssue()
	hints := map[string]bool{}
	for _, b := range v.liveBindings(bindingsFor(ctxIssue)) {
		if b.hint != "" {
			hints[b.hint] = true
		}
	}
	scroll := false
	for h := range hints {
		if strings.Contains(h, "scroll") {
			scroll = true
		}
		if strings.HasPrefix(h, "enter ") {
			t.Errorf("footer offers %q on an issue with no row to open", h)
		}
		if strings.HasPrefix(h, "o ") {
			t.Errorf("footer offers %q on a local issue", h)
		}
	}
	if !scroll {
		t.Errorf("no scroll hint among %v", hints)
	}
}

// The wheel scrolls the detail one line per tick (#300's convention), and
// does nothing while a prompt is open.
func TestIssueDetailWheelScrolls(t *testing.T) {
	v := longLocalIssue()
	renderedLines(v, 80, 24)
	v.update(tea.MouseWheelMsg{X: 10, Y: 5, Button: tea.MouseWheelDown})
	renderedLines(v, 80, 24)
	if v.scroll != 1 {
		t.Fatalf("wheel down: scroll = %d, want 1", v.scroll)
	}
	v.w.act = newIssueDelete(v.client, v.issue)
	v.update(tea.MouseWheelMsg{X: 10, Y: 5, Button: tea.MouseWheelDown})
	if v.scroll != 1 {
		t.Fatalf("wheel under the delete prompt moved the page to %d", v.scroll)
	}
}

// A click on a linked task's row selects it.
func TestIssueDetailClickSelectsATask(t *testing.T) {
	v := issueFixture()
	const w, h = 100, 120
	lines := renderedLines(v, w, h)
	y := lineOf(lines, "dark mode, review")
	if y < 0 {
		t.Fatalf("second task not on screen:\n%s", strings.Join(lines, "\n"))
	}
	v.update(tea.MouseClickMsg{X: 10, Y: y, Button: tea.MouseLeft})
	rows := v.selectRows()
	if v.cursor >= len(rows) || rows[v.cursor].task.ID != 22 {
		t.Fatalf("click on task #22 left cursor %d", v.cursor)
	}
}

// The wheel moves the list's selection, and a click selects the row under
// it; neither acts while a prompt is open.
func TestIssuesListWheelAndClick(t *testing.T) {
	v := boardFixture()
	const w, h = 100, 40
	renderedLines(v, w, h)
	start := v.cursor
	v.update(tea.MouseWheelMsg{X: 10, Y: 5, Button: tea.MouseWheelDown})
	if v.cursor == start {
		t.Errorf("wheel down left the list cursor at %d", start)
	}

	lines := renderedLines(v, w, h)
	y := lineOf(lines, "older")
	if y < 0 {
		t.Fatalf("issue #14 not on screen:\n%s", strings.Join(lines, "\n"))
	}
	v.update(tea.MouseClickMsg{X: 10, Y: y, Button: tea.MouseLeft})
	if v.selected != 14 {
		t.Errorf("click on #14 selected #%d", v.selected)
	}

	v.w.act = newIssueDelete(v.client, apiclient.Issue{ID: 14})
	at := v.cursor
	v.update(tea.MouseWheelMsg{X: 10, Y: 5, Button: tea.MouseWheelDown})
	if v.cursor != at {
		t.Error("wheel under the delete prompt moved the list cursor")
	}
}

// An operation a lenient load rebound to one of the detail's fixed scroll
// keys fires on it, as the load's warning says it now does, rather than the
// key still scrolling (task 128 decision 3, review F5).
func TestIssueDetailRebindingAScrollKeyWins(t *testing.T) {
	withLenientKeymap(t, map[string]string{"delete": "g", "comment": "G"})
	v := issueFixture()
	v.render(80, 24)
	v.scroll = 3
	v.updateKey(key("g"))
	if v.w.act == nil {
		t.Error("g bound to delete did not raise the delete question")
	}
	if v.scroll != 3 {
		t.Errorf("g bound to delete still scrolled, to %d", v.scroll)
	}
	v.w.act = nil
	v.updateKey(key("G"))
	if !strings.Contains(v.note, "comment there") {
		t.Errorf("G bound to comment did not reach the comment action; note %q", v.note)
	}
	if v.scroll != 3 {
		t.Errorf("G bound to comment still scrolled, to %d", v.scroll)
	}
}

// The selection acts only while it is on screen (#792): on the first page
// of a long issue the linked-task rows are below it, so enter opens nothing
// and the footer does not offer it until a row has scrolled in (review F6).
func TestIssueDetailEnterNeedsTheSelectionOnScreen(t *testing.T) {
	v := issueFixture()
	v.render(80, 24)
	if v.visible(v.cursor) {
		t.Fatal("fixture: the selected row is on the first page of an 80×24 pane")
	}
	if _, cmd := v.updateKey(registryKey(t, "enter")); cmd != nil {
		t.Fatalf("enter opened %#v with the selected row off screen", cmd())
	}
	for _, b := range v.liveBindings(bindings) {
		if b.context == ctxIssue && b.op == keymap.OpenRow {
			t.Fatal("the footer offers enter with the selected row off screen")
		}
	}
	v.updateKey(key("G"))
	v.render(80, 24)
	if !v.visible(v.cursor) {
		t.Fatal("the selected row is still off screen at the end of the page")
	}
	_, cmd := v.updateKey(registryKey(t, "enter"))
	if cmd == nil {
		t.Fatal("enter on an on-screen row opened nothing")
	}
	if msg, ok := cmd().(selectTaskMsg); !ok || msg.id != 21 {
		t.Fatalf("enter produced %#v, want task #21", cmd())
	}
}
