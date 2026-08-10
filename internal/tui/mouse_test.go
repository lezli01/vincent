package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestHitTest is the T3.13 done-when: the pure half of click-to-focus,
// table-tested against a real layout.
func TestHitTest(t *testing.T) {
	boxes := layout(120, 32, panelTasks)
	if len(boxes) != 3 {
		t.Fatalf("layout returned %d boxes", len(boxes))
	}
	band := boxes[1].y
	cases := []struct {
		name string
		x, y int
		want panelID
		ok   bool
	}{
		{"top-left corner", 0, 0, panelTasks, true},
		{"inside the table", 40, 3, panelTasks, true},
		{"tasks bottom edge", 119, band - 1, panelTasks, true},
		{"timeline first cell", 0, band, panelTimeline, true},
		{"timeline body", 10, band + 1, panelTimeline, true},
		{"output first cell", boxes[2].x, band, panelOutput, true},
		{"output body", 119, band + 2, panelOutput, true},
		{"right of everything", 120, 3, 0, false},
		{"below everything", 4, 32, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := hitTest(tc.x, tc.y, boxes)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("hitTest(%d,%d) = %v,%v; want %v,%v", tc.x, tc.y, got, ok, tc.want, tc.ok)
			}
		})
	}
	if _, ok := hitTest(1, 1, nil); ok {
		t.Error("hitTest hit something in an empty layout")
	}
}

// TestShellClickFocusesPanels: a click lands focus where it points (§15).
func TestShellClickFocusesPanels(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning))
	boxes := s.lastBoxes
	if len(boxes) != 3 {
		t.Fatalf("fixture rendered %d boxes", len(boxes))
	}
	s.update(tea.MouseClickMsg{X: 2, Y: boxes[1].y + 1, Button: tea.MouseLeft})
	if s.focus != panelTimeline {
		t.Fatalf("focus = %v after clicking the timeline, want it focused", s.focus)
	}
	s.update(tea.MouseClickMsg{X: boxes[2].x + 2, Y: boxes[2].y + 1, Button: tea.MouseLeft})
	if s.focus != panelOutput {
		t.Fatalf("focus = %v after clicking the output pane", s.focus)
	}
	s.update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft})
	if s.focus != panelTasks {
		t.Fatalf("focus = %v after clicking the table", s.focus)
	}
}

// TestShellClickSelectsRow: clicking a row moves the cursor there, and the
// selection rides the settle window exactly like key movement — no
// subscription until the cursor rests.
func TestShellClickSelectsRow(t *testing.T) {
	tasks := make([]apiclient.Task, 0, 4)
	for id := int64(1); id <= 4; id++ {
		tasks = append(tasks, task(id, stateRunning))
	}
	s, subs := newShellFixture(t, tasks...)
	s.settle()
	if *subs != 1 {
		t.Fatalf("subscriptions after settle = %d, want 1", *subs)
	}
	rows := s.board.visible()

	// The third data row: box top border, the board's lines above the table,
	// then row 0, 1, 2…
	y := s.lastBoxes[0].y + 1 + s.board.firstRowLine() + 2
	s.update(tea.MouseClickMsg{X: 10, Y: y, Button: tea.MouseLeft})
	s.render(120, 37)

	if got, ok := s.board.selected(); !ok || got != rows[2].ID {
		t.Fatalf("selected = %d, want row 2's #%d", got, rows[2].ID)
	}
	if s.detail.taskID != 0 || s.detail.streamID != 0 {
		t.Fatal("a click opened or subscribed before the settle window closed")
	}
	if *subs != 1 {
		t.Fatalf("subscriptions right after the click = %d, want still 1", *subs)
	}
	s.settle()
	if s.detail.taskID != rows[2].ID || *subs != 2 {
		t.Fatalf("after settle: detail #%d subs %d, want #%d and 2", s.detail.taskID, *subs, rows[2].ID)
	}
}

// TestShellClickSelectsTheRowItPointsAt derives the target line from the
// *rendered frame* rather than from the same arithmetic updateClick uses, so
// the two cannot agree on a wrong answer. They did: the click offset borrowed
// the board's height budget and landed two rows high, which is what the M3
// gate hit on macOS ("I have to click a few lines below the task").
func TestShellClickSelectsTheRowItPointsAt(t *testing.T) {
	tasks := make([]apiclient.Task, 0, 5)
	for id := int64(1); id <= 5; id++ {
		tasks = append(tasks, task(id, stateRunning))
	}
	s, _ := newShellFixture(t, tasks...)
	s.settle()

	lines := strings.Split(s.render(120, 37), "\n")
	for _, want := range s.board.visible() {
		y := lineOfTaskRow(t, lines, want.ID)
		s.update(tea.MouseClickMsg{X: 10, Y: y, Button: tea.MouseLeft})
		s.render(120, 37)
		if got, ok := s.board.selected(); !ok || got != want.ID {
			t.Fatalf("clicking line %d (task #%d's row) selected #%d", y, want.ID, got)
		}
	}
}

// lineOfTaskRow finds the rendered line whose ID column holds id.
func lineOfTaskRow(t *testing.T, lines []string, id int64) int {
	t.Helper()
	want := strconv.FormatInt(id, 10)
	for i, line := range lines {
		fields := strings.Fields(strings.Trim(ansi.Strip(line), "│ "))
		if len(fields) > 0 && fields[0] == want {
			return i
		}
	}
	t.Fatalf("task #%d has no row in the frame", id)
	return 0
}

// TestShellClickBelowTheRowsIsIgnored is a T3.8 finding: the table is
// padded with blank lines when it has fewer rows than pane, and clicking
// one used to select the last task because the move clamps.
func TestShellClickBelowTheRowsIsIgnored(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning), task(2, stateRunning))
	s.settle()
	before, ok := s.board.selected()
	if !ok {
		t.Fatal("fixture selected nothing")
	}
	box := s.lastBoxes[0]
	firstRow := box.y + 1 + s.board.firstRowLine()

	// Well past two rows, still inside the panel.
	for _, offset := range []int{2, 3, 5} {
		s.update(tea.MouseClickMsg{X: 10, Y: firstRow + offset, Button: tea.MouseLeft})
		s.render(120, 37)
		if got, _ := s.board.selected(); got != before {
			t.Fatalf("a click %d lines below the last row moved the selection to #%d", offset, got)
		}
	}
	// The row above it still selects, so the bound is not simply "ignore".
	s.update(tea.MouseClickMsg{X: 10, Y: firstRow + 1, Button: tea.MouseLeft})
	s.render(120, 37)
	if got, _ := s.board.selected(); got == before {
		t.Fatal("clicking the second row selected nothing")
	}
}

// TestShellWheelScrollsFocusedPanel: §15 — the wheel scrolls the focused
// panel, not the hovered one.
func TestShellWheelScrollsFocusedPanel(t *testing.T) {
	tasks := make([]apiclient.Task, 0, 4)
	for id := int64(1); id <= 4; id++ {
		tasks = append(tasks, task(id, stateRunning))
	}
	s, _ := newShellFixture(t, tasks...)
	rows := s.board.visible()

	s.update(tea.MouseWheelMsg{X: 5, Y: 3, Button: tea.MouseWheelDown})
	s.render(120, 37)
	if got, _ := s.board.selected(); got != rows[1].ID {
		t.Fatalf("wheel on the focused table selected #%d, want #%d", got, rows[1].ID)
	}

	// Focus the output pane: the wheel now scrolls it, and the table cursor
	// stays put even though the pointer is over the table.
	s.focus = panelOutput
	before, _ := s.board.selected()
	s.update(tea.MouseWheelMsg{X: 5, Y: 3, Button: tea.MouseWheelDown})
	s.render(120, 37)
	if got, _ := s.board.selected(); got != before {
		t.Fatalf("wheel moved the unfocused table from #%d to #%d", before, got)
	}
}

// TestShellClickOutputTab: clicking the diff span in the output panel's
// title switches the tab; clicking output switches back (§15: click a tab).
func TestShellClickOutputTab(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning))
	s.settle()
	out := s.lastBoxes[2]

	// "┌─ output │ diff …": the diff span sits after "output" and " │ ".
	diffX := out.x + 3 + 6 + 3 + 1
	s.update(tea.MouseClickMsg{X: diffX, Y: out.y, Button: tea.MouseLeft})
	if s.detail.tab != tabDiff {
		t.Fatalf("tab = %v after clicking diff, want the diff tab", s.detail.tab)
	}
	if s.focus != panelOutput {
		t.Fatalf("focus = %v, want the clicked panel focused", s.focus)
	}
	// The glyph shifts the spans while the panel is focused.
	outputX := out.x + 3 + 2 + 1
	s.update(tea.MouseClickMsg{X: outputX, Y: out.y, Button: tea.MouseLeft})
	if s.detail.tab != tabOutput {
		t.Fatalf("tab = %v after clicking output, want the output tab", s.detail.tab)
	}
}

// TestShellPopupIgnoresClicks: popup interaction stays keyboard — a stray
// click must not answer a question or steal focus from it.
func TestShellPopupIgnoresClicks(t *testing.T) {
	s, _ := newShellFixture(t, task(3, stateAwaitingInput))
	s.settle()
	s.detail.form = newAnswerForm(apiclient.InputRequest{
		Kind:      apiclient.InputKindQuestion,
		Questions: []apiclient.InputQuestion{{Text: "Sure?", Options: []string{"y"}}},
	})
	s.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !s.popup {
		t.Fatal("fixture: popup did not open")
	}
	before := s.focus
	s.update(tea.MouseClickMsg{X: 2, Y: s.lastBoxes[1].y + 1, Button: tea.MouseLeft})
	if !s.popup || s.focus != before {
		t.Fatal("a click reached the panels under the popup")
	}
}

// TestRootFooterClickFiresHint: clicking a footer hint replays its key —
// the pinned `q quit` is the always-present span to prove it with.
func TestRootFooterClickFiresHint(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	content(m) // render once so the footer records its spans

	var quit *footerHit
	for i, h := range m.footerHits {
		if h.key == "q" {
			quit = &m.footerHits[i]
		}
	}
	if quit == nil {
		t.Fatalf("no q span in the footer hits: %+v", m.footerHits)
	}
	_, cmd := m.Update(tea.MouseClickMsg{X: quit.x0, Y: 39, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("clicking the quit hint produced no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("clicking the quit hint produced %T, want tea.Quit", cmd())
	}
}

// TestRootMouseToggle: on by default per §15, M turns it off and the view
// stops requesting mouse events — native text selection returns.
func TestRootMouseToggle(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.notice.active = false

	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode = %v, want cell motion on by default", v.MouseMode)
	}
	m.Update(key("M"))
	if v := m.View(); v.MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse mode = %v after M, want none", v.MouseMode)
	}
	m.Update(key("M"))
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode = %v after M M, want cell motion again", v.MouseMode)
	}
	// The toggle is discoverable: a registry row, so palette and ? carry it.
	if !strings.Contains(helpText(ctxTasks), "toggle the mouse") {
		t.Error("the mouse toggle is not in the help overlay")
	}
}
