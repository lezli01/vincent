package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// `!` crossing projects against the real handlers (task 132 decision 52).

// awaitInput moves a queued task to awaiting_input the way the engine does.
func awaitInput(t *testing.T, st *store.Store, task *store.Task) {
	t.Helper()
	ctx := context.Background()
	for _, step := range [][2]store.TaskState{
		{store.TaskQueued, store.TaskRunning},
		{store.TaskRunning, store.TaskAwaitingInput},
	} {
		if _, _, err := st.TransitionTask(ctx, task.ID, step[0], step[1], store.TaskChange{}); err != nil {
			t.Fatalf("TransitionTask(%d, %s → %s): %v", task.ID, step[0], step[1], err)
		}
	}
	task.State = store.TaskAwaitingInput
}

// TestAttentionKeyCrossesProjects: two presses walk the selected project's
// attention tasks, the next goes to the next project by name that has one —
// `mid` before `zeta`, though `zeta` was registered first, and `kilo`, which
// has none, skipped — opening its task and naming the switch; after the last
// project, `!` wraps back to the first project's first attention task.
func TestAttentionKeyCrossesProjects(t *testing.T) {
	h := newSwitchHarness(t)
	ctx := context.Background()
	project := func(name string) int64 {
		p := &store.Project{Name: name, Path: "/" + name, DefaultBranch: "main"}
		if err := h.st.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
		return p.ID
	}
	kilo, mid := project("kilo"), project("mid")
	h.p.until(10*time.Second, "all four projects to be listed", func() bool { return len(h.m.projects) == 4 })

	a1 := h.taskIn(t, h.first.ID, "a-one")
	a2 := h.taskIn(t, h.first.ID, "a-two")
	h.taskIn(t, kilo, "kilo-quiet")
	m1 := h.taskIn(t, mid, "mid-one")
	z1 := h.taskIn(t, h.second.ID, "zeta-one")
	for _, task := range []*store.Task{a1, a2, m1, z1} {
		awaitInput(t, h.st, task)
	}
	b := h.m.views[viewHome].(*shell).board
	h.p.until(20*time.Second, "every attention task on the board", func() bool {
		return b.attentionTally().total() == 4
	})
	if got := b.attentionTally(); got.here != 2 || got.elsewhere != 2 {
		t.Fatalf("tally = %+v, want 2 here and 2 elsewhere", got)
	}
	if got := content(h.m); !strings.Contains(got, "(! 2 elsewhere)") {
		t.Fatalf("no badge in the header:\n%s", got)
	}

	selected := func() int64 { id, _ := b.selected(); return id }
	// Board order within the first project is the band sort's, read back
	// rather than assumed; the cursor starts on its first.
	order := b.attentionTargets(h.first.ID)
	if len(order) != 2 {
		t.Fatalf("the first project's attention targets = %v, want two", order)
	}
	first, second := order[0].ID, order[1].ID
	b.selectedID = first
	b.restoreSelection(b.rows())

	h.key("!")
	if h.m.sel.id != h.first.ID || selected() != second {
		t.Fatalf("first !: selection %d, task %d; want %d's %d", h.m.sel.id, selected(), h.first.ID, second)
	}
	if h.m.selNotice != "" {
		t.Fatalf("a press inside the selection raised %q", h.m.selNotice)
	}

	h.key("!")
	if h.m.sel.id != mid || selected() != m1.ID || h.m.active != viewTask {
		t.Fatalf("second !: selection %d, task %d, view %v; want mid %d, %d, the workspace",
			h.m.sel.id, selected(), h.m.active, mid, m1.ID)
	}
	if want := fmt.Sprintf("! — switched to `mid` (task #%d needs you)", m1.ID); h.m.selNotice != want {
		t.Fatalf("notice = %q, want %q", h.m.selNotice, want)
	}
	if got := content(h.m); !strings.Contains(got, "(! 3 elsewhere)") {
		t.Fatalf("the badge did not follow the selection:\n%s", got)
	}

	h.key("!")
	if h.m.sel.id != h.second.ID || selected() != z1.ID {
		t.Fatalf("third !: selection %d, task %d; want zeta %d, %d", h.m.sel.id, selected(), h.second.ID, z1.ID)
	}

	h.key("!")
	if h.m.sel.id != h.first.ID || selected() != first {
		t.Fatalf("fourth !: selection %d, task %d; want the wrap to %d's %d", h.m.sel.id, selected(), h.first.ID, first)
	}
	h.key("!")
	if h.m.sel.id != h.first.ID || selected() != second {
		t.Fatalf("fifth !: selection %d, task %d; want %d inside the first project", h.m.sel.id, selected(), second)
	}
}

// TestAttentionKeyAsksOverADraft: a `!` that would cross projects from a
// dirty new-task form goes through the root's draft confirmation (decision
// 36), and y lands on the other project's task with the notice.
func TestAttentionKeyAsksOverADraft(t *testing.T) {
	h := newFormSwitchHarness(t)
	task := &store.Task{
		ProjectID: h.second.ID, Title: "waits", WorkflowName: "three",
		WorkflowSnapshot: threeStepWorkflow, BaseBranch: "main", State: store.TaskQueued,
	}
	if err := h.st.CreateTask(context.Background(), task, func(int64) (string, error) { return "b-waits", nil }); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	awaitInput(t, h.st, task)
	b := h.m.views[viewHome].(*shell).board
	h.p.until(20*time.Second, "the other project's question on the board", func() bool {
		return b.attentionTally().elsewhere == 1
	})

	h.p.push(h.m.openNewTask())
	n := h.m.views[viewNewTask].(*newTask)
	h.p.until(10*time.Second, "the form to load", func() bool { return n.loaded })
	n.touched, n.mode = true, ntNavigating

	h.key("!")
	if h.m.pending == nil || h.m.pending.open == nil || h.m.sel.id != h.first.ID {
		t.Fatalf("! over a draft: pending %+v, selection %d; want the question asked", h.m.pending, h.m.sel.id)
	}
	if got := content(h.m); !strings.Contains(got, "discard the task draft and switch to `zeta`? y/n") {
		t.Fatalf("no confirmation on screen:\n%s", got)
	}
	h.key("y")
	if h.m.sel.id != h.second.ID || h.m.active != viewTask || h.m.selectedTask != task.ID {
		t.Fatalf("y: selection %d, view %v, task %d; want zeta's %d in the workspace",
			h.m.sel.id, h.m.active, h.m.selectedTask, task.ID)
	}
	if want := fmt.Sprintf("! — switched to `zeta` (task #%d needs you)", task.ID); h.m.selNotice != want {
		t.Fatalf("notice = %q, want %q", h.m.selNotice, want)
	}
}
