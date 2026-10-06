package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// Task 132.7 against the real handlers: a deleted selection is replaced, a
// rename is persisted, and a reconnect reloads only the views whose last load
// failed.

// deleteOver deletes a project through the API, as another client would.
func deleteOver(t *testing.T, m *root, id int64) {
	t.Helper()
	if err := m.client.DeleteProject(context.Background(), id, true); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
}

// addProject registers one more project straight in the store.
func addProject(t *testing.T, st *store.Store, name string) *store.Project {
	t.Helper()
	pr := &store.Project{Name: name, Path: "/nowhere/" + name, DefaultBranch: "main"}
	if err := st.CreateProject(context.Background(), pr); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return pr
}

func TestDeletedSelectionFallsToTheFirstByName(t *testing.T) {
	h := newSwitchHarness(t) // `board` selected, `zeta` listed
	deleteOver(t, h.m, h.first.ID)
	h.p.until(10*time.Second, "the selection to move off the deleted project", func() bool { return h.m.sel.id == h.second.ID })
	if got := h.m.headerLine(); !strings.Contains(got, headerProjectGlyph+" zeta") {
		t.Errorf("header %q does not show the replacement", got)
	}
	if want := "project `board` was deleted — showing `zeta` (first by name)"; h.m.selNotice != want {
		t.Errorf("notice = %q, want %q", h.m.selNotice, want)
	}
	h.p.until(10*time.Second, "the replacement to reach tui.json", func() bool { return h.persisted() == h.second.ID })
}

func TestDeletedSelectionPrefersTheDefaultProject(t *testing.T) {
	cfg := func() config.Config {
		c := config.Default()
		yak := "yak"
		c.TUI.DefaultProject = &yak
		return c
	}
	h := newBoardLiveHarnessConfig(t, cfg)
	addProject(t, h.st, "alpha")
	yak := addProject(t, h.st, "yak")
	h.p.until(10*time.Second, "all three projects to be listed", func() bool { return len(h.m.projects) == 3 })
	h.p.until(10*time.Second, "the config answer to land", func() bool { return h.m.defaultProject == "yak" })
	deleteOver(t, h.m, h.projectID)
	h.p.until(10*time.Second, "the default project to be selected", func() bool { return h.m.sel.id == yak.ID })
	if want := "project `board` was deleted — showing `yak` (default project)"; h.m.selNotice != want {
		t.Errorf("notice = %q, want %q", h.m.selNotice, want)
	}
}

// Deleting the only project leaves nothing selected, the header says so, and
// every scoped view draws the shared empty state. The board stays the active
// view: nothing force-navigates.
func TestDeletingTheLastProjectEmptiesEveryScopedView(t *testing.T) {
	h := newBoardLiveHarness(t)
	deleteOver(t, h.m, h.projectID)
	h.p.until(10*time.Second, "the selection to clear", func() bool { return h.m.sel.id == 0 && len(h.m.projects) == 0 })
	if got := h.m.headerLine(); !strings.Contains(got, headerProjectGlyph+" no project") {
		t.Errorf("header %q does not say no project", got)
	}
	if want := "project `board` was deleted — no projects remain"; h.m.selNotice != want {
		t.Errorf("notice = %q, want %q", h.m.selNotice, want)
	}
	if h.m.active != viewHome {
		t.Errorf("active view = %v, want the board left in place", h.m.active)
	}
	for _, id := range []viewID{
		viewHome, viewArchived, viewChats, viewArchivedChats, viewIssues,
		viewPullRequests, viewWorkflows, viewTriggers,
	} {
		out := ansi.Strip(h.m.views[id].render(140, 30))
		if !strings.Contains(out, noProjectsEmpty()) {
			t.Errorf("view %v does not draw the shared empty state:\n%s", id, out)
		}
	}
}

// A dirty draft still asks (decision 47), and only y answers: the project is
// gone, so there is no staying.
func TestDeletedSelectionAsksOverADraft(t *testing.T) {
	h := newSwitchHarness(t)
	h.key("n")
	h.p.until(10*time.Second, "the new-task form to open", func() bool { return h.m.active == viewNewTask })
	nt := h.m.views[viewNewTask].(*newTask)
	nt.touched = true

	deleteOver(t, h.m, h.first.ID)
	h.p.until(10*time.Second, "the confirmation", func() bool { return h.m.pending != nil })
	if got := h.m.pending.prompt(); !strings.Contains(got, "`board` was deleted") || !strings.Contains(got, "`zeta`") {
		t.Errorf("prompt = %q", got)
	}
	h.key("n")
	if h.m.pending == nil {
		t.Fatal("n dismissed a confirmation that has nowhere to stay")
	}
	h.key("y")
	h.p.until(10*time.Second, "the switch to apply", func() bool { return h.m.sel.id == h.second.ID })
	if h.m.pending != nil || h.m.active != viewNewTask {
		t.Fatalf("pending %v, active %v; want the form, re-aimed", h.m.pending, h.m.active)
	}
	if nt.touched || nt.selected != h.second.ID {
		t.Errorf("form touched %v, aimed at %d; want a fresh form for zeta", nt.touched, nt.selected)
	}
}

// A switch's target deleted while its draft question is open drops the
// question: y must not select a project that no longer exists, and the
// selection and the draft stay where they were (review F2).
func TestPendingSwitchTargetDeletedWhileAsking(t *testing.T) {
	h := newSwitchHarness(t)
	h.key("n")
	h.p.until(10*time.Second, "the new-task form to open", func() bool { return h.m.active == viewNewTask })
	nt := h.m.views[viewNewTask].(*newTask)
	nt.touched = true
	h.p.push(h.m.selectProject(h.second, "test"))
	if h.m.pending == nil || h.m.pending.project.ID != h.second.ID {
		t.Fatalf("pending = %+v, want the switch to zeta asked", h.m.pending)
	}

	deleteOver(t, h.m, h.second.ID)
	h.p.until(10*time.Second, "the question to be dropped", func() bool { return h.m.pending == nil })
	if want := "project `zeta` was deleted — staying on `board`"; h.m.selNotice != want {
		t.Errorf("notice = %q, want %q", h.m.selNotice, want)
	}
	h.key("y")
	if h.m.sel.id != h.first.ID || h.m.active != viewNewTask || !nt.touched {
		t.Fatalf("selection %d, active %v, draft kept %v; want board, the form and its draft", h.m.sel.id, h.m.active, nt.touched)
	}
	if got := h.persisted(); got == h.second.ID {
		t.Errorf("tui.json carries the deleted project %d", got)
	}
}

// A rename of the selected project reaches the header and tui.json.
func TestRenamedSelectionIsPersisted(t *testing.T) {
	h := newBoardLiveHarness(t)
	name := "renamed"
	if _, err := h.m.client.PatchProject(context.Background(), h.projectID, apiclient.PatchProjectRequest{Name: apiclient.SetOpt(name)}); err != nil {
		t.Fatalf("PatchProject: %v", err)
	}
	h.p.until(10*time.Second, "the rename to reach the selection", func() bool { return h.m.sel.name == name })
	if got := h.m.headerLine(); !strings.Contains(got, headerProjectGlyph+" renamed") {
		t.Errorf("header %q does not show the new name", got)
	}
	h.p.until(10*time.Second, "the rename to reach tui.json", func() bool {
		sp := readTUIState(h.m.dataDir).SelectedProject
		return sp != nil && sp.ID == h.projectID && sp.Name == name
	})
}

// disconnect stands in for a dropped stream: every request now fails, and the
// root hears the note the client would raise.
func (h *switchHarness) disconnect() {
	h.paths.down.Store(true)
	_, cmd := h.m.Update(noteMsg{note: apiclient.DisconnectedNote{RetryIn: time.Second}})
	h.p.push(cmd)
}

// reconnect is the stream coming back.
func (h *switchHarness) reconnect() {
	h.paths.down.Store(false)
	_, cmd := h.m.Update(noteMsg{note: apiclient.ConnectedNote{}})
	h.p.push(cmd)
}

// listStamps is every view with a list load of its own, by its stamps. Not
// the triggers view: the harness's daemon has no triggers directory, so its
// load fails whether connected or not.
func listStamps(m *root) map[viewID]*loadStamps {
	return map[viewID]*loadStamps{
		viewArchived:      &m.views[viewArchived].(*board).stamps,
		viewChats:         &m.views[viewChats].(*chatsView).stamps,
		viewArchivedChats: &m.views[viewArchivedChats].(*chatsView).stamps,
		viewIssues:        &m.views[viewIssues].(*issuesView).stamps,
		viewWorkflows:     &m.views[viewWorkflows].(*workflowsView).stamps,
	}
}

// A switch made while offline fails every load it issues; the reconnect
// reloads each of those views for the new project.
func TestReconnectReloadsViewsAfterAnOfflineSwitch(t *testing.T) {
	reconnectAfterOfflineSwitch(t, newSwitchHarness(t), nil)
}

// The same, with an event-driven refresh landing between the offline loads
// failing and the reconnect (issue #734). The workflows and issues views
// refresh on any project.* event, and on a loaded runner the harness's own
// project.created for the second project can still be on its way then; CI
// read the refresh it starts as a second reload by the reconnect ("view 4: 2
// loads for project 2 after the reconnect"). Renaming the first project here
// makes that refresh happen on every run instead of on a slow one. The
// reconnect still owes each failed view exactly one reload, so the count must
// start from the loads issued when the stream comes back.
func TestReconnectReloadsViewsAfterAnOfflineSwitchWithARefreshPending(t *testing.T) {
	h := newSwitchHarness(t)
	reconnectAfterOfflineSwitch(t, h, func() {
		p, err := h.st.GetProject(context.Background(), h.first.ID)
		if err != nil {
			t.Fatalf("GetProject: %v", err)
		}
		p.Name = "renamed-while-offline"
		if err := h.st.UpdateProject(context.Background(), p); err != nil {
			t.Fatalf("UpdateProject: %v", err)
		}
		issues := h.m.views[viewIssues].(*issuesView)
		workflows := h.m.views[viewWorkflows].(*workflowsView)
		h.p.until(10*time.Second, "the project event to open a refresh window", func() bool {
			return issues.refreshWait && workflows.refreshPending
		})
		// The window closes with a load of its own, which fails offline.
		h.p.until(10*time.Second, "the event's refresh to fail", func() bool {
			return !issues.refreshWait && !workflows.refreshPending &&
				viewLoadFailed(issues) && viewLoadFailed(workflows) &&
				issues.stamps.applied == issues.stamps.issued &&
				workflows.stamps.applied == workflows.stamps.issued
		})
	})
}

// reconnectAfterOfflineSwitch is the offline switch and its reconnect, with
// beforeReconnect, when set, run once every offline load has failed and just
// before the stream comes back.
func reconnectAfterOfflineSwitch(t *testing.T, h *switchHarness, beforeReconnect func()) {
	h.disconnect()
	h.key("@")
	if h.m.projPick == nil {
		t.Fatal("the picker did not open while reconnecting")
	}
	if out := ansi.Strip(h.m.projPick.render(60, 12)); !strings.Contains(out, "offline — list may be stale") {
		t.Errorf("the offline picker is not marked stale:\n%s", out)
	}
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	h.p.push(cmd)

	h.p.push(h.m.selectProject(h.second, "test"))
	views := listStamps(h.m)
	failed := func(id viewID) bool { return viewLoadFailed(h.m.views[id]) }
	h.p.until(10*time.Second, "every offline load to fail", func() bool {
		for id := range views {
			if !failed(id) {
				return false
			}
		}
		return true
	})
	issued := map[viewID]uint64{}
	for id, s := range views {
		issued[id] = s.issued
	}

	if beforeReconnect != nil {
		beforeReconnect()
	}
	h.reconnect()
	h.p.until(10*time.Second, "every failed view to reload", func() bool {
		for id := range views {
			if failed(id) {
				return false
			}
		}
		return true
	})
	for id, s := range views {
		if s.project != h.second.ID || s.issued != issued[id]+1 {
			t.Errorf("view %v: %d loads for project %d after the reconnect, want one for %d",
				id, s.issued-issued[id], s.project, h.second.ID)
		}
	}
	if h.m.sel.id != h.second.ID {
		t.Errorf("the offline switch did not hold across the reconnect: %+v", h.m.sel)
	}
}

// A clean reconnect fetches nothing beyond the projects relist and the
// GitHub re-probe, which are both project routes.
func TestCleanReconnectReloadsNoView(t *testing.T) {
	h := newSwitchHarness(t)
	views := listStamps(h.m)
	h.p.settle(10*time.Second, 300*time.Millisecond, "the opening loads to settle", func() (bool, int) {
		return true, len(h.paths.snapshot())
	})
	before := len(h.paths.snapshot())
	issued := map[viewID]uint64{}
	for id, s := range views {
		issued[id] = s.issued
	}
	seq := h.m.projectsSeq
	h.disconnect()
	h.reconnect()
	h.p.settle(10*time.Second, 500*time.Millisecond, "the reconnect's fetches to land", func() (bool, int) {
		return h.m.projectsSeq > seq, len(h.paths.snapshot())
	})
	for _, path := range h.paths.snapshot()[before:] {
		// The triggers view's load always fails here (listStamps), so it
		// is the one view a reconnect rightly reloads.
		if !strings.HasPrefix(path, "/v1/projects") && path != "/v1/triggers" {
			t.Errorf("a clean reconnect fetched %s", path)
		}
	}
	for id, s := range views {
		if s.issued != issued[id] {
			t.Errorf("view %v issued %d loads on a clean reconnect", id, s.issued-issued[id])
		}
	}
}

// A delete made while the stream was down never arrives as an event; the
// reconnect's relist catches it (decision 49).
func TestDeleteDuringAnOutageIsCaughtOnReconnect(t *testing.T) {
	h := newSwitchHarness(t)
	h.disconnect()
	h.st.SetEventHook(nil) // the event is lost with the stream
	if err := h.st.DeleteProjectCascade(context.Background(), h.first.ID); err != nil {
		t.Fatalf("DeleteProjectCascade: %v", err)
	}
	h.st.SetEventHook(h.broker.Publish)
	if h.m.sel.id != h.first.ID {
		t.Fatalf("the selection moved before the reconnect: %+v", h.m.sel)
	}
	h.reconnect()
	h.p.until(10*time.Second, "the relist to replace the deleted selection", func() bool { return h.m.sel.id == h.second.ID })
}

// viewLoadFailed reads a stamped view's projectScope.loadFailed.
func viewLoadFailed(v panel) bool {
	switch v := v.(type) {
	case *board:
		return v.loadFailed
	case *chatsView:
		return v.loadFailed
	case *issuesView:
		return v.loadFailed
	case *workflowsView:
		return v.loadFailed
	case *triggersView:
		return v.loadFailed
	}
	return false
}
