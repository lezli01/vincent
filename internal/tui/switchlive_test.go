package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// switchHarness is the board harness with a second project, both listed in
// the root's cache and the first one selected (task 132.6).
type switchHarness struct {
	*boardLiveHarness
	first, second apiclient.Project
}

func newSwitchHarness(t *testing.T) *switchHarness {
	t.Helper()
	h := newBoardLiveHarness(t)
	other := &store.Project{Name: "zeta", Path: "/elsewhere", DefaultBranch: "main"}
	if err := h.st.CreateProject(context.Background(), other); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	h.p.until(10*time.Second, "both projects to be listed", func() bool { return len(h.m.projects) == 2 })
	h.p.until(10*time.Second, "the first project to be selected", func() bool { return h.m.sel.id == h.projectID })
	s := &switchHarness{boardLiveHarness: h}
	for _, p := range h.m.projects {
		if p.ID == h.projectID {
			s.first = p
		} else {
			s.second = p
		}
	}
	return s
}

// taskIn creates a task in project id.
func (h *switchHarness) taskIn(t *testing.T, id int64, title string) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: id, Title: title, WorkflowName: "three",
		WorkflowSnapshot: threeStepWorkflow, BaseBranch: "main", State: store.TaskQueued,
	}
	if err := h.st.CreateTask(context.Background(), task, func(int64) (string, error) { return "b-" + title, nil }); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func (h *switchHarness) key(s string) {
	r := []rune(s)[0]
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: r, Text: s})
	h.p.push(cmd)
}

func (h *switchHarness) persisted() int64 {
	if sp := readTUIState(h.m.dataDir).SelectedProject; sp != nil {
		return sp.ID
	}
	return 0
}

// TestSwitchFallsBackFromTheWorkspace: a switch on the task workspace lands on
// the board with the back stack empty and the workspace no longer watching.
func TestSwitchFallsBackFromTheWorkspace(t *testing.T) {
	h := newSwitchHarness(t)
	task := h.taskIn(t, h.first.ID, "here")
	_, cmd := h.m.Update(selectTaskMsg{id: task.ID, projectID: h.first.ID})
	h.p.push(cmd)
	tv := h.m.views[viewTask].(*taskView)
	h.p.until(10*time.Second, "the workspace to load", func() bool { return tv.detail.loaded })
	tv.stack = []int64{99}

	h.p.push(h.m.selectProject(h.second, "test"))
	if h.m.active != viewHome {
		t.Fatalf("active after a switch on the workspace = %v, want the board", h.m.active)
	}
	if len(tv.stack) != 0 {
		t.Fatalf("back stack after a switch = %v, want empty", tv.stack)
	}
	if tv.detail.active {
		t.Fatal("the workspace still counts itself watched after the switch")
	}
	if h.m.selNotice != "" {
		t.Fatalf("a picked switch raised %q; only a follow does", h.m.selNotice)
	}
}

// TestSwitchFallsBackFromChatAndIssue: the chat and the issue detail land on
// their lists, and the chat's stream is stopped.
func TestSwitchFallsBackFromChatAndIssue(t *testing.T) {
	h := newSwitchHarness(t)
	cv := h.m.views[viewChat].(*chatView)
	stopped := false
	cv.chatID, cv.streamStop = 5, func() { stopped = true }
	h.p.push(h.m.switchTo(viewChat))
	h.p.push(h.m.selectProject(h.second, "test"))
	if h.m.active != viewChats || !stopped || cv.chatID != 0 {
		t.Fatalf("after a switch on a chat: active %v, stream stopped %v, chat %d; want the chats board, true, 0",
			h.m.active, stopped, cv.chatID)
	}

	h.p.push(h.m.switchTo(viewIssue))
	h.p.push(h.m.selectProject(h.first, "test"))
	if h.m.active != viewIssues {
		t.Fatalf("active after a switch on an issue = %v, want the issues list", h.m.active)
	}
}

// TestSwitchKeepsListViews: every list view, and the two views that are not
// project-bearing, stay in front across a switch.
func TestSwitchKeepsListViews(t *testing.T) {
	h := newSwitchHarness(t)
	next := h.second
	for _, id := range []viewID{
		viewHome, viewChats, viewIssues, viewPullRequests, viewArchived,
		viewArchivedChats, viewWorkflows, viewTriggers, viewDaemon, viewProjects,
	} {
		h.p.push(h.m.switchTo(id))
		h.p.push(h.m.selectProject(next, "test"))
		if h.m.active != id || h.m.sel.id != next.ID {
			t.Fatalf("switch on view %d: active %v, selection %d; want it kept and %d", id, h.m.active, h.m.sel.id, next.ID)
		}
		if next == h.second {
			next = h.first
		} else {
			next = h.second
		}
	}
}

// formSwitchHarness is the new-task harness — the one whose agent catalog
// answers — with a second git project beside its own.
type formSwitchHarness struct {
	*newTaskLiveHarness
	first, second apiclient.Project
}

func newFormSwitchHarness(t *testing.T) *formSwitchHarness {
	t.Helper()
	h := newNewTaskLiveHarness(t)
	other := &store.Project{Name: "zeta", Path: testrepo.Init(t, "main"), DefaultBranch: "main"}
	if err := h.st.CreateProject(context.Background(), other); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	h.p.until(10*time.Second, "both projects to be listed", func() bool { return len(h.m.projects) == 2 })
	h.p.until(10*time.Second, "the first project to be selected", func() bool { return h.m.sel.id == h.projectID })
	f := &formSwitchHarness{newTaskLiveHarness: h}
	for _, p := range h.m.projects {
		if p.ID == h.projectID {
			f.first = p
		} else {
			f.second = p
		}
	}
	return f
}

func (h *formSwitchHarness) key(s string) {
	h.sendKey(tea.KeyPressMsg{Code: []rune(s)[0], Text: s})
}

func (h *formSwitchHarness) persisted() int64 {
	if sp := readTUIState(h.m.dataDir).SelectedProject; sp != nil {
		return sp.ID
	}
	return 0
}

// TestSwitchRetargetsAPristineForm: the new-task form is reopened on the new
// selection, and its catalogs follow it.
func TestSwitchRetargetsAPristineForm(t *testing.T) {
	h := newFormSwitchHarness(t)
	h.p.push(h.m.openNewTask())
	n := h.m.views[viewNewTask].(*newTask)
	h.p.until(10*time.Second, "the form to load on the first project", func() bool {
		return n.loaded && n.projectID == h.first.ID
	})
	h.p.push(h.m.selectProject(h.second, "test"))
	if h.m.pending != nil {
		t.Fatal("a pristine form asked before switching")
	}
	h.p.until(10*time.Second, "the form to reload on the second project", func() bool {
		return n.loaded && n.projectID == h.second.ID
	})
	if h.m.active != viewNewTask {
		t.Fatalf("active = %v, want the form kept", h.m.active)
	}
}

// TestSwitchAsksOverADraft: a touched form and a seeded one ask; n keeps
// everything, y lands on a pristine form for the new project.
func TestSwitchAsksOverADraft(t *testing.T) {
	for _, c := range []struct {
		name  string
		dirty func(n *newTask)
	}{
		{"touched", func(n *newTask) { n.touched = true; n.titleIn.SetValue("half a thought") }},
		{"seeded from a pull request", func(n *newTask) { n.pull = &apiclient.GitHubPullRequest{Number: 7} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newFormSwitchHarness(t)
			h.p.push(h.m.openNewTask())
			n := h.m.views[viewNewTask].(*newTask)
			h.p.until(10*time.Second, "the form to load", func() bool { return n.loaded })
			h.p.until(10*time.Second, "the selection to persist", func() bool { return h.persisted() == h.first.ID })
			c.dirty(n)
			title := n.titleIn.Value()

			h.p.push(h.m.selectProject(h.second, "test"))
			if h.m.pending == nil || h.m.sel.id != h.first.ID {
				t.Fatalf("a %s form switched without asking (selection %d)", c.name, h.m.sel.id)
			}
			if got := content(h.m); !strings.Contains(got, "discard the task draft and switch to `zeta`? y/n") {
				t.Fatalf("no confirmation on screen: %q", got)
			}
			h.key("n")
			if h.m.pending != nil || h.m.sel.id != h.first.ID || n.titleIn.Value() != title {
				t.Fatalf("n: pending %v, selection %d, title %q; want nothing changed", h.m.pending, h.m.sel.id, n.titleIn.Value())
			}
			if !strings.Contains(h.m.headerLine(), h.first.Name) || h.persisted() != h.first.ID {
				t.Fatal("n moved the header or tui.json")
			}

			h.p.push(h.m.selectProject(h.second, "test"))
			h.key("y")
			if h.m.sel.id != h.second.ID || h.m.active != viewNewTask {
				t.Fatalf("y: selection %d, active %v; want the second project on the form", h.m.sel.id, h.m.active)
			}
			if n.touched || n.pull != nil || n.titleIn.Value() != "" {
				t.Fatal("y kept the draft; want a fresh form")
			}
			h.p.until(10*time.Second, "the fresh form to load on the second project", func() bool {
				return n.loaded && n.projectID == h.second.ID
			})
			h.p.until(10*time.Second, "the switch to persist", func() bool { return h.persisted() == h.second.ID })
		})
	}
}

// TestFollowCancelledWithTheSwitch: n on a follow's prompt drops the open.
func TestFollowCancelledWithTheSwitch(t *testing.T) {
	h := newFormSwitchHarness(t)
	h.p.push(h.m.openNewTask())
	n := h.m.views[viewNewTask].(*newTask)
	h.p.until(10*time.Second, "the form to load", func() bool { return n.loaded })
	n.touched = true
	h.send(selectTaskMsg{id: 99, projectID: h.second.ID})
	if h.m.pending == nil || h.m.pending.open == nil {
		t.Fatal("a follow over a draft did not ask")
	}
	h.key("n")
	if h.m.active != viewNewTask || h.m.sel.id != h.first.ID || !n.touched {
		t.Fatalf("n: active %v, selection %d; want the form, its draft and the first project", h.m.active, h.m.sel.id)
	}
}

// TestCreatedTaskFollowsWithoutAsking: a task created from the form in a
// project other than the selection follows there without the draft prompt —
// the draft is the task just sent — and lands on it with the form reset.
func TestCreatedTaskFollowsWithoutAsking(t *testing.T) {
	h := newFormSwitchHarness(t)
	h.p.push(h.m.openNewTask())
	n := h.m.views[viewNewTask].(*newTask)
	h.p.until(10*time.Second, "the form to load", func() bool { return n.loaded })
	n.touched = true
	n.submitting = true
	h.send(taskCreatedMsg{task: apiclient.TaskDetail{Task: apiclient.Task{ID: 99, ProjectID: h.second.ID}}})
	if h.m.pending != nil {
		t.Fatalf("a created task asked to discard its own draft: %+v", h.m.pending)
	}
	if h.m.sel.id != h.second.ID || h.m.active != viewTask {
		t.Fatalf("selection %d, active %v; want the second project's task", h.m.sel.id, h.m.active)
	}
	if n.submitting {
		t.Fatal("the form was left on \"creating…\"")
	}
}

// TestSwitchGuardsWorkspaceAndIssueForms: the same root prompt guards a dirty
// follow-up form and a dirty issue edit, and y takes each view with it.
func TestSwitchGuardsWorkspaceAndIssueForms(t *testing.T) {
	h := newSwitchHarness(t)
	tv := h.m.views[viewTask].(*taskView)
	tv.detail.followUp = newFollowUpForm(1, h.first.ID, "")
	tv.detail.followUp.cursor = fuBody
	tv.detail.followUp.update(tea.KeyPressMsg{Code: tea.KeyEnter}, nil)
	tv.detail.followUp.update(tea.KeyPressMsg{Code: 'x', Text: "x"}, nil)
	if !tv.detail.followUp.dirty {
		t.Fatal("typing into the follow-up form left it pristine")
	}
	h.p.push(h.m.switchTo(viewTask))
	h.p.push(h.m.selectProject(h.second, "test"))
	if h.m.pending == nil || h.m.pending.draft != "follow-up" {
		t.Fatalf("pending = %+v, want the follow-up guarded", h.m.pending)
	}
	h.key("y")
	if h.m.active != viewHome || tv.detail.followUp != nil {
		t.Fatalf("y: active %v, form %v; want the board and the form gone", h.m.active, tv.detail.followUp)
	}

	iv := h.m.views[viewIssue].(*issueView)
	iv.w.form = newIssueForm(nil, nil, &apiclient.Issue{ID: 3, ProjectID: h.second.ID, Title: "old", Editable: []string{"title"}}, h.second.ID)
	iv.w.form.title.SetValue("new")
	h.p.push(h.m.switchTo(viewIssue))
	h.p.push(h.m.selectProject(h.first, "test"))
	if h.m.pending == nil || h.m.pending.draft != "unsaved issue edit" {
		t.Fatalf("pending = %+v, want the issue edit guarded", h.m.pending)
	}
	h.key("y")
	if h.m.active != viewIssues || iv.w.form != nil {
		t.Fatalf("y: active %v; want the issues list and the form gone", h.m.active)
	}
}

// TestOpenFollowsTheObject: an open of another project's task switches first,
// says so, persists the selection and clears the back stack; a same-project
// one does none of that.
func TestOpenFollowsTheObject(t *testing.T) {
	h := newSwitchHarness(t)
	here := h.taskIn(t, h.first.ID, "here")
	there := h.taskIn(t, h.second.ID, "there")
	tv := h.m.views[viewTask].(*taskView)

	_, cmd := h.m.Update(selectTaskMsg{id: here.ID, projectID: h.first.ID})
	h.p.push(cmd)
	_, cmd = h.m.Update(openTaskMsg{id: here.ID, from: 77, projectID: h.first.ID})
	h.p.push(cmd)
	if h.m.selNotice != "" || len(tv.stack) != 1 {
		t.Fatalf("a same-project open: notice %q, stack %v; want none and [77]", h.m.selNotice, tv.stack)
	}

	_, cmd = h.m.Update(openTaskMsg{id: there.ID, from: here.ID, projectID: h.second.ID})
	h.p.push(cmd)
	if h.m.sel.id != h.second.ID || h.m.active != viewTask || tv.detail.taskID != there.ID {
		t.Fatalf("follow: selection %d, active %v, task %d; want %d, the workspace, %d",
			h.m.sel.id, h.m.active, tv.detail.taskID, h.second.ID, there.ID)
	}
	if h.m.selNotice != "switched to `zeta`" {
		t.Fatalf("notice = %q, want the switch named", h.m.selNotice)
	}
	if len(tv.stack) != 0 {
		t.Fatalf("stack after a follow = %v, want empty", tv.stack)
	}
	h.p.until(10*time.Second, "the follow to persist", func() bool { return h.persisted() == h.second.ID })

	// A created task follows the same way.
	_, cmd = h.m.Update(taskCreatedMsg{task: apiclient.TaskDetail{Task: apiclient.Task{ID: here.ID, ProjectID: h.first.ID}}})
	h.p.push(cmd)
	if h.m.sel.id != h.first.ID || h.m.selNotice != "switched to `board`" {
		t.Fatalf("created: selection %d, notice %q", h.m.sel.id, h.m.selNotice)
	}
}

// TestOpenWithoutAProjectFetchesFirst: an open that does not say its project
// GETs the task and still switches before it routes.
func TestOpenWithoutAProjectFetchesFirst(t *testing.T) {
	h := newSwitchHarness(t)
	there := h.taskIn(t, h.second.ID, "there")
	_, cmd := h.m.Update(selectTaskMsg{id: there.ID})
	if h.m.active == viewTask {
		t.Fatal("routed before the project was known")
	}
	h.p.push(cmd)
	h.p.until(10*time.Second, "the fetched follow", func() bool {
		return h.m.active == viewTask && h.m.sel.id == h.second.ID
	})
}

// TestTriggerLedgerOpenCarriesTheProject: the ledger's open names the
// trigger's project as the daemon resolved it.
func TestTriggerLedgerOpenCarriesTheProject(t *testing.T) {
	v := newTriggersView()
	id := int64(12)
	v.list.Triggers = []apiclient.TriggerSummary{{ID: "nightly", ProjectID: 4}}
	v.ledgerID = "nightly"
	v.ledger = []apiclient.TriggerDelivery{{TaskID: &id}}
	msg, ok := v.openDelivery()().(selectTaskMsg)
	if !ok || msg.id != 12 || msg.projectID != 4 {
		t.Fatalf("openDelivery = %+v, want task 12 in project 4", msg)
	}
}
