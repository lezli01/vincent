package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// runningTasks is n running tasks with unique titles, task 1 the longest
// running and so the top row of the flat board.
func runningTasks(n int) []apiclient.Task {
	tasks := make([]apiclient.Task, 0, n)
	for i := 1; i <= n; i++ {
		tasks = append(tasks, task(int64(i), stateRunning,
			withTitle(fmt.Sprintf("unique-%02d", i)),
			withStarted(testNow.Add(-time.Duration(n-i+1)*time.Minute))))
	}
	return tasks
}

// selectedRowVisible reports whether the selected task's title is on screen.
func selectedRowVisible(t *testing.T, b *board, rendered string) bool {
	t.Helper()
	id, ok := b.selected()
	if !ok {
		t.Fatal("no task selected")
	}
	return strings.Contains(ansi.Strip(rendered), fmt.Sprintf("unique-%02d", id))
}

// TestCursorStaysVisibleWhenTheSelectedTaskSortsToTheBottom is #798 B1:
// restoreSelection calls bubbles' SetCursor, which re-renders the row range
// but never moves the viewport's YOffset, so the selected row lands off
// screen and action keys act on a task the user cannot see.
func TestCursorStaysVisibleWhenTheSelectedTaskSortsToTheBottom(t *testing.T) {
	b := testBoard()
	tasks := runningTasks(40)
	b.updateLoaded(boardLoadedMsg{tasks: tasks})
	b.render(80, 24)
	b.rememberSelection()
	if id, _ := b.selected(); id != 1 {
		t.Fatalf("selected = %d, want task 1 on top", id)
	}

	tasks[0].State = stateDone
	b.updateLoaded(boardLoadedMsg{tasks: tasks})
	rendered := b.render(80, 24)
	if id, _ := b.selected(); id != 1 {
		t.Fatalf("selected = %d after the re-sort, want task 1", id)
	}
	if !selectedRowVisible(t, b, rendered) {
		t.Errorf("cursor %d is on task 1, which is not on screen:\n%s", b.tbl.Cursor(), ansi.Strip(rendered))
	}
}

// TestLiveResortBeforeAnyKeyKeepsTheSelection is #798 B15: selectedID is 0
// until a key is pressed, so a re-sort moves the cursor to another task.
func TestLiveResortBeforeAnyKeyKeepsTheSelection(t *testing.T) {
	b := testBoard()
	tasks := runningTasks(5)
	b.updateLoaded(boardLoadedMsg{tasks: tasks})
	b.render(120, 20)
	if id, _ := b.selected(); id != 1 {
		t.Fatalf("selected = %d on open, want task 1", id)
	}
	tasks[0].State = stateDone // sorts below the running band
	b.updateLoaded(boardLoadedMsg{tasks: tasks})
	b.render(120, 20)
	if id, _ := b.selected(); id != 1 {
		t.Errorf("selected = %d after a live re-sort with no key pressed, want task 1", id)
	}
}

// TestArchivingTheSelectedTaskLandsOnANeighbour is #798 B7: the fallback is
// firstTaskRow, so a cursor deep in the list jumps to the top.
func TestArchivingTheSelectedTaskLandsOnANeighbour(t *testing.T) {
	b := testBoard()
	tasks := runningTasks(40)
	b.updateLoaded(boardLoadedMsg{tasks: tasks})
	b.render(120, 60)
	for range 30 {
		b.updateKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	}
	b.render(120, 60)
	if b.tbl.Cursor() != 30 {
		t.Fatalf("cursor = %d, want 30", b.tbl.Cursor())
	}
	gone, _ := b.selected()
	var left []apiclient.Task
	for _, tk := range tasks {
		if tk.ID != gone {
			left = append(left, tk)
		}
	}
	b.updateLoaded(boardLoadedMsg{tasks: left})
	b.render(120, 60)
	if c := b.tbl.Cursor(); c < 29 || c > 30 {
		t.Errorf("cursor = %d after the selected task left, want a neighbour of row 30", c)
	}
}

// TestTheNeighbourBecomesTheSelection: once the selected task has left the
// board, its neighbour is the selection, so a later re-sort above it keeps
// the cursor on that neighbour rather than on whatever lands at its index
// (review F16).
func TestTheNeighbourBecomesTheSelection(t *testing.T) {
	b := testBoard()
	tasks := runningTasks(40)
	b.updateLoaded(boardLoadedMsg{tasks: tasks})
	b.render(120, 60)
	for range 30 {
		b.updateKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	}
	b.render(120, 60)
	gone, _ := b.selected()
	var left []apiclient.Task
	for _, tk := range tasks {
		if tk.ID != gone {
			left = append(left, tk)
		}
	}
	b.updateLoaded(boardLoadedMsg{tasks: left})
	b.render(120, 60)
	neighbour, ok := b.selected()
	if !ok || neighbour == gone {
		t.Fatalf("selected = %d (%v) after task %d left", neighbour, ok, gone)
	}
	left[2].State = stateDone // sorts below the running band, above nothing
	b.updateLoaded(boardLoadedMsg{tasks: left})
	b.render(120, 60)
	if id, _ := b.selected(); id != neighbour {
		t.Errorf("selected = %d after a re-sort above it, want the neighbour %d", id, neighbour)
	}
}

// TestShrinkingTheBoardKeepsTheCursorOnScreen: bubbles' SetHeight does not
// move the table's scroll, so a pane that shrank by a line (a resize, a
// status line appearing) left the cursor's row below the window (review
// F21).
func TestShrinkingTheBoardKeepsTheCursorOnScreen(t *testing.T) {
	for from := 18; from <= 22; from++ {
		for _, to := range []int{from - 1, from - 3, 12} {
			b := testBoard()
			b.updateLoaded(boardLoadedMsg{tasks: runningTasks(40)})
			b.render(80, 24)
			for b.tbl.Cursor() < from {
				b.updateKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
				b.render(80, 24)
			}
			if !selectedRowVisible(t, b, b.render(80, 24)) {
				t.Fatalf("row %d is off screen before the shrink", from)
			}
			if rendered := b.render(80, to); !selectedRowVisible(t, b, rendered) {
				id, _ := b.selected()
				t.Errorf("cursor on row %d: shrinking 80×24 to 80×%d hid task %d:\n%s", from, to, id, ansi.Strip(rendered))
			}
		}
	}
}

// TestFailedFirstLoadDoesNotSayNoTasksYet is #798 B8: emptyBody handles
// only !loaded && loadErr == nil, so a first load that failed falls through
// to the first-run "no tasks yet".
func TestFailedFirstLoadDoesNotSayNoTasksYet(t *testing.T) {
	b := testBoard()
	b.loaded = false
	b.updateLoaded(boardLoadedMsg{err: errors.New("connection refused")})
	if got := ansi.Strip(b.render(120, 20)); strings.Contains(got, "no tasks yet") {
		t.Errorf("a failed first load says the board is empty:\n%s", got)
	}
}

// TestEmptyArchivedBoardDoesNotOfferNew is #798 B9: the archived board
// falls through to the live board's "press n to create one".
func TestEmptyArchivedBoardDoesNotOfferNew(t *testing.T) {
	b := selectTestProject(newArchivedBoard())
	b.now = func() time.Time { return testNow }
	b.bell = func() {}
	b.updateLoaded(boardLoadedMsg{tasks: nil})
	if got := ansi.Strip(b.render(120, 20)); strings.Contains(got, "to create one") {
		t.Errorf("the empty archived board offers to create a task:\n%s", got)
	}
}

// TestWrapCellLinesTruncatesTheOriginalText is #798 B10: the cut last line
// was the wrapped pieces rejoined with a space, so a title the wrap had broken
// without one — after a hyphen — showed a space the title never had.
func TestWrapCellLinesTruncatesTheOriginalText(t *testing.T) {
	got := wrapCellLines("fix-the-board-cursor-scrolling-off-screen-now", 10, 2)
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(got), got)
	}
	if want := "board-cur…"; got[1] != want {
		t.Errorf("last line = %q, want %q", got[1], want)
	}
}
