package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// laneBoardFixture is a card board with one running fan-out parent, #30,
// whose two lanes the daemon has answered with — `api` running, `web`
// blocked — and an ordinary running task beside it.
func laneBoardFixture(t *testing.T) *board {
	t.Helper()
	b := newBoard()
	b.cards = true
	b.now = func() time.Time { return taskBoardNow }
	b.project = projectSel{id: 1, name: "api"}
	parent := apiclient.Task{
		ID: 30, ProjectID: 1, State: stateAwaitingChildren, Title: "split the gateway",
		StepTotal: 3, CurrentStep: 1, StepName: "build", UpdatedAt: taskBoardNow,
		Children: &apiclient.ChildrenRollup{Total: 2},
	}
	b.tasks = []apiclient.Task{
		parent,
		{ID: 31, ProjectID: 1, State: stateRunning, Title: "soak the limiter", StepTotal: 2, UpdatedAt: taskBoardNow},
	}
	lane := func(id int64, name, state string) apiclient.Task {
		pid := int64(30)
		return apiclient.Task{
			ID: id, ProjectID: 1, State: state, Title: "split the gateway — " + name,
			ParentTaskID: &pid, LaneID: &name, StepTotal: 4, CurrentStep: 1, StepName: "test",
		}
	}
	b.lanes.apply(boardLanesMsg{parentID: 30, lanes: []apiclient.Task{
		lane(41, "api", stateRunning), lane(42, "web", stateBlocked),
	}})
	b.selectedID = 30
	return b
}

func boardText(b *board) string { return ansi.Strip(b.renderCards(160, 40)) }

// TestCardBoardParentWearsItsLaneCount: a fan-out parent says it has lanes
// before anyone unfolds it, and none of them is a card of its own.
func TestCardBoardParentWearsItsLaneCount(t *testing.T) {
	b := laneBoardFixture(t)
	out := boardText(b)
	if !strings.Contains(out, "#30 "+groupGlyphFolded+"2") {
		t.Fatalf("parent card lacks its folded lane count:\n%s", out)
	}
	if strings.Contains(out, "#41") {
		t.Fatalf("a folded parent's lane is drawn:\n%s", out)
	}
}

// TestCardBoardUnfoldsLanesUnderTheParent: L hangs the lanes under the parent
// card as boxes the cursor walks onto, and L on a lane folds them back with
// the cursor on the parent.
func TestCardBoardUnfoldsLanesUnderTheParent(t *testing.T) {
	b := laneBoardFixture(t)
	b.toggleCardLanes()
	out := boardText(b)
	parent, api, web := strings.Index(out, "#30"), strings.Index(out, "#41"), strings.Index(out, "#42")
	if parent < 0 || api < parent || web < api {
		t.Fatalf("lanes not drawn under their parent:\n%s", out)
	}
	if !strings.Contains(out, "step 2/4 test") {
		t.Fatalf("a running lane box does not say where it is:\n%s", out)
	}
	// Both lanes stay in the parent's column whatever their own state: the
	// blocked lane is not a Needs-you card.
	if got := countTotal(b, taskColumnNeedsYou); got != 0 {
		t.Fatalf("Needs you counts %d tasks, want 0 — a lane is not a column card", got)
	}

	b.moveCard(1)
	if id, _ := b.cardSelected(); id != 41 {
		t.Fatalf("↓ from the parent selected %d, want lane 41", id)
	}
	b.moveCard(1)
	if id, _ := b.cardSelected(); id != 42 {
		t.Fatalf("↓ selected %d, want lane 42", id)
	}

	b.toggleCardLanes()
	if id, _ := b.cardSelected(); id != 30 {
		t.Fatalf("L on a lane left the cursor on %d, want the parent 30", id)
	}
	if strings.Contains(boardText(b), "#41") {
		t.Fatal("L on a lane did not fold its parent's lanes")
	}
}

// TestCardBoardUnfoldsANestedFanOut: a lane that fanned out unfolds its own
// lanes one level deeper.
func TestCardBoardUnfoldsANestedFanOut(t *testing.T) {
	b := laneBoardFixture(t)
	b.lanes.rows[30][0].Children = &apiclient.ChildrenRollup{Total: 1}
	b.toggleCardLanes()
	b.selectedID = 41
	b.toggleCardLanes()
	pid, name := int64(41), "api-auth"
	b.lanes.apply(boardLanesMsg{parentID: 41, lanes: []apiclient.Task{
		{ID: 51, ProjectID: 1, State: stateRunning, ParentTaskID: &pid, LaneID: &name},
	}})
	lines := strings.Split(boardText(b), "\n")
	col := func(sub string) int {
		for _, l := range lines {
			if i := strings.Index(l, sub); i >= 0 {
				return ansi.StringWidth(l[:i])
			}
		}
		return -1
	}
	if col("api-auth") <= col("▾1 api") {
		t.Fatalf("the nested lane is not indented under its lane:\n%s", strings.Join(lines, "\n"))
	}
	b.moveCard(1)
	if id, _ := b.cardSelected(); id != 51 {
		t.Fatalf("↓ from the unfolded lane selected %d, want nested lane 51", id)
	}
}

// countTotal is how many tasks a column counts.
func countTotal(b *board, column int) int {
	n := 0
	for _, g := range b.cardGroups()[column] {
		n += len(g.tasks)
	}
	return n
}

// TestRowBoardLTogglesLanes: the narrow row board answers L the same way,
// splicing the lanes in as indented rows.
func TestRowBoardLTogglesLanes(t *testing.T) {
	b := testBoard()
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{task(30, stateAwaitingChildren, func(t *apiclient.Task) {
		t.Children = &apiclient.ChildrenRollup{Total: 1}
	})}})
	b.render(100, 20)
	b.toggleCardLanes()
	if !b.lanes.expanded.has(30) {
		t.Fatal("L on the row board did not unfold the parent")
	}
	pid, name := int64(30), "api"
	b.lanes.apply(boardLanesMsg{parentID: 30, lanes: []apiclient.Task{
		{ID: 41, ProjectID: testProjectID, State: stateRunning, ParentTaskID: &pid, LaneID: &name, Title: "lane api"},
	}})
	// rows() is one entry per drawn line, so a wrapped row appears twice.
	var order []int64
	for _, r := range b.rows() {
		if r.line == 0 {
			order = append(order, r.task.ID)
			if r.task.ID == 41 && r.lane != 1 {
				t.Fatalf("lane 41 sits at lane depth %d, want 1", r.lane)
			}
		}
	}
	if len(order) != 2 || order[0] != 30 || order[1] != 41 {
		t.Fatalf("rows after L = %v, want the parent 30 then lane 41", order)
	}
}
