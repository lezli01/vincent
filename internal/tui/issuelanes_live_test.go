package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// laneWalkWorkflows is one manual gate under `hand_off: explicit`: the gate
// keeps the task unsettled until the test approves it, and explicit keeps a
// finished task's issue idle in progress, so `.` has a move to make.
var laneWalkWorkflows = map[string]string{
	"walk.yaml": `name: walk
hand_off: explicit
fields:
  - {name: issue, type: string}
steps:
  - {id: gate, type: manual, instructions: review}
`,
}

// An issue's lane is derived, never set (task 134 decision 1, kept by task
// 146 decision 1), so moving a card runs the action its lane is derived
// from. This walks one issue through every lane from the issues list with
// `m` (task 145), against the real handlers and engine:
//
//	open ─m→ in progress ─(task done)→ idle ─m→ hand-off ─m→ in progress
//	     ─.→ hand-off ─m→ done ─m→ out of done
//
// and a second issue from in progress back to open, `m` cancelling its task.
func TestIssueLaneMovesFromTheBoard(t *testing.T) {
	h := newNewTaskLiveHarnessWith(t, liveOptions{workflows: laneWalkWorkflows})
	ctx := t.Context()
	iss := h.seedIssue(t, "Walk the lanes")
	list := issuesListView(t, h)
	h.send(selectViewMsg{id: viewIssues})

	// laneOf is the lane the board draws the issue in — the row's own
	// section, not the store's opinion, since the board is what is tested.
	laneOf := func(id int64) string {
		for _, row := range list.rows() {
			if !row.header && row.issue.ID == id {
				return issueSections[row.section].lane
			}
		}
		return ""
	}
	waitLane := func(id int64, lane string) {
		t.Helper()
		h.p.until(20*time.Second, "issue in the "+lane+" lane", func() bool {
			return h.m.active == viewIssues && list.w.act == nil && laneOf(id) == lane
		})
	}
	// press selects the issue first: every write moves it, and a re-list
	// puts the cursor back on `selected`.
	press := func(id int64, key string) {
		t.Helper()
		list.selectIssue(id)
		h.sendKey(keyPress(key))
	}
	// moveTo opens `m`'s picker and picks lane in it.
	moveTo := func(id int64, lane string) {
		t.Helper()
		press(id, issueMoveKey)
		h.p.until(10*time.Second, "the lane picker", func() bool {
			return list.w.act != nil && list.w.act.stage == iaMove
		})
		p := list.w.act.pick
		for range len(p.options) {
			if opt, ok := p.current(); ok && opt.value == lane {
				break
			}
			h.sendKey(keyPress("down"))
		}
		if opt, _ := p.current(); opt.value != lane {
			t.Fatalf("the picker offers no %s", lane)
		}
		h.sendKey(keyPress("enter"))
	}

	waitLane(iss.ID, "open")

	// open → hand-off is refused in the picker, which stays open.
	moveTo(iss.ID, "hand_off")
	if list.w.act == nil || list.w.act.pick == nil || list.w.act.pick.err == "" {
		t.Fatal("open → hand-off was not refused with a reason")
	}
	h.sendKey(keyPress("esc"))

	// open → in progress: the form opens seeded from the issue, and the task
	// it creates is the issue's root task.
	moveTo(iss.ID, "in_progress")
	n := h.form(t)
	h.p.until(10*time.Second, "the seeded form", func() bool {
		return h.m.active == viewNewTask && n.loaded && n.issue != nil && n.issue.ID == iss.ID
	})
	h.p.push(n.applyPick(ntWorkflow, "walk", false))
	h.p.until(10*time.Second, "walk's prefill", func() bool {
		_, ok := n.fieldValue("issue")
		return ok
	})
	h.sendKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	var task store.Task
	h.p.until(20*time.Second, "the issue's task", func() bool {
		tasks := h.createdTasks(t, iss.ID)
		if len(tasks) == 1 {
			task = tasks[0]
		}
		return len(tasks) == 1
	})
	// The create lands on the new task's workspace; go back to the board
	// only once it has, or the late switch would take the board away again.
	h.p.until(10*time.Second, "the task workspace", func() bool { return h.m.active == viewTask })
	h.send(selectViewMsg{id: viewIssues})
	waitLane(iss.ID, "in_progress")

	// in progress → hand-off while the task runs: refused, nothing moves.
	h.p.until(20*time.Second, "the gate", func() bool {
		got, err := h.st.GetTask(ctx, task.ID)
		return err == nil && got.State == "awaiting_gate"
	})
	moveTo(iss.ID, "hand_off")
	if list.w.act == nil || !strings.Contains(list.w.act.pick.err, "still running") {
		t.Fatal("hand-off with a running task was not refused")
	}
	h.sendKey(keyPress("esc"))
	if laneOf(iss.ID) != "in_progress" {
		t.Fatalf("a refused move moved the card to %q", laneOf(iss.ID))
	}

	// The task finishes: under hand_off: explicit the issue stays in
	// progress, idle, until someone marks it ready.
	if _, err := h.m.client.Approve(ctx, task.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	h.p.until(20*time.Second, "the idle marker", func() bool {
		for _, row := range list.rows() {
			if !row.header && row.issue.ID == iss.ID {
				return row.issue.Lane == "in_progress" && issueMarker(row.issue) == markerIdle
			}
		}
		return false
	})

	// in progress ⇄ hand-off by `m`, and by `.`, which does the same.
	moveTo(iss.ID, "hand_off")
	waitLane(iss.ID, "hand_off")
	moveTo(iss.ID, "in_progress")
	waitLane(iss.ID, "in_progress")
	press(iss.ID, ".")
	waitLane(iss.ID, "hand_off")

	// hand-off → done: the close asks for a reason (completed is first).
	moveTo(iss.ID, "done")
	h.p.until(10*time.Second, "the reason prompt", func() bool {
		return list.w.act != nil && list.w.act.stage == iaReason
	})
	h.sendKey(keyPress("enter"))
	h.p.until(10*time.Second, "the close", func() bool {
		got, err := h.st.GetIssue(ctx, iss.ID)
		return err == nil && string(got.State) == "closed" && list.w.act == nil
	})
	if !list.showDone {
		h.sendKey(keyPress("s"))
	}
	waitLane(iss.ID, "done")

	// done → open reopens; the finished task puts it back in hand-off, and
	// the note says so rather than claiming open.
	moveTo(iss.ID, "open")
	waitLane(iss.ID, "hand_off")
	if !strings.Contains(list.status().text, "it is in the hand-off lane") {
		t.Fatalf("the reopen note says %q", list.status().text)
	}

	// in progress → open: a second issue whose only task is cancelled.
	other := h.seedIssue(t, "Abandon me")
	workflow := "walk"
	if _, err := h.m.client.CreateTask(ctx, apiclient.CreateTaskRequest{
		ProjectID: h.projectID, Title: "abandoned", Workflow: &workflow, IssueID: &other.ID,
	}); err != nil {
		t.Fatalf("create the other task: %v", err)
	}
	waitLane(other.ID, "in_progress")
	h.p.until(10*time.Second, "the task admitted", func() bool {
		for _, row := range list.rows() {
			if !row.header && row.issue.ID == other.ID {
				return row.issue.Active
			}
		}
		return false
	})
	moveTo(other.ID, "open")
	h.p.until(10*time.Second, "the cancel confirmation", func() bool {
		return list.w.act != nil && list.w.act.stage == iaConfirm && list.w.act.action == issueActCancel
	})
	h.sendKey(keyPress("y"))
	waitLane(other.ID, "open")
	if !strings.Contains(list.status().text, "it is in the open lane") {
		t.Fatalf("the cancel note says %q", list.status().text)
	}
}
