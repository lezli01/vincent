package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestProjectScopedRegistry pins which views are project-bearing (task
// 132.2). Every viewID is in exactly one list, so adding a view forces the
// decision of whether the selection reaches it.
func TestProjectScopedRegistry(t *testing.T) {
	scoped := []viewID{
		viewHome, viewTask, viewNewTask, viewWorkflows, viewPullRequests,
		viewChats, viewChat, viewArchived, viewArchivedChats, viewTriggers,
		viewIssues, viewIssue,
	}
	unscoped := []viewID{viewProjects, viewDaemon}
	if got := len(scoped) + len(unscoped); got != int(viewCount) {
		t.Fatalf("%d views classified, viewCount is %d: decide whether the new view is project-bearing", got, viewCount)
	}
	views := newViews(context.Background(), newHyperlinkHolder(), newLevelHolder())
	for _, id := range scoped {
		if _, ok := views[id].(projectScoped); !ok {
			t.Errorf("view %d (%s) does not implement projectScoped", id, views[id].title())
		}
	}
	for _, id := range unscoped {
		if _, ok := views[id].(projectScoped); ok {
			t.Errorf("view %d (%s) implements projectScoped; it is not project-bearing", id, views[id].title())
		}
	}
}

// scopeStub records what the root hands it, in order.
type scopeStub struct {
	calls *[]string
	sel   projectSel
	cmd   tea.Cmd
	msgs  []tea.Msg
}

type scopeStubMsg struct{ name string }

func (s *scopeStub) title() string { return "stub" }
func (s *scopeStub) update(msg tea.Msg) (panel, tea.Cmd) {
	s.msgs = append(s.msgs, msg)
	return s, nil
}
func (s *scopeStub) render(int, int) string { return "" }

//nolint:unparam // clientAware's signature
func (s *scopeStub) setClient(*apiclient.Client) tea.Cmd {
	*s.calls = append(*s.calls, "setClient")
	return nil
}

func (s *scopeStub) setProject(p projectSel) tea.Cmd {
	*s.calls = append(*s.calls, "setProject:"+p.name)
	s.sel = p
	return s.cmd
}

func TestSelectProjectWalksScopedViews(t *testing.T) {
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	var calls []string
	a := &scopeStub{calls: &calls, cmd: func() tea.Msg { return scopeStubMsg{"a"} }}
	b := &scopeStub{calls: &calls, cmd: func() tea.Msg { return scopeStubMsg{"b"} }}
	m.views[viewHome], m.views[viewIssues] = a, b

	cmd := m.selectProject(apiclient.Project{ID: 7, Name: "alpha"}, "test")
	if m.sel != (projectSel{id: 7, name: "alpha"}) || m.selWhy != "test" {
		t.Fatalf("sel = %+v (%q), want alpha/7 chosen by test", m.sel, m.selWhy)
	}
	for _, s := range []*scopeStub{a, b} {
		if s.sel != m.sel {
			t.Errorf("stub got %+v, want %+v", s.sel, m.sel)
		}
	}
	// Every real view stored it too.
	if v, ok := m.views[viewChat].(*chatView); !ok || v.project != m.sel {
		t.Errorf("chat view did not store the selection")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 3 {
		t.Fatalf("selectProject cmd = %T (%d), want both stubs' commands and the save batched", cmd(), len(batch))
	}
	var got []string
	for _, c := range batch[:2] {
		got = append(got, c().(scopeStubMsg).name)
	}
	// The third is decision 27's write of the last-used project.
	batch[2]()
	if st := readTUIState(m.dataDir).SelectedProject; st == nil || *st != (selectedProjectState{ID: 7, Name: "alpha"}) {
		t.Errorf("tui.json selected_project = %+v, want alpha/7", st)
	}
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("batched = %v, want a,b", got)
	}
}

func TestConnectHandsSelectionAfterClient(t *testing.T) {
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	var calls []string
	m.views[viewHome] = &scopeStub{calls: &calls}
	m.sel = projectSel{id: 3, name: "kept"}
	m.updateConnected(connectedMsg{client: apiclient.New("http://127.0.0.1:1", "t")})
	if strings.Join(calls, ",") != "setClient,setProject:kept" {
		t.Fatalf("calls = %v, want setClient then setProject", calls)
	}
}

func TestProjectListSelection(t *testing.T) {
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.client = apiclient.New("http://127.0.0.1:1", "t")
	m.startup.done = true // after the 132.3 chain; startproject_test.go covers it
	land := func(projects ...apiclient.Project) {
		t.Helper()
		_ = m.refreshProjects() // bump the sequence the answer must match
		m.Update(projectListMsg{seq: m.projectsSeq, projects: projects})
	}

	land()
	if m.sel.id != 0 {
		t.Fatalf("an empty list selected %+v", m.sel)
	}
	land(apiclient.Project{ID: 1, Name: "zeta"}, apiclient.Project{ID: 2, Name: "alpha"})
	if m.sel != (projectSel{id: 2, name: "alpha"}) {
		t.Fatalf("sel = %+v, want the first by name", m.sel)
	}
	// Decision 10: a new project does not take over a selection.
	land(apiclient.Project{ID: 1, Name: "zeta"}, apiclient.Project{ID: 2, Name: "alpha"}, apiclient.Project{ID: 3, Name: "aaa"})
	if m.sel.id != 2 {
		t.Fatalf("a new project changed the selection to %+v", m.sel)
	}
	// A rename reaches the selection and the views.
	land(apiclient.Project{ID: 2, Name: "beta"})
	if m.sel.name != "beta" {
		t.Fatalf("rename not adopted: %+v", m.sel)
	}
	if v := m.views[viewIssues].(*issuesView); v.project.name != "beta" {
		t.Errorf("views not re-walked on rename: %+v", v.project)
	}
	// A vanished selection is 132.7's; it is left alone here.
	land(apiclient.Project{ID: 9, Name: "other"})
	if m.sel.id != 2 {
		t.Fatalf("a vanished selection changed to %+v", m.sel)
	}
	// A stale answer is dropped.
	m.sel = projectSel{}
	m.Update(projectListMsg{seq: m.projectsSeq - 1, projects: []apiclient.Project{{ID: 4, Name: "x"}}})
	if m.sel.id != 0 {
		t.Fatalf("a stale list answer selected %+v", m.sel)
	}
}

func TestOpenNewTaskSeedsFromSelection(t *testing.T) {
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	var calls []string
	form := &scopeStub{calls: &calls}
	m.views[viewNewTask] = form
	m.views[viewHome] = &hinter{scopeStub{calls: &calls}, 5}
	m.active = viewHome

	m.openNewTask()
	if got := form.msgs[0]; got != (newTaskMsg{projectID: 5}) {
		t.Fatalf("no selection: form got %+v, want the hint", got)
	}
	m.active = viewHome
	m.sel = projectSel{id: 8, name: "sel"}
	form.msgs = nil
	m.openNewTask()
	if got := form.msgs[0]; got != (newTaskMsg{projectID: 5}) {
		t.Fatalf("hint and selection: form got %+v, want the hint", got)
	}
	m.active = viewHome
	m.views[viewHome] = &hinter{scopeStub{calls: &calls}, 0}
	form.msgs = nil
	m.openNewTask()
	if got := form.msgs[0]; got != (newTaskMsg{projectID: 8}) {
		t.Fatalf("no hint: form got %+v, want the selection", got)
	}
}

// TestNewTaskFromProjectsViewTakesTheCursorRow is review F1 of the 132.2
// train: with one project selected, `n` on the projects view opens the form
// on the row under the cursor, not on the selection. The projects view is
// never project-bearing, so the selection must not override it.
func TestNewTaskFromProjectsViewTakesTheCursorRow(t *testing.T) {
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.phase = phaseConnected
	var calls []string
	form := &scopeStub{calls: &calls}
	m.views[viewNewTask] = form
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "alpha"), testProject(2, "web")}, nil)
	p.render(120, 24)
	p.tbl.SetCursor(1)
	if id, _ := p.selected(); id != 2 {
		t.Fatalf("cursor on project %d, want 2", id)
	}
	m.views[viewProjects] = p
	m.active = viewProjects
	m.sel = projectSel{id: 1, name: "alpha"}

	m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if len(form.msgs) == 0 {
		t.Fatal("n on the projects view did not open the new-task form")
	}
	if got := form.msgs[0]; got != (newTaskMsg{projectID: 2}) {
		t.Fatalf("form got %+v, want the cursor's project 2, not the selected 1", got)
	}
}

type hinter struct {
	scopeStub
	id int64
}

func (h *hinter) hintedProject() int64 { return h.id }

func (h *hinter) update(tea.Msg) (panel, tea.Cmd) { return h, nil }

// TestHeaderProjectSegment covers the segment and its shedding order (task
// 132.2): the tag truncates then goes, then the version, then the name
// truncates; the disconnected badge is never shed.
func TestHeaderProjectSegment(t *testing.T) {
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.phase = phaseConnected
	m.version = "0.9.0"
	header := func(w int) string {
		m.width = w
		return ansi.Strip(m.headerLine())
	}

	for _, w := range []int{60, 80, 120} {
		if got := header(w); !strings.Contains(got, "vincent 0.9.0  ◆ no project  [") {
			t.Errorf("width %d, no selection: %q", w, got)
		}
	}
	m.sel = projectSel{id: 1, name: "web"}
	if got := header(80); !strings.HasPrefix(got, " vincent 0.9.0  ◆ web  [") {
		t.Errorf("selected: %q", got)
	}

	m.sel.name = strings.Repeat("n", 40)
	// 60 columns: the 40-cell name still fits beside the version, so only
	// the tag goes.
	got := header(60)
	if strings.Contains(got, "[") || !strings.Contains(got, "0.9.0") || !strings.Contains(got, m.sel.name) {
		t.Errorf("tag should shed first: %q", got)
	}
	// Narrower: the version goes, the name still whole.
	got = header(52)
	if strings.Contains(got, "0.9.0") || !strings.Contains(got, " vincent  ◆ "+m.sel.name) {
		t.Errorf("version should shed second: %q", got)
	}
	// Narrower still: the name truncates.
	got = header(30)
	if !strings.HasSuffix(got, "…") || ansi.StringWidth(got) > 30 || !strings.HasPrefix(got, " vincent  ◆ n") {
		t.Errorf("name should truncate last: %q", got)
	}

	// The badge is never shed.
	m.phase = phaseReconnecting
	got = header(40)
	if !strings.Contains(got, "reconnecting") || !strings.Contains(got, "◆ n") || ansi.StringWidth(got) > 40 {
		t.Errorf("badge kept, name truncated: %q", got)
	}
	if shellChromeH != 2 {
		t.Errorf("shellChromeH = %d: the segment must not add a chrome row", shellChromeH)
	}
}
