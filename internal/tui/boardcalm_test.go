package tui

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The calm board (task 129.15): the board says only what needs a look. At
// zero the attention clause is absent, healthy adapters are one dim
// `agents ✓`, a grouping level every shown task shares draws no header, the
// in-frame action line is gone and the app header drops `● connected`.

// agentBoard is a board that has heard from /v1/info with these adapters.
func agentBoard(agents ...apiclient.AgentStatus) *board {
	b := testBoard()
	b.update(boardInfoMsg{info: apiclient.Info{MaxParallelTasks: headerCap, Agents: agents}})
	return b
}

func healthyAgent(name string) apiclient.AgentStatus {
	return apiclient.AgentStatus{Name: name, Available: true}
}

func loggedOutAgent(name string) apiclient.AgentStatus {
	out := false
	return apiclient.AgentStatus{Name: name, Available: true, LoggedIn: &out}
}

func spentAgent(name string) apiclient.AgentStatus {
	return apiclient.AgentStatus{Name: name, Available: true, Quota: &apiclient.AgentQuota{
		Source: apiclient.QuotaSourceObserved, ObservedAt: testNow.Add(-time.Minute),
		ResetsAt: testNow.Add(time.Hour), ResetsAtReported: true,
	}}
}

func missingAgent(name string) apiclient.AgentStatus {
	return apiclient.AgentStatus{Name: name}
}

func TestHeaderOmitsTheAttentionClauseAtZero(t *testing.T) {
	b := servedBoard(apiclient.InfoSlots{Used: 1}, task(1, stateRunning))
	if got := headerText(b); strings.Contains(got, "need attention") {
		t.Errorf("header = %q, want no attention clause at zero", got)
	}
}

func TestHeaderNamesANonZeroAttentionCount(t *testing.T) {
	b := servedBoard(apiclient.InfoSlots{}, task(1, stateBlocked), task(2, stateAwaitingInput))
	got := headerText(b)
	if !strings.Contains(got, attentionBadge+" 2 need attention") {
		t.Errorf("header = %q, want %q", got, attentionBadge+" 2 need attention")
	}
	if strings.Contains(got, "all tasks") {
		t.Errorf("header = %q says (all tasks) with no filter committed", got)
	}
}

// TestHeaderAttentionIgnoresAFilter: the count is global by design, and says
// so while a filter is hiding some of what it counts.
func TestHeaderAttentionIgnoresAFilter(t *testing.T) {
	b := servedBoard(apiclient.InfoSlots{},
		task(1, stateBlocked, withTitle("alpha")),
		task(2, stateAwaitingInput, withTitle("beta")),
		task(3, stateRunning, withTitle("gamma")),
	)
	b.filter.SetValue("gamma")
	b.commitFilter()
	if got := headerText(b); !strings.Contains(got, attentionBadge+" 2 need attention (all tasks)") {
		t.Errorf("header = %q, want the unfiltered count marked (all tasks)", got)
	}
}

func TestAgentStripNamesOnlyWhatNeedsALook(t *testing.T) {
	for _, tc := range []struct {
		name   string
		agents []apiclient.AgentStatus
		want   string
	}{
		{"all healthy", []apiclient.AgentStatus{healthyAgent("claude"), healthyAgent("codex")}, "agents ✓"},
		{"one logged out", []apiclient.AgentStatus{healthyAgent("claude"), loggedOutAgent("codex")}, "agents ✓ codex ⚠"},
		{
			"one spent",
			[]apiclient.AgentStatus{healthyAgent("claude"), spentAgent("cursor")},
			"agents ✓ cursor " + quotaBadge(spentAgent("cursor").Quota, testNow),
		},
		{"none healthy", []apiclient.AgentStatus{loggedOutAgent("codex")}, "codex ⚠"},
		{"one missing", []apiclient.AgentStatus{healthyAgent("claude"), missingAgent("cursor")}, "agents ✓"},
		{"none installed", []apiclient.AgentStatus{missingAgent("claude"), missingAgent("codex")}, "no adapters"},
		{"empty catalog", nil, "no adapters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := agentBoard(tc.agents...)
			if got := ansi.Strip(b.agentsSummary()); got != tc.want {
				t.Errorf("agent strip = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAgentStripNeverReddensAMissingAdapter: not installed is not a problem
// on the board — the doctor and the daemon view list the catalog.
func TestAgentStripNeverReddensAMissingAdapter(t *testing.T) {
	b := agentBoard(healthyAgent("claude"), missingAgent("cursor"))
	got := b.agentsSummary()
	if strings.Contains(ansi.Strip(got), "cursor") {
		t.Errorf("agent strip %q names an adapter that is not installed", ansi.Strip(got))
	}
	if strings.Contains(got, styleBad.Render("cursor ✗")) || strings.Contains(ansi.Strip(got), "✗") {
		t.Errorf("agent strip %q marks a missing adapter as bad", got)
	}
}

// TestCalmHeaderSurvivesNoColour: the tick and the badge are glyphs, so the
// line still reads without colour (§15 Colour).
func TestCalmHeaderSurvivesNoColour(t *testing.T) {
	b := agentBoard(healthyAgent("claude"), loggedOutAgent("codex"))
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{task(1, stateBlocked)}})
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ASCII}
	if _, err := w.Write([]byte(b.headerLine())); err != nil {
		t.Fatalf("downgrade write: %v", err)
	}
	for _, want := range []string{attentionBadge + " 1 need attention", "agents ✓", "codex ⚠"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("no-colour header %q lost %q", buf.String(), want)
		}
	}
}

// TestSingleProjectDrawsWorkflowHeadersOnly: each level is judged on its own.
func TestSingleProjectDrawsWorkflowHeadersOnly(t *testing.T) {
	b := groupedBoard(
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateBlocked, inProject("api"), inWorkflow("docs")),
	)
	var labels []string
	for _, r := range b.rows() {
		if r.header {
			labels = append(labels, r.label)
			if r.depth != 0 {
				t.Errorf("header %q at depth %d, want the outermost drawn level", r.label, r.depth)
			}
		}
	}
	if !slices.Equal(labels, []string{"docs", "build"}) {
		t.Errorf("headers = %v, want the two workflows and no project", labels)
	}
	// The badge lands on the header that is still drawn.
	if r := b.rows()[0]; r.attention != 1 {
		t.Errorf("docs header attention = %d, want 1", r.attention)
	}
	s, _ := newShellFixture(t, b.tasks...)
	s.board.group, s.board.configGroup = defaultGrouping(), defaultGrouping()
	if got := s.panelTitle(panelTasks); got != "Tasks · api" {
		t.Errorf("title = %q, want the skipped project named", got)
	}
}

func TestSingleValueBoardIsFlatAndNamedInTheTitle(t *testing.T) {
	s, _ := newShellFixture(t,
		task(1, stateQueued, inProject("api"), inWorkflow("verify-build")),
		task(2, stateQueued, inProject("api"), inWorkflow("verify-build")),
	)
	s.board.group, s.board.configGroup = defaultGrouping(), defaultGrouping()
	for _, r := range s.board.rows() {
		if r.header {
			t.Fatalf("a one-project, one-workflow board drew the header %q", r.label)
		}
		if r.depth != 0 {
			t.Errorf("task %d at depth %d, want 0 with no headers", r.task.ID, r.depth)
		}
	}
	if got := s.panelTitle(panelTasks); got != "Tasks · api › verify-build" {
		t.Errorf("title = %q, want both skipped values in level order", got)
	}
	// The grouped columns stay dropped: the title is where the value lives.
	cols, _ := boardColumns(200, s.board.group, false)
	for _, c := range cols {
		if c.Title == "PROJECT" || c.Title == "WORKFLOW" {
			t.Errorf("%s column came back on a grouped board", c.Title)
		}
	}
}

func TestTwoProjectsDrawBothLevels(t *testing.T) {
	b := twoProjectBoard()
	var paths []string
	for _, r := range b.rows() {
		if r.header {
			paths = append(paths, strings.Join(r.path, "/"))
		}
	}
	want := []string{"api", "api/build", "api/docs", "web", "web/build"}
	if !slices.Equal(paths, want) {
		t.Errorf("headers = %v, want %v", paths, want)
	}
}

func TestCursorStepsOverOneHeaderLevel(t *testing.T) {
	b := groupedBoard(
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateQueued, inProject("api"), inWorkflow("docs")),
	)
	b.render(160, 20)
	if got, _ := b.selected(); got != 1 {
		t.Fatalf("first render selected %d, want 1", got)
	}
	b.updateKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if r := b.rowAt(b.tbl.Cursor()); r.header {
		t.Fatalf("down parked the cursor on the header %q", r.label)
	}
	if got, _ := b.selected(); got != 2 {
		t.Fatalf("down selected %d, want 2", got)
	}
	b.updateKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if got, _ := b.selected(); got != 1 {
		t.Fatalf("up selected %d, want 1", got)
	}
}

// TestFoldOnASkippedLevelHidesNothing: a remembered fold for a level that
// draws no header cannot hide rows and is not rewritten — and it applies
// again the moment the level splits.
func TestFoldOnASkippedLevelHidesNothing(t *testing.T) {
	b := groupedBoard(
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateQueued, inProject("api"), inWorkflow("docs")),
	)
	b.folds = foldSet{{"api"}}
	b.render(160, 20)
	shown := 0
	for _, r := range b.rows() {
		if !r.header {
			shown++
		}
	}
	if shown != 2 {
		t.Fatalf("a fold on a skipped project hid tasks: %d of 2 shown", shown)
	}
	// ← on a task folds its workflow group, and a second ← has no drawn
	// parent to walk out to: neither touches the skipped level's entry.
	foldPress(b, keyLeft)
	foldPress(b, keyLeft)
	if !b.folds.has(foldPath{"api"}) || !b.folds.has(foldPath{"api", "build"}) || len(b.folds) != 2 {
		t.Fatalf("folds = %v, want the skipped [api] untouched beside [api build]", b.folds)
	}

	// A second project splits the level: its old fold applies again.
	b.updateLoaded(boardLoadedMsg{tasks: append(slices.Clone(b.tasks),
		task(3, stateQueued, inProject("web"), inWorkflow("build")))})
	b.render(160, 20)
	i := headerIndex(b.rows(), foldPath{"api"})
	if i < 0 || !b.rows()[i].collapsed {
		t.Fatalf("the api fold did not come back when the project level split: %+v", b.rows())
	}
}

// TestBoardDrawsNoActionLine: the §6 keys live in the footer only.
func TestBoardDrawsNoActionLine(t *testing.T) {
	b := testBoard()
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{
		task(1, stateRunning, func(t *apiclient.Task) {
			t.AvailableActions = footerTarget.actions
		}),
	}})
	b.actions = &actionBar{}
	out := ansi.Strip(b.render(80, 20))
	for _, action := range footerTarget.actions {
		if strings.Contains(out, " "+action) {
			t.Errorf("the board frame still lists %q:\n%s", action, out)
		}
	}
	footer := ansi.Strip(renderFooter(80, bindingsFor(ctxTasks), b.actions, b.target(), 0, false))
	for _, action := range footerTarget.actions {
		if n := strings.Count(footer, " "+action); n != 1 {
			t.Errorf("80-column footer lists %q %d times, want once: %q", action, n, footer)
		}
	}
}

// TestFirstRowLineAndClicksWithZeroOneTwoHeaderLevels: the click math reads
// firstRowLine and the row list, whatever number of levels draw headers.
func TestFirstRowLineAndClicksWithZeroOneTwoHeaderLevels(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tasks []apiclient.Task
	}{
		{"none", []apiclient.Task{
			task(1, stateQueued, inProject("api"), inWorkflow("build")),
			task(2, stateQueued, inProject("api"), inWorkflow("build")),
		}},
		{"one", []apiclient.Task{
			task(1, stateQueued, inProject("api"), inWorkflow("build")),
			task(2, stateQueued, inProject("api"), inWorkflow("docs")),
		}},
		{"two", []apiclient.Task{
			task(1, stateQueued, inProject("api"), inWorkflow("build")),
			task(2, stateQueued, inProject("web"), inWorkflow("docs")),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := groupedBoard(tc.tasks...)
			lines := strings.Split(ansi.Strip(b.render(160, 20)), "\n")
			rows := b.rows()
			first := b.firstRowLine()
			if first >= len(lines) || !strings.Contains(lines[first], rowText(rows[0])) {
				t.Fatalf("line %d = %q, want the first row %q", first, lines[first], rowText(rows[0]))
			}
			for i := len(rows) - 1; i >= 0; i-- {
				if rows[i].header {
					continue
				}
				b.clickRow(i)
				if got, _ := b.selected(); got != rows[i].task.ID {
					t.Errorf("click on row %d selected %d, want %d", i, got, rows[i].task.ID)
				}
			}
		})
	}
}

func rowText(r boardRow) string {
	if r.header {
		return r.label
	}
	return r.task.Title
}

func TestAppHeaderHidesConnectedOnly(t *testing.T) {
	m := connectedRoot(t)
	if got := ansi.Strip(m.headerLine()); strings.Contains(got, "connected") {
		t.Errorf("connected header = %q, want no connection clause", got)
	}
	m.phase = phaseReconnecting
	if got := ansi.Strip(m.headerLine()); !strings.Contains(got, "reconnecting") {
		t.Errorf("reconnecting header = %q, want the clause shown", got)
	}
	m.phase = phaseFailed
	if got := ansi.Strip(m.headerLine()); !strings.Contains(got, "disconnected") {
		t.Errorf("disconnected header = %q, want the clause shown", got)
	}
}
