package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The overview's root side (task 132 decision 42): enter on a project row
// selects it and goes back to the last project-scoped view; enter on a
// "needs you" row selects the task's project and opens the task.

// overviewRoot is a connected root with api selected, sitting on the
// overview after visiting the views in path.
func overviewRoot(t *testing.T, path ...viewID) *root {
	t.Helper()
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.phase = phaseConnected
	m.sel = projectSel{id: 1, name: "api"}
	for _, id := range append(path, viewProjects) {
		m.Update(selectViewMsg{id: id})
	}
	if m.active != viewProjects {
		t.Fatalf("active = %d, want the overview", m.active)
	}
	return m
}

func TestOverviewPickReturnsToTheLastScopedView(t *testing.T) {
	web := apiclient.Project{ID: 2, Name: "web"}
	for _, tc := range []struct {
		name    string
		path    []viewID
		project apiclient.Project
		want    viewID
	}{
		{"chats", []viewID{viewChats}, web, viewChats},
		{"no history", []viewID{viewDaemon}, web, viewHome},
		{"the scoped view before an unscoped one", []viewID{viewIssues, viewDaemon}, web, viewIssues},
		// A record belongs to the project it was opened in: after a switch
		// it gives way to its list, and stays when the project does.
		{"a chat across a switch", []viewID{viewChat}, web, viewChats},
		{"a chat on the same project", []viewID{viewChat}, apiclient.Project{ID: 1, Name: "api"}, viewChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := overviewRoot(t, tc.path...)
			m.Update(overviewPickMsg{project: tc.project})
			if m.active != tc.want {
				t.Errorf("active = %d, want %d", m.active, tc.want)
			}
			if m.sel != (projectSel{id: tc.project.ID, name: tc.project.Name}) {
				t.Errorf("sel = %+v, want %+v", m.sel, tc.project)
			}
		})
	}
}

func TestOverviewAttentionPickOpensTheTask(t *testing.T) {
	m := overviewRoot(t, viewChats)
	task := apiclient.Task{ID: 21, ProjectID: 2, ProjectName: "web", State: stateBlocked}
	_, cmd := m.Update(overviewPickMsg{project: apiclient.Project{ID: 2, Name: "web"}, task: &task})
	if m.sel != (projectSel{id: 2, name: "web"}) {
		t.Fatalf("sel = %+v, want the task's project", m.sel)
	}
	open, ok := findMsg[selectTaskMsg](cmd)
	if !ok {
		t.Fatal("no selectTaskMsg for the picked task")
	}
	if open.id != 21 || open.back != viewProjects {
		t.Errorf("open = %+v, want task 21 with esc back to the overview", open)
	}
	m.Update(open)
	if m.active != viewTask {
		t.Errorf("active = %d, want the task workspace", m.active)
	}
}

// findMsg runs cmd, flattening batches, and returns the first T it produced.
func findMsg[T any](cmd tea.Cmd) (T, bool) {
	var zero T
	if cmd == nil {
		return zero, false
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if got, ok := findMsg[T](c); ok {
				return got, true
			}
		}
	case T:
		return msg, true
	}
	return zero, false
}

// Both ways in reach the overview: the palette's nav row and the picker's
// last row, which is never a project.
func TestPaletteAndPickerReachTheOverview(t *testing.T) {
	found := false
	for _, e := range paletteEntries(ctxTasks, taskActions{}, false, true, false, nil, nil) {
		if e.nav && e.navTarget == viewProjects {
			found = strings.Contains(e.label, "project overview")
		}
	}
	if !found {
		t.Error("the palette has no project overview row")
	}

	m, _ := pickerRoot(t, twoProjects())
	pressRoot(m, atKey())
	for range 3 { // two projects, then the overview row; the last is clamped
		pressRoot(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if pp := m.projPick; !pp.overview || pp.cursor != 1 {
		t.Fatalf("cursor = %d, overview %v; want the overview row after both projects", pp.cursor, pp.overview)
	}
	if out := m.projPick.render(60, 12); !strings.Contains(out, projectPickerOverview) {
		t.Errorf("the picker draws no overview row:\n%s", out)
	}
	pressRoot(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.projPick != nil {
		t.Fatal("enter on the overview row left the picker open")
	}
	if m.active != viewProjects {
		t.Errorf("active = %d, want the overview", m.active)
	}
	if m.sel != (projectSel{id: 1, name: "api"}) {
		t.Errorf("sel = %+v, the overview row must not select a project", m.sel)
	}
}

// The filter narrows the projects and never the overview row, and a
// filter's own enter still lands on a project, not on the overview.
func TestPickerOverviewRowIsNotAProject(t *testing.T) {
	m, _ := pickerRoot(t, twoProjects())
	pressRoot(m, atKey())
	for _, r := range "overview" {
		pressRoot(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := m.projPick.matches(); len(got) != 0 {
		t.Errorf("filter %q matched %v; the overview row is not a project", m.projPick.input.Value(), got)
	}
	m.projPick.input.SetValue("we")
	m.projPick.clampCursor()
	pressRoot(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sel != (projectSel{id: 2, name: "web"}) {
		t.Errorf("sel = %+v, want the filtered project", m.sel)
	}
}
