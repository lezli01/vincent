package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
)

// The board's fan-out tree (boardlanes.go, issue #316). What these tests are
// really guarding is task 014 decision 13: lanes are visible from the board
// now, and they are still not in the list — not by a filter somewhere, but
// because they never enter b.tasks at all.

// laneTask is one lane of parent 7, as GET /v1/tasks?parent_id=7 serves it.
func laneTask(id int64, laneID string, order int, state string, opts ...func(*apiclient.Task)) apiclient.Task {
	t := task(id, state)
	parent := int64(7)
	t.Title = laneID + " lane"
	t.ParentTaskID, t.LaneID, t.LaneOrder = &parent, &laneID, &order
	for _, o := range opts {
		o(&t)
	}
	return t
}

// laneChildOf re-parents a lane onto another lane, for the nesting tests.
func laneChildOf(parent int64) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.ParentTaskID = &parent }
}

// boardWithCursorOnParent is a flat board holding tasks, with the cursor
// parked on the fan-out parent every fixture in this file numbers 7 — which is
// the state `L` acts in.
func boardWithCursorOnParent(tasks ...apiclient.Task) *board {
	b := testBoard()
	b.updateLoaded(boardLoadedMsg{tasks: tasks})
	b.selectedID = 7
	b.render(120, 20)
	return b
}

// expand opens one parent and hands the board the lanes the daemon would have
// served, without a round trip.
func expand(b *board, parentID int64, lanes ...apiclient.Task) {
	b.lanes.expanded = b.lanes.expanded.toggle(parentID)
	b.lanes.apply(boardLanesMsg{parentID: parentID, lanes: lanes})
}

// laneIDs is the task ids the board would render, headers dropped.
func laneIDs(b *board) []int64 {
	out := make([]int64, 0, len(b.tasks))
	for _, r := range b.allRows() {
		if !r.header {
			out = append(out, r.task.ID)
		}
	}
	return out
}

// TestFanOutParentExpandsInEveryStateItPassesThrough is the whole point of
// #316. `L` used to be accepted only on awaiting_children, so a parent's lanes
// became unreachable from the board at the exact moment they matter most —
// blocked on lane_failed or on a merge conflict, where what a human needs to
// read is a lane's transcript.
func TestFanOutParentExpandsInEveryStateItPassesThrough(t *testing.T) {
	reason := "lane_failed"
	for _, tc := range []struct {
		name   string
		parent apiclient.Task
		// want is the whole board, in band-sort order: a done parent sits
		// below the running task, and its lanes travel with it.
		want      []int64
		collapsed []int64
	}{
		{"awaiting_children", task(7, stateAwaitingChildren), []int64{7, 42, 43, 8}, []int64{7, 8}},
		{"blocked", func() apiclient.Task {
			p := task(7, stateBlocked)
			p.BlockReason = &reason
			return p
		}(), []int64{7, 42, 43, 8}, []int64{7, 8}},
		{"done", task(7, stateDone), []int64{8, 7, 42, 43}, []int64{8, 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := boardWithCursorOnParent(tc.parent, task(8, stateRunning))
			b.toggleLanes()
			if !b.lanes.expanded.has(7) {
				t.Fatalf("L did not expand a %s parent", tc.name)
			}
			b.lanes.apply(boardLanesMsg{parentID: 7, lanes: []apiclient.Task{
				laneTask(42, "api", 0, stateBlocked),
				laneTask(43, "web", 1, stateDone),
			}})
			if got := laneIDs(b); !equalIDs(got, tc.want...) {
				t.Fatalf("rows = %v, want the lanes under the parent (%v)", got, tc.want)
			}
			// And a second press takes them away again.
			b.toggleLanes()
			if got := laneIDs(b); !equalIDs(got, tc.collapsed...) {
				t.Fatalf("a second L left %v on the board, want %v", got, tc.collapsed)
			}
		})
	}
}

// TestExpandedLanesRenderInMergeOrderAndIndented pins both halves of what an
// expanded row looks like: §13.2 already serves the lanes in the order the
// join will merge them, so the board sorts nothing, and each one is inset a
// level under its parent.
func TestExpandedLanesRenderInMergeOrder(t *testing.T) {
	parent := task(7, stateAwaitingChildren)
	parent.Title = "fan out" // short enough that the title cell does not wrap
	b := boardWithCursorOnParent(parent)
	expand(b, 7,
		laneTask(43, "web", 0, stateRunning),
		laneTask(42, "api", 1, stateRunning),
		laneTask(44, "cli", 2, stateQueued),
	)
	// Merge order, not the band sort: 44 is queued and would sort below the
	// two running lanes if the board had re-ordered them.
	if got := laneIDs(b); !equalIDs(got, 7, 43, 42, 44) {
		t.Fatalf("lanes rendered %v, want the daemon's merge order (7 43 42 44)", got)
	}
	for _, r := range b.allRows() {
		if r.task.ID == 7 && r.lane != 0 {
			t.Errorf("the parent row is at lane depth %d, want 0", r.lane)
		}
		if r.task.ID > 7 && r.lane != 1 {
			t.Errorf("lane %d is at lane depth %d, want 1", r.task.ID, r.lane)
		}
	}
	// The indent is visible in the rendered title, and the parent wears the
	// open marker.
	out := ansi.Strip(b.render(120, 20))
	if !strings.Contains(out, groupGlyphOpen+" fan out") {
		t.Errorf("the expanded parent has no open marker:\n%s", out)
	}
	if !strings.Contains(out, groupIndent+"web lane") {
		t.Errorf("the lane title is not indented under its parent:\n%s", out)
	}
}

// TestCollapsedFanOutParentWearsTheClosedMarker: the affordance has to be
// there before anybody presses anything, and it must not appear on a row with
// nothing under it — a `▸` the board cannot deliver on is worse than none.
func TestCollapsedFanOutParentWearsTheClosedMarker(t *testing.T) {
	reason := "merge_conflict"
	blocked := task(8, stateBlocked)
	blocked.BlockReason = &reason
	plain := task(9, stateBlocked)
	other := "check_failed"
	plain.BlockReason = &other

	b := boardWithCursorOnParent(task(7, stateAwaitingChildren), blocked, plain, task(10, stateDone))
	for _, tc := range []struct {
		id   int64
		want string
	}{
		{7, groupGlyphFolded}, // parked on its join: it has lanes by definition
		{8, groupGlyphFolded}, // a fan-out block reason says so too
		{9, ""},               // blocked on something else entirely
		{10, ""},              // done, and nothing on a list row says it fanned out
	} {
		got := b.laneGlyph(b.tasks[indexOfTask(t, b.tasks, tc.id)])
		if got != tc.want {
			t.Errorf("task %d wears %q, want %q", tc.id, got, tc.want)
		}
	}
}

func indexOfTask(t *testing.T, tasks []apiclient.Task, id int64) int {
	t.Helper()
	for i := range tasks {
		if tasks[i].ID == id {
			return i
		}
	}
	t.Fatalf("no task %d in the fixture", id)
	return -1
}

// TestNothingExpandedIsTodaysBoard is the compatibility half of the design:
// the lanes come from their own request, per expanded parent, so a board with
// nothing open renders exactly the rows it rendered before this feature and
// asks the daemon exactly what it asked before.
func TestNothingExpandedIsTodaysBoard(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	b := testBoard()
	b.client = apiclient.New(srv.URL, "token")
	cmd := b.loadCmd()
	if cmd == nil {
		t.Fatal("loadCmd produced nothing")
	}
	cmd()
	// Bubble Tea batches are a message, not a call: with nothing expanded
	// there is exactly one command and it runs inline above.
	mu.Lock()
	got := append([]string(nil), queries...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "/v1/tasks?" {
		t.Fatalf("the default board issued %v, want one unfiltered /v1/tasks", got)
	}

	// And the rows are the daemon's list, one per task, with no marker on an
	// ordinary row.
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{task(1, stateRunning), task(2, stateQueued)}})
	if ids := laneIDs(b); !equalIDs(ids, 1, 2) {
		t.Fatalf("rows = %v, want exactly the two listed tasks", ids)
	}
	out := ansi.Strip(b.render(120, 20))
	if strings.Contains(out, groupGlyphOpen) || strings.Contains(out, groupGlyphFolded) {
		t.Errorf("an ordinary board grew a disclosure marker:\n%s", out)
	}
}

// TestExpandingIssuesOneRequestPerExpandedParent: the refresh that keeps a
// running lane's row live is per parent, and only for parents the reader can
// actually see.
func TestExpandingIssuesOneRequestPerExpandedParent(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	b := testBoard()
	b.client = apiclient.New(srv.URL, "token")
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{task(7, stateAwaitingChildren)}})
	b.lanes.expanded = expandSet{7}
	// A parent that is not on the board is not asked about: an expansion
	// nobody can see is pure load.
	b.lanes.expanded = b.lanes.expanded.toggle(99)

	cmds := b.laneCmds()
	if len(cmds) != 1 {
		t.Fatalf("laneCmds produced %d commands, want one per *reachable* expanded parent", len(cmds))
	}
	cmds[0]()
	mu.Lock()
	got := append([]string(nil), queries...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "parent_id=7" {
		t.Fatalf("the lane fetch asked %v, want parent_id=7", got)
	}
}

// TestLanesStayOutOfEveryCount is task 014 decision 13 held by construction:
// the lanes never enter b.tasks, so the header counts, the flat count and the
// group headers' `n` and `! n` are computed from the list they were always
// computed from.
func TestLanesStayOutOfEveryCount(t *testing.T) {
	b := groupedBoard(
		task(7, stateAwaitingChildren, inProject("api"), inWorkflow("fan")),
		task(8, stateRunning, inProject("api"), inWorkflow("fan")),
	)
	b.now = func() time.Time { return testNow }
	before := ansi.Strip(b.headerLine())

	expand(b, 7,
		laneTask(42, "api", 0, stateBlocked),
		laneTask(43, "web", 1, stateRunning),
	)

	if got := ansi.Strip(b.headerLine()); got != before {
		t.Errorf("the header count changed when a fan-out was expanded:\n got %s\nwant %s", got, before)
	}
	for _, r := range b.allRows() {
		if !r.header {
			continue
		}
		if r.count != 2 {
			t.Errorf("header %v counts %d, want the 2 listed tasks", r.path, r.count)
		}
		if r.attention != 0 {
			t.Errorf("header %v shows %d needing a human; the blocked row is a lane", r.path, r.attention)
		}
	}
	// The lanes are on screen all the same — which is what pays for keeping
	// them out of the numbers.
	if ids := laneIDs(b); !equalIDs(ids, 7, 42, 43, 8) {
		t.Fatalf("rows = %v, want the lanes rendered under their parent", ids)
	}
}

// TestNestedLanesComposeToMaxDepth: a lane may itself be a fan-out parent, and
// the board nests exactly as far as the engine will derive (§7.6
// fan_out.max_depth) — drawing a level deeper would be drawing a tree that
// cannot exist.
func TestNestedLanesComposeToMaxDepth(t *testing.T) {
	b := boardWithCursorOnParent(task(7, stateAwaitingChildren))
	b.applyConfig(boardConfigMsg{laneDepth: config.Default().FanOut.MaxDepth})
	if b.lanes.depth() != 3 {
		t.Fatalf("the board composes to %d levels, want fan_out.max_depth 3", b.lanes.depth())
	}

	expand(b, 7, laneTask(42, "api", 0, stateAwaitingChildren))
	expand(b, 42, laneTask(52, "unit", 0, stateAwaitingChildren, laneChildOf(42)))
	expand(b, 52, laneTask(62, "fast", 0, stateAwaitingChildren, laneChildOf(52)))
	expand(b, 62, laneTask(72, "too-deep", 0, stateRunning, laneChildOf(62)))

	if ids := laneIDs(b); !equalIDs(ids, 7, 42, 52, 62) {
		t.Fatalf("rows = %v, want three levels of lane under the parent and no fourth", ids)
	}
	depths := map[int64]int{7: 0, 42: 1, 52: 2, 62: 3}
	for _, r := range b.allRows() {
		if want, ok := depths[r.task.ID]; ok && r.lane != want {
			t.Errorf("task %d rendered at lane depth %d, want %d", r.task.ID, r.lane, want)
		}
	}
	// The list a bulk action dispatches through walks the same tree to the
	// same depth: a marked lane at level 4 would be a mark on a row nobody
	// can see.
	ordered := make([]int64, 0, 4)
	for _, task := range b.orderedTasks() {
		ordered = append(ordered, task.ID)
	}
	if !equalIDs(ordered, 7, 42, 52, 62) {
		t.Fatalf("orderedTasks = %v, want the rendered rows (7 42 52 62)", ordered)
	}
	// Only the parents whose lanes can actually be drawn are refetched.
	if got := b.expandedParents(); !equalIDs(got, 7, 42, 52) {
		t.Fatalf("expandedParents = %v, want the three whose lanes fit the depth", got)
	}

	// A daemon configured shallower is honoured, and the default stands in
	// before any config has landed.
	b.applyConfig(boardConfigMsg{laneDepth: 1})
	if ids := laneIDs(b); !equalIDs(ids, 7, 42) {
		t.Fatalf("with max_depth 1 the rows are %v, want one level of lane", ids)
	}
	if (&laneTree{}).depth() != laneDepthDefault {
		t.Errorf("a board with no config composes to %d, want the %d default",
			(&laneTree{}).depth(), laneDepthDefault)
	}
}

// TestLaneDepthDefaultMatchesTheConfigDefault holds the two definitions of
// "how deep" together, the way boardgroup_test.go holds the grouping's.
func TestLaneDepthDefaultMatchesTheConfigDefault(t *testing.T) {
	if got := config.Default().FanOut.MaxDepth; got != laneDepthDefault {
		t.Fatalf("config.Default().FanOut.MaxDepth is %d, the board's default is %d", got, laneDepthDefault)
	}
}

// TestExpandedSetNeverReachesTUIState is the decision, not an implementation
// detail: the expanded set is session-only. A task id is not a label path, and
// task 054's rule for pruning a fold whose project has left the board has no
// honest counterpart for a task that was archived while the TUI was down.
func TestExpandedSetNeverReachesTUIState(t *testing.T) {
	dir := t.TempDir()
	b := groupedBoard(task(7, stateAwaitingChildren, inProject("api"), inWorkflow("fan")))
	b.setDataDir(dir)
	expand(b, 7, laneTask(42, "api", 0, stateBlocked))

	// Everything that writes the file: a fold, and the prune a load runs.
	b.folds = b.folds.with(foldPath{"api"})
	b.persistFolds()
	b.updateLoaded(boardLoadedMsg{seq: 9, tasks: []apiclient.Task{
		task(7, stateAwaitingChildren, inProject("api"), inWorkflow("fan")),
	}})

	raw, err := os.ReadFile(filepath.Join(dir, "tui.json"))
	if err != nil {
		t.Fatalf("tui.json: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("tui.json is not an object: %v\n%s", err, raw)
	}
	for key := range fields {
		switch key {
		case foldsKey, chatFoldsKey, "full_auto_notice_ack", "status_line_declined":
		default:
			t.Errorf("tui.json grew a %q field; the expanded set is session-only", key)
		}
	}
	for _, id := range []string{"7", "42"} {
		if strings.Contains(string(raw), id) {
			t.Errorf("tui.json names task %s:\n%s", id, raw)
		}
	}
	// And a fresh board reading the same dir opens with nothing expanded.
	fresh := newBoard()
	fresh.setDataDir(dir)
	if len(fresh.lanes.expanded) != 0 {
		t.Errorf("a board built from %s opened with %v expanded", dir, fresh.lanes.expanded)
	}
}

// TestALaneRowIsAnOrdinaryTaskRow is the risk this unit was carved out for:
// boardrows.go feeds folding, the bulk selection, the attention badge and the
// column ladder, and an indented lane has to behave in every one of them.
func TestALaneRowIsAnOrdinaryTaskRow(t *testing.T) {
	b := groupedBoard(
		task(7, stateAwaitingChildren, inProject("api"), inWorkflow("fan")),
		task(8, stateRunning, inProject("web"), inWorkflow("build")),
	)
	b.now = func() time.Time { return testNow }
	blocked := laneTask(42, "api", 0, stateBlocked)
	blocked.ProjectName, blocked.Workflow = "api", "fan"
	expand(b, 7, blocked, laneTask(43, "web", 1, stateDone))

	// The attention badge, on the lane's own row.
	out := ansi.Strip(b.render(140, 24))
	if !strings.Contains(out, attentionBadge+" "+stateBlocked) {
		t.Errorf("the blocked lane's row has no attention badge:\n%s", out)
	}

	// `!` and `V` walk it: visible() is what both read.
	ids := make([]int64, 0, 4)
	for _, task := range b.visible() {
		ids = append(ids, task.ID)
	}
	if !equalIDs(ids, 7, 42, 43, 8) {
		t.Fatalf("visible() = %v, want the lanes among the tasks", ids)
	}

	// The bulk selection: a marked lane survives a refresh and is dispatched
	// with the rest.
	b.marks = b.marks.toggle(42)
	b.updateLoaded(boardLoadedMsg{seq: 3, tasks: b.tasks})
	if !b.marks.has(42) {
		t.Fatal("a refresh pruned the mark off a lane; the lane is live, it is only absent from a listing that excludes lanes")
	}
	targets := make([]int64, 0, 1)
	for _, m := range b.markedTargets() {
		targets = append(targets, m.id)
	}
	if !equalIDs(targets, 42) {
		t.Fatalf("markedTargets = %v, want the marked lane", targets)
	}

	// Folding: a collapsed group swallows the whole subtree, lanes included.
	b.folds = b.folds.with(foldPath{"api"})
	rows := b.rows()
	for _, r := range rows {
		if !r.header && (r.task.ID == 42 || r.task.ID == 43) {
			t.Fatalf("a collapsed group left lane %d on screen:\n%v", r.task.ID, rowLabels(rows))
		}
	}
}

// TestNarrowBoardShedsColumnsWithLanesOnIt: the column ladder is width
// arithmetic over the rendered rows, and a lane row is one of them — a row
// that disagreed with the column set would index the table out of range.
func TestNarrowBoardShedsColumnsWithLanesOnIt(t *testing.T) {
	parent := task(7, stateAwaitingChildren)
	parent.Title = "fan out"
	b := boardWithCursorOnParent(parent)
	expand(b, 7, laneTask(42, "api", 0, stateBlocked))
	for _, width := range []int{60, 80, 100, 140, 200} {
		out := ansi.Strip(b.render(width, 20))
		if !strings.Contains(out, strconv.Itoa(42)) {
			t.Errorf("the lane row vanished at width %d:\n%s", width, out)
		}
		// The indent is the first thing a narrow board would be tempted to
		// spend, and it is the only thing saying which parent the row is under.
		if !strings.Contains(out, groupIndent+"api lane") {
			t.Errorf("the lane lost its indent at width %d:\n%s", width, out)
		}
	}
}
