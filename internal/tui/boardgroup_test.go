package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
)

// groupedBoard is the fixture for everything in this file: a board with the
// §15 default grouping, unlike testBoard's flat table.
func groupedBoard(tasks ...apiclient.Task) *board {
	b := newBoard()
	b.now = func() time.Time { return testNow }
	b.bell = func() {}
	b.loaded = true
	b.updateLoaded(boardLoadedMsg{tasks: tasks})
	return b
}

func inProject(name string) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.ProjectName = name }
}

func inWorkflow(name string) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.Workflow = name }
}

// rowLabels renders the row structure compactly: "▾ label" for an expanded
// header at its depth, "▸ label" for a collapsed one, "#id" for a task.
func rowLabels(rows []boardRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.header {
			glyph := groupGlyphOpen
			if r.collapsed {
				glyph = groupGlyphFolded
			}
			out = append(out, strings.Repeat(" ", r.depth)+glyph+" "+r.label)
			continue
		}
		out = append(out, "#"+strconv.FormatInt(r.task.ID, 10))
	}
	return out
}

// TestDefaultGroupingMatchesTheConfigDefault holds the two definitions of
// "default" together: the board renders grouped before any daemon answers,
// and it must be the same grouping the daemon would have told it.
func TestDefaultGroupingMatchesTheConfigDefault(t *testing.T) {
	levels := make([]string, 0, 2)
	for _, l := range config.Default().TUI.Board.GroupBy {
		levels = append(levels, string(l))
	}
	if want := parseGrouping(levels); !defaultGrouping().equal(want) {
		t.Errorf("TUI default grouping = %s, config default = %s",
			defaultGrouping().label(), want.label())
	}
	if b := newBoard(); !b.group.equal(defaultGrouping()) {
		t.Errorf("a fresh board groups %s, want %s", b.group.label(), defaultGrouping().label())
	}
}

// TestGroupRowsNestsWorkflowInsideProject is the default the task asked for:
// projects outermost, the workflows of one project inside it.
func TestGroupRowsNestsWorkflowInsideProject(t *testing.T) {
	b := groupedBoard(
		task(1, stateRunning, inProject("api"), inWorkflow("build"), withStarted(testNow.Add(-time.Minute))),
		task(2, stateRunning, inProject("api"), inWorkflow("docs"), withStarted(testNow.Add(-time.Minute))),
		task(3, stateRunning, inProject("web"), inWorkflow("build"), withStarted(testNow.Add(-time.Minute))),
		task(4, stateRunning, inProject("api"), inWorkflow("build"), withStarted(testNow.Add(-time.Minute))),
	)
	got := rowLabels(b.rows())
	want := []string{
		"▾ api",
		" ▾ build", "#1", "#4",
		" ▾ docs", "#2",
		"▾ web",
		" ▾ build", "#3",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rows =\n  %v\nwant\n  %v", got, want)
	}
}

// TestGroupOrderFollowsTheBandSort is the property grouping was not allowed
// to cost (§15): a task waiting on a human stays at the top of the board, so
// the group holding it comes first even though its project sorts later.
func TestGroupOrderFollowsTheBandSort(t *testing.T) {
	b := groupedBoard(
		task(1, stateRunning, inProject("api"), inWorkflow("build"), withStarted(testNow.Add(-time.Hour))),
		task(2, stateBlocked, inProject("web"), inWorkflow("deploy")),
	)
	rows := b.rows()
	if rows[0].label != "web" {
		t.Fatalf("first group = %q, want the one holding the blocked task", rows[0].label)
	}
	if rows[0].attention != 1 {
		t.Errorf("group %q attention = %d, want 1 — a header must not hide that work is waiting",
			rows[0].label, rows[0].attention)
	}
	cell := rows[0].headerCell()
	if !strings.Contains(cell, attentionBadge) {
		t.Errorf("header cell %q carries no attention badge", cell)
	}
	if !strings.Contains(cell, "web") {
		t.Errorf("header cell %q does not name its group", cell)
	}
}

// TestGroupHeaderCountsItsTasks: the count is what makes a header worth a row.
func TestGroupHeaderCountsItsTasks(t *testing.T) {
	// Queued, so the band sort is scheduler order — id ascending here — and
	// the expected row order reads off the fixture.
	b := groupedBoard(
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateQueued, inProject("api"), inWorkflow("build")),
		task(3, stateQueued, inProject("api"), inWorkflow("docs")),
	)
	rows := b.rows()
	if rows[0].count != 3 {
		t.Errorf("project header count = %d, want 3", rows[0].count)
	}
	if !strings.Contains(rows[0].headerCell(), "3") {
		t.Errorf("project header %q does not show its count", rows[0].headerCell())
	}
	if rows[1].count != 2 {
		t.Errorf("workflow header count = %d, want 2", rows[1].count)
	}
}

// TestGroupValueFallsBackToADash: a task the daemon reported without a
// workflow still belongs somewhere, and a blank header reads as a bug.
func TestGroupValueFallsBackToADash(t *testing.T) {
	b := groupedBoard(task(1, stateQueued, inProject("api")))
	rows := b.rows()
	if rows[1].label != groupUnnamed {
		t.Errorf("empty workflow grouped as %q, want %q", rows[1].label, groupUnnamed)
	}
}

// TestCursorStepsOverGroupHeaders: headers are labels. Moving through the
// table must land on tasks only, in both directions.
func TestCursorStepsOverGroupHeaders(t *testing.T) {
	b := groupedBoard(
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateQueued, inProject("web"), inWorkflow("build")),
	)
	b.render(160, 20)
	if got, ok := b.selected(); !ok || got != 1 {
		t.Fatalf("first render selected %d (ok=%v), want the first task", got, ok)
	}
	if r := b.rowAt(b.tbl.Cursor()); r.header {
		t.Fatal("the cursor came up on a group header")
	}

	// Down crosses this group's end and the next group's two headers.
	b.updateKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if r := b.rowAt(b.tbl.Cursor()); r.header {
		t.Fatalf("down parked the cursor on the header %q", r.label)
	}
	if got, _ := b.selected(); got != 2 {
		t.Fatalf("down selected %d, want 2", got)
	}

	// And back up, over the same two headers.
	b.updateKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if r := b.rowAt(b.tbl.Cursor()); r.header {
		t.Fatalf("up parked the cursor on the header %q", r.label)
	}
	if got, _ := b.selected(); got != 1 {
		t.Fatalf("up selected %d, want 1", got)
	}
}

// TestGroupCycleKeepsTheSelectedTask: `g` is a way to look at the same board
// differently, so the task under the cursor must survive the regrouping —
// the row *index* certainly does not.
func TestGroupCycleKeepsTheSelectedTask(t *testing.T) {
	b := groupedBoard(
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateQueued, inProject("web"), inWorkflow("build")),
	)
	b.render(160, 20)
	b.updateKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if got, _ := b.selected(); got != 2 {
		t.Fatalf("fixture selected %d, want 2", got)
	}

	for _, want := range []grouping{
		{groupProject}, {groupWorkflow}, {}, {groupProject, groupWorkflow},
	} {
		b.updateKey(tea.KeyPressMsg{Code: 'g', Text: "g"})
		if !b.group.equal(want) {
			t.Fatalf("g moved to %s, want %s", b.group.label(), want.label())
		}
		b.render(160, 20)
		if got, ok := b.selected(); !ok || got != 2 {
			t.Fatalf("%s: selected %d (ok=%v), want 2 through the regrouping",
				b.group.label(), got, ok)
		}
		if r := b.rowAt(b.tbl.Cursor()); r.header {
			t.Fatalf("%s: regrouping left the cursor on a header", b.group.label())
		}
	}
}

// TestGroupedColumnsAreDropped: a level named by every header above the rows
// does not also need a column repeating it on every row.
func TestGroupedColumnsAreDropped(t *testing.T) {
	full := columnsFor(160, nil, false)
	if !full.project || !full.workflow {
		t.Fatalf("flat at 160 = %+v, want both columns", full)
	}
	both := columnsFor(160, grouping{groupProject, groupWorkflow}, false)
	if both.project || both.workflow {
		t.Errorf("grouped by project and workflow = %+v, want neither column", both)
	}
	one := columnsFor(160, grouping{groupProject}, false)
	if one.project {
		t.Error("grouping by project kept the PROJECT column")
	}
	if !one.workflow {
		t.Error("grouping by project dropped the WORKFLOW column, which nothing names")
	}
	// The width a dropped column frees is spent on the row rather than lost:
	// on the title first, which is where a grouped board needs it because the
	// titles are indented under their headers, and then — once the title has
	// reached maxTitle — on STEP and STATUS (task 050 decision 1, amending
	// task 036 decision 9, which said "strictly wider" and could not survive
	// a ceiling).
	if both.titleWidth(160) <= full.titleWidth(160) {
		t.Errorf("grouped remainder %d, flat %d — grouping must not cost row space",
			both.titleWidth(160), full.titleWidth(160))
	}
	for _, width := range []int{120, 160, 200, 240, 400} {
		flatCols, _ := boardColumns(width, nil, false)
		groupedCols, _ := boardColumns(width, grouping{groupProject, groupWorkflow}, false)
		flatSpend, groupedSpend := flexibleWidth(flatCols), flexibleWidth(groupedCols)
		if groupedSpend < flatSpend {
			t.Errorf("width %d: grouped spends %d on TITLE/STEP/STATUS, flat %d — grouping must never be worse off",
				width, groupedSpend, flatSpend)
		}
		if groupedTitle, flatTitle := colWidth(groupedCols, "TITLE"), colWidth(flatCols, "TITLE"); groupedTitle < flatTitle {
			t.Errorf("width %d: grouped title %d, flat %d", width, groupedTitle, flatTitle)
		}
	}
}

// flexibleWidth is what a column set spends on the three columns a surplus
// can reach: whatever grouping frees has to turn up in one of them.
func flexibleWidth(cols []table.Column) int {
	total := 0
	for _, c := range cols {
		switch c.Title {
		case "TITLE", "STEP", "STATUS":
			total += c.Width
		}
	}
	return total
}

// colWidth is a named column's width, or zero when the width shed it.
func colWidth(cols []table.Column, title string) int {
	for _, c := range cols {
		if c.Title == title {
			return c.Width
		}
	}
	return 0
}

// TestGroupedRowsMatchTheColumnCount: a row that disagrees with the column
// set indexes the table out of range, which is a panic, not a rendering
// glitch.
// A continuation row is held to the same bar (task 050): it is the next line
// of a wrapped row, and one that forgot a cell is the same panic — so the
// fixture carries a task long enough to wrap at every width above the narrow
// end.
func TestGroupedRowsMatchTheColumnCount(t *testing.T) {
	b := groupedBoard(
		task(1, stateRunning, inProject("api"), inWorkflow("build")),
		task(2, stateRunning, inProject("api"), inWorkflow("build"),
			withTitle(strings.Repeat("word ", 30)),
			withStatus(strings.Repeat("clause ", 30))),
	)
	// 240 is wide enough for the status column (task 036), which is the
	// widest set a row is ever built at.
	for _, width := range []int{40, 70, 90, 120, 200, 240} {
		for _, g := range []grouping{nil, {groupProject}, {groupProject, groupWorkflow}} {
			b.group = g
			// With and without the bulk-selection marker column (task 011):
			// both the task rows and the group headers have to grow the extra
			// cell, and a header that forgot it is the same panic.
			for _, marks := range []markSet{nil, {1}} {
				b.marks = marks
				b.render(width, 30)
				cols, set := boardColumns(width, g, b.hasMarks())
				rows := b.rows()
				for i, row := range b.rowsFor(rows, cols, set) {
					if len(row) != len(cols) {
						t.Fatalf("width %d, %s, marks=%v: row %d has %d cells, %d columns",
							width, g.label(), marks, i, len(row), len(cols))
					}
				}
				// A header is one line at every row height: a group broken
				// across three would be more air than the group has rows.
				for i, r := range rows {
					if r.header && i+1 < len(rows) && rows[i+1].line != 0 {
						t.Fatalf("width %d, %s: header at row %d grew a continuation", width, g.label(), i)
					}
				}
			}
		}
		b.marks = nil
	}
}

// TestGroupHeadersRenderInTheTitleColumn: the header label has to land in the
// one column wide enough to hold a name, at every width.
func TestGroupHeadersRenderInTheTitleColumn(t *testing.T) {
	b := groupedBoard(task(1, stateRunning, inProject("api"), inWorkflow("build")))
	for _, width := range []int{70, 120, 200} {
		out := b.render(width, 20)
		if !strings.Contains(out, "▾ api") {
			t.Errorf("width %d rendered no project header:\n%s", width, out)
		}
		if !strings.Contains(out, "▾ build") {
			t.Errorf("width %d rendered no workflow header:\n%s", width, out)
		}
	}
}

// TestConfiguredGroupingApplies is the point of the config key: the daemon's
// file decides what the board comes up as.
func TestConfiguredGroupingApplies(t *testing.T) {
	b := groupedBoard(task(1, stateRunning))
	b.applyConfig(boardConfigMsg{board: apiclient.ConfigBoard{GroupBy: []string{"workflow"}}})
	if want := (grouping{groupWorkflow}); !b.group.equal(want) {
		t.Errorf("group = %s, want %s", b.group.label(), want.label())
	}

	// An empty list is a configured choice — the flat table — not a missing
	// answer.
	b.applyConfig(boardConfigMsg{board: apiclient.ConfigBoard{GroupBy: []string{}}})
	if len(b.group) != 0 {
		t.Errorf("group = %s, want the flat table the config asked for", b.group.label())
	}

	// A level this TUI predates is dropped, not fatal.
	b.applyConfig(boardConfigMsg{board: apiclient.ConfigBoard{GroupBy: []string{"agent", "project"}}})
	if want := (grouping{groupProject}); !b.group.equal(want) {
		t.Errorf("group = %s, want %s — an unknown level must be ignored", b.group.label(), want.label())
	}
}

// TestConfigFetchFailureKeepsTheGrouping: a config request that timed out is
// not a statement about how anyone wants their board.
func TestConfigFetchFailureKeepsTheGrouping(t *testing.T) {
	b := groupedBoard(task(1, stateRunning))
	b.applyConfig(boardConfigMsg{err: errTest})
	if !b.group.equal(defaultGrouping()) {
		t.Errorf("group = %s after a failed fetch, want the default kept", b.group.label())
	}
}

// TestPressedGroupingSurvivesAReconnect: the config is where the board
// starts, not a setting the daemon re-imposes under someone's hands. A
// reconnect refetches the config, and that must not undo a `g`.
func TestPressedGroupingSurvivesAReconnect(t *testing.T) {
	b := groupedBoard(task(1, stateRunning))
	b.render(160, 20)
	b.updateKey(tea.KeyPressMsg{Code: 'g', Text: "g"})
	pressed := b.group

	b.applyConfig(boardConfigMsg{board: apiclient.ConfigBoard{
		GroupBy: []string{"project", "workflow"},
	}})
	if !b.group.equal(pressed) {
		t.Errorf("group = %s after a refetch, want the pressed %s",
			b.group.label(), pressed.label())
	}
	if !b.configGroup.equal(defaultGrouping()) {
		t.Errorf("configGroup = %s, want what the daemon served", b.configGroup.label())
	}
}

// TestPanelTitleNamesAnUnconfiguredGrouping follows the `v` precedent (§15):
// the headers say what the grouping is, the title says when it is not the one
// the config asked for.
func TestPanelTitleNamesAnUnconfiguredGrouping(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning))
	s.board.group, s.board.configGroup = defaultGrouping(), defaultGrouping()
	if got := s.panelTitle(panelTasks); got != "Tasks" {
		t.Errorf("title = %q under the configured grouping, want a plain %q", got, "Tasks")
	}

	s.board.group = grouping{groupWorkflow}
	if got := s.panelTitle(panelTasks); !strings.Contains(got, "by workflow") {
		t.Errorf("title = %q, want it to name the grouping in effect", got)
	}

	s.board.filter.SetValue("api")
	got := s.panelTitle(panelTasks)
	if !strings.Contains(got, "by workflow") || !strings.Contains(got, "/api") {
		t.Errorf("title = %q, want both the grouping and the committed filter", got)
	}
}

// TestGroupSummaryReadsAsASetting is the daemon view's line: what the file
// says, in words rather than YAML.
func TestGroupSummaryReadsAsASetting(t *testing.T) {
	if got := groupSummary([]string{"project", "workflow"}); got != "by project › workflow" {
		t.Errorf("summary = %q", got)
	}
	if got := groupSummary(nil); !strings.Contains(got, "flat") {
		t.Errorf("summary of no grouping = %q, want it to say the list is flat", got)
	}
}

// TestGroupingEqualAndHas pins the two set predicates directly. Everywhere
// else in this file `equal` is the assertion rather than the subject, so a
// rewrite of it could silently take the assertions with it — which is exactly
// what task 042's `slices.Equal` change does.
func TestGroupingEqualAndHas(t *testing.T) {
	cases := []struct {
		name string
		a, b grouping
		want bool
	}{
		{"identical", grouping{groupProject, groupWorkflow}, grouping{groupProject, groupWorkflow}, true},
		{"both empty", grouping{}, grouping{}, true},
		{"empty and nil", grouping{}, nil, true},
		{"shorter prefix", grouping{groupProject}, grouping{groupProject, groupWorkflow}, false},
		{"longer", grouping{groupProject, groupWorkflow}, grouping{groupProject}, false},
		{"same length, different element", grouping{groupProject}, grouping{groupWorkflow}, false},
		{"same set, reversed", grouping{groupProject, groupWorkflow}, grouping{groupWorkflow, groupProject}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.equal(tc.b); got != tc.want {
				t.Errorf("%v.equal(%v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}

	g := grouping{groupProject}
	if !g.has(groupProject) {
		t.Error("has(groupProject) = false on a grouping that holds it")
	}
	if g.has(groupWorkflow) {
		t.Error("has(groupWorkflow) = true on a grouping that does not hold it")
	}
	if (grouping{}).has(groupProject) {
		t.Error("has = true on a flat grouping")
	}
}
