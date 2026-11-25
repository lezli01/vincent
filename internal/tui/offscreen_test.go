package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// #1072: a column with more cards than fit says how many are above and
// below on its edge.
func TestOverflowingColumnSaysWhatIsHidden(t *testing.T) {
	b := taskBoardFixture()
	for i := range 12 {
		b.tasks = append(b.tasks, apiclient.Task{
			ID: int64(100 + i), ProjectID: 1, State: stateRunning, Title: fmt.Sprintf("soak %d", i),
			StepTotal: 1, UpdatedAt: taskBoardNow,
		})
	}
	got := ansi.Strip(strings.Join(b.cardColumns(200, 30, 0), "\n"))
	if !strings.Contains(got, "▼") {
		t.Errorf("an overflowing Running column does not count what is below:\n%s", got)
	}
}

// #1072: `↓ N new` names the key that follows again.
func TestNewLinesMarkNamesTheFollowKey(t *testing.T) {
	v := chatViewFixture()
	v.following, v.newLines = false, 3
	if got := ansi.Strip(v.edgeLabel()); !strings.Contains(got, "↓ 3 new · alt+shift+g follows") {
		t.Fatalf("edge = %q", got)
	}
}

// #1072: L is hinted only on a fan-out, and says why it did nothing on a
// task without lanes.
func TestLaneKeyOnlyWhereItApplies(t *testing.T) {
	b := taskBoardFixture()
	b.selectedID = 21 // no lanes
	rows := b.liveBindings(bindingsFor(ctxTasks))
	for _, r := range rows {
		if r.key == "L" {
			t.Fatal("L is hinted on a task with no lanes")
		}
	}
	b.cardHeader = nil
	b.toggleCardLanes()
	if !strings.Contains(b.actions.status, "is not a fan-out") {
		t.Fatalf("L on a task with no lanes said %q", b.actions.status)
	}
}

// #1151: the root asks the shell, not the board, for the home screen's live
// rows, so the #1072 filter has to hold through the root — the footer and
// the palette — and not only when the board method is called directly.
func TestLaneKeyOnlyWhereItAppliesThroughTheRoot(t *testing.T) {
	fanOut := task(2, stateAwaitingChildren)
	fanOut.Children = &apiclient.ChildrenRollup{Total: 2}
	m := attentionRoot(t, task(1, stateRunning), fanOut)
	b := m.views[viewHome].(*shell).board
	offered := func() bool {
		m.openPalette()
		defer func() { m.palette = nil }()
		for _, e := range m.palette.entries {
			if e.key == "L" {
				return true
			}
		}
		return false
	}
	b.selectedID = 1
	if offered() {
		t.Error("the palette offers L on a task with no lanes")
	}
	if strings.Contains(ansi.Strip(m.footerLine()), "L lanes") {
		t.Error("the footer hints L on a task with no lanes")
	}
	b.selectedID = 2
	if !offered() {
		t.Error("the palette does not offer L on a fan-out")
	}
}
