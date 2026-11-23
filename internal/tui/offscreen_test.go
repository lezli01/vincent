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
