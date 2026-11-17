package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The board grouped by vincent issue (task 148.2, #983): the default since
// 148.1, rendered from here on. The fixtures are boardgroup_test.go's,
// under the [issue] grouping.

// onIssue starts a task from a local, open vincent issue.
func onIssue(id int64, title string) func(*apiclient.Task) {
	return func(t *apiclient.Task) {
		t.Issue = &apiclient.TaskIssue{ID: id, Title: title, State: "open"}
	}
}

// issueClosed closes the task's issue, as the row carries it.
func issueClosed() func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.Issue.State = issueStateClosed }
}

// issueImported marks the task's issue as imported, with the references the
// daemon renders (task 142.8).
func issueImported(shortRef, ref string) func(*apiclient.Task) {
	return func(t *apiclient.Task) {
		t.Issue.Source = &apiclient.IssueSource{Provider: "github", ShortRef: shortRef, Ref: ref}
	}
}

func headerPathsOf(rows []boardRow) []string {
	var out []string
	for _, r := range rows {
		if r.header {
			out = append(out, strings.Join(r.path, "/"))
		}
	}
	return out
}

// TestGroupRowsByIssue: one header per issue and one `no issue`, in the
// order of each group's first task, keyed by `#<id>` and the reserved
// no-issue key.
func TestGroupRowsByIssue(t *testing.T) {
	b := groupedBoardBy(byIssue(),
		task(1, stateQueued, onIssue(42, "Fix login")),
		task(2, stateQueued),
		task(3, stateQueued, onIssue(42, "Fix login")),
		task(4, stateQueued, onIssue(7, "Docs")),
	)
	wantRows(t, b,
		"▾ issue #42 · Fix login", "#1", "#3",
		"▾ no issue", "#2",
		"▾ issue #7 · Docs", "#4",
	)
	if got, want := headerPathsOf(b.rows()), []string{"#42", groupNoIssue, "#7"}; !slices.Equal(got, want) {
		t.Errorf("header paths = %v, want %v", got, want)
	}
	if r := b.rows()[0]; r.count != 2 {
		t.Errorf("issue #42 count = %d, want 2", r.count)
	}
}

// TestNoIssueGroupFollowsTheBandSort: `no issue` is not pinned last — a
// group holding a task that needs a human rises to the top, whichever it is.
func TestNoIssueGroupFollowsTheBandSort(t *testing.T) {
	b := groupedBoardBy(byIssue(),
		task(1, stateRunning, onIssue(42, "Fix login"), withStarted(testNow.Add(-time.Hour))),
		task(2, stateBlocked),
	)
	r := b.rows()[0]
	if !r.header || r.label != groupNoIssueLabel || r.attention != 1 {
		t.Fatalf("first row = %+v, want the no-issue header carrying the blocked task", r)
	}
}

// TestIssueHeaderLabel: the ref only when imported, short before full, and
// `closed` only when the issue is.
func TestIssueHeaderLabel(t *testing.T) {
	for _, tc := range []struct {
		name       string
		opts       []func(*apiclient.Task)
		want       string
		wantClosed bool
	}{
		{"local open", nil, "issue #42 · Fix login", false},
		{"short ref", []func(*apiclient.Task){issueImported("gh#142", "acme/app#142")}, "issue #42 · Fix login · gh#142", false},
		{"full ref fallback", []func(*apiclient.Task){issueImported("", "PROJ-12")}, "issue #42 · Fix login · PROJ-12", false},
		{"closed", []func(*apiclient.Task){issueClosed()}, "issue #42 · Fix login", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tk := task(1, stateQueued, append([]func(*apiclient.Task){onIssue(42, "Fix login")}, tc.opts...)...)
			label, closed := groupTitle(tk, groupIssue)
			if label != tc.want || closed != tc.wantClosed {
				t.Errorf("groupTitle = %q, %v; want %q, %v", label, closed, tc.want, tc.wantClosed)
			}
			cell := ansi.Strip(boardRow{header: true, label: label, closed: closed, count: 1}.headerCell(0))
			if strings.Contains(cell, "closed") != tc.wantClosed {
				t.Errorf("header %q: closed shown = %v, want %v", cell, !tc.wantClosed, tc.wantClosed)
			}
		})
	}
}

// TestDeletedIssueSnapshotGroupsByID: a deleted issue's tasks keep their
// frozen snapshot (task 130 decision 6), which keeps its id — so they stay
// one group even when two snapshots disagree on the title.
func TestDeletedIssueSnapshotGroupsByID(t *testing.T) {
	b := groupedBoardBy(byIssue(),
		task(1, stateQueued, onIssue(9, "Old title")),
		task(2, stateQueued, onIssue(9, "Renamed")),
		task(3, stateQueued),
	)
	wantRows(t, b, "▾ issue #9 · Old title", "#1", "#2", "▾ no issue", "#3")
}

// TestGroupCycleIsIssueWorkflowFlat: `g` walks issue → workflow → flat and
// back, the cursor follows the task, and a config reload does not undo it.
func TestGroupCycleIsIssueWorkflowFlat(t *testing.T) {
	b := groupedBoardBy(byIssue(),
		task(1, stateQueued, onIssue(42, "A"), inWorkflow("build")),
		task(2, stateQueued, inWorkflow("docs")),
	)
	b.render(160, 20)
	b.selectedID = 2
	b.render(160, 20)
	for _, want := range []grouping{byWorkflow(), {}, byIssue()} {
		b.updateKey(keyGroup)
		if !b.group.equal(want) {
			t.Fatalf("g moved to %s, want %s", b.group.label(), want.label())
		}
		b.render(160, 20)
		if got, ok := b.selected(); !ok || got != 2 {
			t.Fatalf("%s: selected %d (ok=%v), want 2", b.group.label(), got, ok)
		}
	}
	b.updateKey(keyGroup)
	b.applyConfig(boardConfigMsg{board: apiclient.ConfigBoard{GroupBy: []string{"issue"}}})
	if !b.group.equal(byWorkflow()) || !b.configGroup.equal(byIssue()) {
		t.Errorf("after a reload group = %s, config = %s; want the pressed by workflow kept",
			b.group.label(), b.configGroup.label())
	}
	// A two-level grouping from config is outside the cycle: g jumps to its head.
	if got := (grouping{groupIssue, groupWorkflow}).next(); !got.equal(byIssue()) {
		t.Errorf("next of [issue, workflow] = %s, want by issue", got.label())
	}
}

func TestParseGroupingKeepsIssue(t *testing.T) {
	for _, tc := range []struct {
		served []string
		want   grouping
	}{
		{[]string{"issue"}, byIssue()},
		{[]string{"issue", "workflow"}, grouping{groupIssue, groupWorkflow}},
		{[]string{"agent", "issue", "issue"}, byIssue()},
	} {
		if got := parseGrouping(tc.served); !got.equal(tc.want) {
			t.Errorf("parseGrouping(%v) = %s, want %s", tc.served, got.label(), tc.want.label())
		}
	}
	if got := groupSummary([]string{"issue"}); got != "by issue" {
		t.Errorf("groupSummary([issue]) = %q, want %q", got, "by issue")
	}
}

// TestIssueThenWorkflowRendersTwoLevels: [issue, workflow] from config nests
// a workflow inside each issue, and the no-issue group's unnamed workflow is
// a different path from any other.
func TestIssueThenWorkflowRendersTwoLevels(t *testing.T) {
	b := groupedBoardBy(grouping{groupIssue, groupWorkflow},
		task(1, stateQueued, onIssue(42, "A"), inWorkflow("build")),
		task(2, stateQueued, onIssue(42, "A"), inWorkflow("docs")),
		task(3, stateQueued),
	)
	wantRows(t, b,
		"▾ issue #42 · A",
		" ▾ build", "#1",
		" ▾ docs", "#2",
		"▾ no issue",
		" ▾ —", "#3",
	)
	want := []string{"#42", "#42/build", "#42/docs", groupNoIssue, groupNoIssue + "/" + groupUnnamed}
	if got := headerPathsOf(b.rows()); !slices.Equal(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

// TestSingleIssueLevelNamesItsLabelInTheTitle: a level every task shares is
// named in the panel title by its label, never its key (task 129 decision 4).
func TestSingleIssueLevelNamesItsLabelInTheTitle(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateQueued, inWorkflow("a")), task(2, stateQueued, inWorkflow("b")))
	s.board.group, s.board.configGroup = byIssue(), byIssue()
	if got := s.panelTitle(panelTasks); got != "Tasks · no issue" {
		t.Errorf("title = %q, want %q", got, "Tasks · no issue")
	}
	s, _ = newShellFixture(t,
		task(1, stateQueued, onIssue(42, "Fix login"), issueImported("gh#1", ""), issueClosed()),
		task(2, stateQueued, onIssue(42, "Fix login"), issueImported("gh#1", ""), issueClosed()),
	)
	s.board.group, s.board.configGroup = byIssue(), byIssue()
	if got, want := s.panelTitle(panelTasks), "Tasks · issue #42 · Fix login · gh#1 · closed"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
}

// TestIssueFoldsAreKeyedByID: a fold names the issue by id, so a title
// change keeps it; workflow and issue folds both survive cycling and a
// filter; the no-issue fold does not alias the unnamed-workflow one; and a
// key that no longer occurs is pruned.
func TestIssueFoldsAreKeyedByID(t *testing.T) {
	b := groupedBoardBy(byIssue(),
		task(1, stateQueued, onIssue(42, "Fix login"), inWorkflow("build")),
		task(2, stateQueued),
	)
	b.render(160, 20)
	b.setFolds(b.folds().with(foldPath{"#42"}).with(foldPath{"build"}))
	wantRows(t, b, "▸ issue #42 · Fix login", "▾ no issue", "#2")

	// The issue is renamed: the fold still holds.
	b.updateLoaded(boardLoadedMsg{stamp: loadStamp{seq: 2}, tasks: []apiclient.Task{
		task(1, stateQueued, onIssue(42, "Fix sign-in"), inWorkflow("build")),
		task(2, stateQueued),
	}})
	wantRows(t, b, "▸ issue #42 · Fix sign-in", "▾ no issue", "#2")

	// Cycle to workflow: the issue fold is kept, the workflow fold applies.
	b.updateKey(keyGroup)
	b.render(160, 20)
	wantRows(t, b, "▸ build", "▾ —", "#2")
	// A filter leaves both alone.
	b.filter.SetValue("2")
	b.updateKey(keyGroup)
	b.updateKey(keyGroup)
	b.filter.SetValue("")
	if !b.folds().has(foldPath{"#42"}) || !b.folds().has(foldPath{"build"}) {
		t.Errorf("folds = %v, want both kept through cycling and filtering", b.folds())
	}

	// The no-issue key is not the unnamed-workflow key.
	plain := task(3, stateQueued)
	if groupValue(plain, groupIssue) == groupValue(plain, groupWorkflow) {
		t.Fatal("the no-issue key aliases the unnamed-workflow key")
	}
	wf := groupedBoard(task(1, stateQueued, inWorkflow("build")), task(2, stateQueued))
	wf.setFolds(foldSet{{groupNoIssue}})
	wantRows(t, wf, "▾ build", "#1", "▾ —", "#2")

	// Pruning keeps every level's live keys and drops a dead issue.
	got := foldSet{{"#42"}, {"#99"}, {"build"}, {groupNoIssue}}.prune(b.tasks)
	if want := (foldSet{{"#42"}, {"build"}, {groupNoIssue}}); !slices.EqualFunc(got, want, foldPath.equal) {
		t.Errorf("prune = %v, want %v", got, want)
	}
}

// TestIssueGroupHoldsItsLanes: a fan-out's lanes nest under their root, in
// the root's issue group, are swallowed by its fold and stay out of its count.
func TestIssueGroupHoldsItsLanes(t *testing.T) {
	b := groupedBoardBy(byIssue(),
		task(7, stateAwaitingChildren, onIssue(42, "Fan")),
		task(8, stateQueued),
	)
	expand(b, 7, laneTask(43, "api", 0, stateRunning), laneTask(44, "web", 1, stateRunning))
	got := rowLabels(b.rows())
	want := []string{"▾ issue #42 · Fan", "#7", "#43", "#44", "▾ no issue", "#8"}
	if !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if r := b.rows()[0]; r.count != 1 {
		t.Errorf("issue header count = %d, want 1 — lanes are never counted", r.count)
	}
	b.setFolds(b.folds().with(foldPath{"#42"}))
	wantRows(t, b, "▸ issue #42 · Fan", "▾ no issue", "#8")
}

// TestIssueHeaderTruncatesToItsColumn: a long title gives way, the count and
// the badges never do — on a narrow board and on a wrapped one.
func TestIssueHeaderTruncatesToItsColumn(t *testing.T) {
	long := strings.Repeat("a very long issue title ", 8)
	r := boardRow{header: true, label: "issue #42 · " + long, closed: true, count: 3, attention: 1, marked: 2, collapsed: true}
	cell := ansi.Strip(r.headerCell(40))
	if w := ansi.StringWidth(cell); w > 40 {
		t.Errorf("header %q is %d wide, want at most 40", cell, w)
	}
	for _, part := range []string{"…", "closed", "3", attentionBadge + " 1", markGlyph + " 2"} {
		if !strings.Contains(cell, part) {
			t.Errorf("header %q lost %q", cell, part)
		}
	}

	for _, width := range []int{80, 120} {
		b := groupedBoardBy(byIssue(),
			task(1, stateBlocked, onIssue(42, long)),
			task(2, stateQueued, withTitle(long)),
		)
		b.render(width, 20)
		rows := b.rows()
		cols, set := b.columns()
		for i, row := range b.rowsFor(rows, cols, set) {
			if !rows[i].header {
				continue
			}
			for j, c := range row {
				if w := ansi.StringWidth(c); w > cols[j].Width {
					t.Errorf("width %d: header cell %q is %d wide in a %d column", width, ansi.Strip(c), w, cols[j].Width)
				}
			}
		}
	}
}

// TestIssueGroupingKeepsTheWorkflowColumn: [issue] drops no column; only a
// grouping by workflow drops WORKFLOW.
func TestIssueGroupingKeepsTheWorkflowColumn(t *testing.T) {
	for _, width := range []int{120, 200} {
		if !columnsFor(width, byIssue(), false, boardContent{}).workflow {
			t.Errorf("%d: WORKFLOW dropped under [issue]", width)
		}
		if columnsFor(width, byWorkflow(), false, boardContent{}).workflow {
			t.Errorf("%d: WORKFLOW kept under [workflow]", width)
		}
	}
}

// TestFilterMatchesTheIssue: `/` matches the issue's `#<id>`, title and
// short ref, so filtering for an issue keeps its group (task 148 decision 9).
func TestFilterMatchesTheIssue(t *testing.T) {
	tasks := []apiclient.Task{
		task(1, stateQueued, onIssue(42, "Fix login"), issueImported("gh#142", "acme/app#142")),
		task(2, stateQueued),
	}
	for _, q := range []string{"#42", "LOGIN", "gh#142"} {
		got := filterTasks(tasks, q)
		if len(got) != 1 || got[0].ID != 1 {
			t.Errorf("filter %q kept %v, want task 1", q, got)
		}
	}
}

// TestConfigEditorOffersIssue: `issue` is a choice for tui.board.group_by.
func TestConfigEditorOffersIssue(t *testing.T) {
	for _, k := range configKeys() {
		if k.path == "tui.board.group_by" {
			if !slices.Contains(k.choices, "issue") {
				t.Errorf("group_by choices = %v, want issue offered", k.choices)
			}
			return
		}
	}
	t.Fatal("the config editor has no tui.board.group_by key")
}

// TestArchivedBoardGroupsByIssue: the archive is the same code path, grouped
// per page.
func TestArchivedBoardGroupsByIssue(t *testing.T) {
	b := testArchivedBoard()
	b.group, b.configGroup = byIssue(), byIssue()
	b.updateLoaded(boardLoadedMsg{stamp: loadStamp{seq: 9}, tasks: []apiclient.Task{
		task(1, stateArchived, onIssue(42, "Shipped"), issueClosed()),
		task(2, stateArchived),
	}})
	var labels []string
	for _, r := range b.rows() {
		if r.header {
			labels = append(labels, r.label)
		}
	}
	if !slices.Equal(labels, []string{"issue #42 · Shipped", groupNoIssueLabel}) &&
		!slices.Equal(labels, []string{groupNoIssueLabel, "issue #42 · Shipped"}) {
		t.Errorf("archived headers = %v, want the issue and no issue", labels)
	}
	b.width = 100
	cols, set := b.columns()
	for _, r := range b.rowsFor(b.rows(), cols, set) {
		if len(r) != len(cols) {
			t.Fatalf("row %q has %d cells for %d columns", r, len(r), len(cols))
		}
	}
}
