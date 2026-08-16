package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 011: the bulk selection on the task table.

// markedBoard is a flat, loaded board with the given tasks — the same fixture
// the sorting and column tests use, since a selection is a question about
// tasks and grouping only changes which line they land on.
func markedBoard(tasks ...apiclient.Task) *board {
	b := testBoard()
	b.tasks = tasks
	return b
}

func withActions(actions ...string) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.AvailableActions = actions }
}

// TestMarksSurviveARefreshThatReorders is the reason the selection holds ids
// rather than row indices: a board reorders under a running task all the time,
// and a selection that followed the rows would silently come to mean other
// tasks.
func TestMarksSurviveARefreshThatReorders(t *testing.T) {
	b := markedBoard(task(1, stateDone), task(2, stateDone), task(3, stateDone))
	b.marks = markSet{1, 3}

	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{
		task(3, stateDone), task(2, stateRunning), task(1, stateDone),
	}})

	if !b.marks.has(1) || !b.marks.has(3) || b.marks.has(2) {
		t.Fatalf("marks after a reordering refresh = %v, want exactly [1 3]", b.marks)
	}
}

// TestMarksArePrunedForTasksTheDaemonDropped: a mark for a task that is gone
// would be counted in the panel title and dispatched to a 404.
func TestMarksArePrunedForTasksTheDaemonDropped(t *testing.T) {
	b := markedBoard(task(1, stateDone), task(2, stateDone))
	b.marks = markSet{1, 2}

	// #2 archived away: the default list stops returning it.
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{task(1, stateDone)}})
	if len(b.marks) != 1 || !b.marks.has(1) {
		t.Fatalf("marks = %v, want just [1]", b.marks)
	}

	// A *failed* refresh says nothing about which tasks exist, so it must not
	// prune: the rows on screen are still the last truth there was.
	b.updateLoaded(boardLoadedMsg{err: errors.New("http 500")})
	if !b.marks.has(1) {
		t.Fatalf("a failed refresh dropped the selection (marks %v)", b.marks)
	}
}

// TestMarkVisibleTakesTheFilterButTheSelectionKeepsWhatItHad: `V` acts on the
// rows on screen, and a selection built before the filter was typed is not
// thrown away by a key aimed at them (task 011 decision).
func TestMarkVisibleTakesTheFilterButTheSelectionKeepsWhatItHad(t *testing.T) {
	b := markedBoard(
		task(1, stateDone, inProject("api")),
		task(2, stateDone, inProject("web")),
		task(3, stateDone, inProject("web")),
	)
	b.marks = markSet{1}
	b.filter.SetValue("web")

	b.markVisible()
	for _, id := range []int64{1, 2, 3} {
		if !b.marks.has(id) {
			t.Fatalf("after V the selection is %v, want #%d in it", b.marks, id)
		}
	}

	// Everything visible is marked, so the same key unmarks it — and only it.
	b.markVisible()
	if b.marks.has(2) || b.marks.has(3) {
		t.Errorf("V left the filtered rows marked: %v", b.marks)
	}
	if !b.marks.has(1) {
		t.Errorf("V cleared a mark the filter was hiding: %v", b.marks)
	}
}

// TestMarkedTargetsCarryEachTaskActions: the bar decides nothing about
// validity, so the selection has to hand it available_actions per task (§6).
func TestMarkedTargetsCarryEachTaskActions(t *testing.T) {
	b := markedBoard(
		task(1, stateDone, withActions(apiclient.ActionArchive)),
		task(2, stateRunning, withActions(apiclient.ActionCancel, apiclient.ActionPause)),
	)
	b.marks = markSet{2, 1}

	got := b.markedTargets()
	// Board order, not the order the marks were made: a bulk action runs the
	// way the rows were read.
	if len(got) != 2 || got[0].id != 2 || got[1].id != 1 {
		t.Fatalf("targets = %+v, want #2 (running) before #1 (done)", got)
	}
	target := b.target()
	if !target.has(apiclient.ActionArchive) || !target.has(apiclient.ActionCancel) {
		t.Fatalf("a mixed selection offers %v, want both archive and cancel", target.marked)
	}
	if ids := target.targets(apiclient.ActionArchive); len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("archive targets = %v, want just the done task", ids)
	}
}

// TestMarkerColumnExistsOnlyWhileSomethingIsMarked: an unmarked board is the
// board every earlier version rendered, to the column.
func TestMarkerColumnExistsOnlyWhileSomethingIsMarked(t *testing.T) {
	b := markedBoard(task(1, stateDone), task(2, stateDone))

	plain := b.render(160, 20)
	if strings.Contains(ansi.Strip(plain), markGlyph) {
		t.Fatalf("an unmarked board rendered the selection glyph:\n%s", plain)
	}
	// At 160 there is slack for the marker, so it is added rather than paid
	// for by shedding — which is what makes this a count of one column.
	cols, _ := boardColumns(160, nil, false)
	marked, _ := boardColumns(160, nil, true)
	if len(marked) != len(cols)+1 {
		t.Fatalf("marking added %d columns, want exactly one", len(marked)-len(cols))
	}

	b.marks = markSet{2}
	out := ansi.Strip(b.render(160, 20))
	if !strings.Contains(out, markGlyph) {
		t.Fatalf("a marked board does not show the selection:\n%s", out)
	}
	// One row is marked, so the glyph appears once — the marker is per row,
	// not a mode indicator painted down the column.
	if n := strings.Count(out, markGlyph); n != 1 {
		t.Fatalf("the glyph renders %d times, want once (one marked row):\n%s", n, out)
	}
}

// TestBoardTitleCountsTheSelection: the selection survives a filter, so the
// count is the only thing that can say a hidden task is still in it.
func TestBoardTitleCountsTheSelection(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateDone, inProject("api")), task(2, stateDone, inProject("web")))
	s.board.marks = markSet{1, 2}
	s.board.filter.SetValue("web")

	if title := s.panelTitle(panelTasks); !strings.Contains(title, "2 selected") {
		t.Fatalf("panel title = %q, want the selection count in it", title)
	}
}

// TestEscClearsTheSelectionBeforeTheFilter is the §15 esc stack with the
// selection spliced in: one layer per press, innermost first.
func TestEscClearsTheSelectionBeforeTheFilter(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateDone))
	s.focus = panelTasks
	s.board.filter.SetValue("task")
	s.board.marks = markSet{1}

	s.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.board.hasMarks() {
		t.Fatal("esc did not clear the selection first")
	}
	if !s.board.filterActive() {
		t.Fatal("esc took the filter with it — that is two layers for one press")
	}
	s.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.board.filterActive() {
		t.Fatal("the second esc did not clear the filter")
	}
}
