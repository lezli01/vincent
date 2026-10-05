package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 132.14: tasks that need a human stay visible across projects — the
// app header's `(! N elsewhere)` badge, the bell for every project, the
// per-project slot clause, and `!` crossing projects (decisions 52–55). The
// crossing itself is proved against the real handlers in
// attention_cross_live_test.go.

// attentionRoot is a root whose board holds tasks, with the fixture project
// selected and the other one listed beside it.
func attentionRoot(t *testing.T, tasks ...apiclient.Task) *root {
	t.Helper()
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m.projects = []apiclient.Project{
		{ID: testProjectID, Name: "proj"},
		{ID: otherProjectID, Name: "other"},
	}
	m.sel = projectSel{id: testProjectID, name: "proj"}
	s := m.views[viewHome].(*shell)
	s.setProject(m.sel)
	s.board.loaded = true
	s.board.tasks = tasks
	return m
}

// TestElsewhereBadgeCountsOtherProjectsRoots: the badge counts other
// projects' root tasks in a NeedsHuman state — never an `awaiting_children`
// parent, and never the selected project's own — on every view, the
// overview included, and is omitted at zero.
func TestElsewhereBadgeCountsOtherProjectsRoots(t *testing.T) {
	other := inProjectID(otherProjectID, "other")
	m := attentionRoot(t,
		task(1, stateAwaitingInput),
		task(2, stateAwaitingInput, other),
		task(3, stateAwaitingChildren, other),
		task(4, stateRunning, other),
		task(5, stateAwaitingGate, inProjectID(3, "third")),
	)
	for _, v := range []viewID{viewHome, viewProjects, viewChats} {
		m.active = v
		if got := ansi.Strip(m.headerLine()); !strings.Contains(got, "◆ proj (! 2 elsewhere)") {
			t.Errorf("view %v header = %q, want the badge after the project", v, got)
		}
	}

	m.views[viewHome].(*shell).board.tasks = []apiclient.Task{task(1, stateAwaitingInput), task(3, stateAwaitingChildren, other)}
	if got := ansi.Strip(m.headerLine()); strings.Contains(got, "elsewhere") {
		t.Errorf("header = %q, want no badge at zero", got)
	}
}

// TestElsewhereBadgeShedsBeforeTheName extends decision 24's shedding order:
// the tag and the version go first, then the badge, and only then does the
// project name truncate. The `◆` click span never covers the badge.
func TestElsewhereBadgeShedsBeforeTheName(t *testing.T) {
	m := attentionRoot(t, task(2, stateBlocked, inProjectID(otherProjectID, "other")))
	m.version = "0.9.0"
	m.sel.name = "a-rather-long-project-name-here"
	m.views[viewHome].(*shell).setProject(m.sel)

	m.width = 160
	m.headerLine()
	wide := m.headerHit

	// Room for the name, but not for the version and the badge as well.
	m.width = 60
	got := ansi.Strip(m.headerLine())
	if !strings.Contains(got, "(! 1 elsewhere)") || strings.Contains(got, "0.9.0") {
		t.Fatalf("at 60 columns header = %q, want the version shed and the badge kept", got)
	}
	if m.headerHit[1]-m.headerHit[0] != wide[1]-wide[0] {
		t.Errorf("the ◆ span is %v, was %v: it grew or shrank with the badge", m.headerHit, wide)
	}
	at := strings.Index(got, "(!")
	if x := ansi.StringWidth(got[:at]); m.headerHit[1] > x {
		t.Errorf("the ◆ span %v reaches the badge at %d", m.headerHit, x)
	}

	m.width = 44
	got = ansi.Strip(m.headerLine())
	if strings.Contains(got, "elsewhere") || !strings.Contains(got, m.sel.name) {
		t.Fatalf("at 44 columns header = %q, want the badge shed and the name whole", got)
	}

	m.width = 30
	if got := ansi.Strip(m.headerLine()); strings.Contains(got, "elsewhere") || !strings.Contains(got, "…") {
		t.Fatalf("at 30 columns header = %q, want the name truncated with no badge", got)
	}
}

// TestBellRingsForEveryProject: the stream stays global (decision 16), and
// nothing scoped stands between it and the bell — a task in another project
// entering awaiting_input rings once while this one is selected.
func TestBellRingsForEveryProject(t *testing.T) {
	m := attentionRoot(t, task(2, stateRunning, inProjectID(otherProjectID, "other")))
	b := m.views[viewHome].(*shell).board
	rings := 0
	b.bell = func() { rings++ }
	if cmd := b.updateNote(stateEvent(7, stateAwaitingInput)); cmd != nil {
		if batch, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					c()
				}
			}
		}
	}
	if rings != 1 {
		t.Fatalf("rings = %d for another project's awaiting_input, want 1", rings)
	}
}

// slotBoard is a served board with the projects listing riding the info
// fetch (decision 54).
func slotBoard(slots apiclient.InfoSlots, projects ...apiclient.Project) *board {
	b := testBoard()
	b.update(boardInfoMsg{
		info:     apiclient.Info{MaxParallelTasks: 8, Slots: slots},
		projects: projects,
	})
	return b
}

// TestHeaderSlotClauseIsPerProject is decisions 11, 33, 54 and 55: the
// selected project's slots, its own cap when it has one, the daemon's
// figure, then the breakdown clauses — and the global clause when the
// projects answer is missing.
func TestHeaderSlotClauseIsPerProject(t *testing.T) {
	three := 3
	slots := apiclient.InfoSlots{Used: 5, Lanes: 2, AwaitingInput: 1}

	capped := slotBoard(slots, apiclient.Project{ID: testProjectID, SlotsUsed: 2, MaxParallelTasks: &three})
	if got := headerText(capped); !strings.Contains(got, "2 running · cap 3 · daemon 5/8 · 2 lanes · 1 on input") {
		t.Errorf("capped header = %q", got)
	}

	uncapped := slotBoard(slots, apiclient.Project{ID: testProjectID, SlotsUsed: 2})
	got := headerText(uncapped)
	if !strings.Contains(got, "2 running · daemon 5/8") || strings.Contains(got, "cap") {
		t.Errorf("uncapped header = %q, want no cap clause", got)
	}

	// No projects answer: the global clause, not a confident zero.
	none := slotBoard(slots)
	if got := headerText(none); !strings.Contains(got, "5/8 running") || strings.Contains(got, "daemon") {
		t.Errorf("header without a projects answer = %q", got)
	}
	// A failed listing leaves the last good one standing.
	capped.update(boardInfoMsg{info: apiclient.Info{MaxParallelTasks: 8, Slots: slots}, projectsErr: errTest})
	if got := headerText(capped); !strings.Contains(got, "cap 3") {
		t.Errorf("a failed listing dropped the clause: %q", got)
	}

	// The debounced info fetch refreshes it.
	capped.update(boardInfoMsg{
		info:     apiclient.Info{MaxParallelTasks: 8, Slots: apiclient.InfoSlots{Used: 6}},
		projects: []apiclient.Project{{ID: testProjectID, SlotsUsed: 3, MaxParallelTasks: &three}},
	})
	if got := headerText(capped); !strings.Contains(got, "3 running · cap 3 · daemon 6/8") {
		t.Errorf("refreshed header = %q", got)
	}

	// The breakdown is shed first, last clause first.
	narrow := slotBoard(slots, apiclient.Project{ID: testProjectID, SlotsUsed: 2, MaxParallelTasks: &three})
	full := ansi.StringWidth(headerText(narrow))
	narrow.width = full - ansi.StringWidth(" · 1 on input")
	got = headerText(narrow)
	if !strings.Contains(got, "daemon 5/8 · 2 lanes") || strings.Contains(got, "on input") {
		t.Errorf("narrowed header = %q, want the last breakdown clause shed", got)
	}
}

// TestProjectsAfterIsByNameAndWraps is decision 52's order: by name,
// starting after the selection, wrapping, and never the selection itself.
func TestProjectsAfterIsByNameAndWraps(t *testing.T) {
	ps := []apiclient.Project{{ID: 1, Name: "board"}, {ID: 2, Name: "zeta"}, {ID: 3, Name: "Mid"}, {ID: 4, Name: "alpha"}}
	var names []string
	for _, p := range projectsAfter(ps, 3) {
		names = append(names, p.Name)
	}
	if got := strings.Join(names, " "); got != "zeta alpha board" {
		t.Errorf("after Mid = %q, want `zeta alpha board`", got)
	}
}
