package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
)

// hostileIssueTitles are titles the daemon accepts — a local create trims
// only the ends, and a sync takes GitHub's or Jira's verbatim — that a
// screen must still fit to the terminal: the 1 KiB bound with no break
// opportunity, a long title that wraps, interior line breaks and tabs, wide
// runes, and terminal escapes.
var hostileIssueTitles = []string{
	strings.Repeat("Supercalifragilistic", 51),
	"Crash on start when " + strings.Repeat("the configuration file is very long ", 25),
	"Line one\nline two\r\nline three\twith a tab\vand a vertical tab",
	strings.Repeat("東京の非常に長い課題タイトル🚀🔥", 20),
	"Escape \x1b]0;pwned\x07 and \x1b[2J\x1b[H clear \x1b[31mred",
}

// laneTitleWalk is one manual gate under the default hand_off: auto, so an
// approved task puts its issue in hand-off.
var longTitleWorkflows = map[string]string{
	"walk.yaml": `name: walk
fields:
  - {name: issue, type: string}
steps:
  - {id: gate, type: manual, instructions: review}
`,
	"handoff.yaml": `name: handoff
role: handoff
steps:
  - {id: gate, type: manual, instructions: review}
`,
}

var fitIssueSizes = [][2]int{{80, 20}, {80, 24}, {120, 40}, {200, 60}}

// TestLongIssueTitlesFitEveryIssueScreen renders every screen an issue's
// title reaches, at several terminal sizes, and holds each frame to the
// terminal: exactly its height, no line wider, and no escape from a title
// reaching the terminal.
func TestLongIssueTitlesFitEveryIssueScreen(t *testing.T) {
	h := newNewTaskLiveHarnessWith(t, liveOptions{workflows: longTitleWorkflows})
	ctx := t.Context()
	list := issuesListView(t, h)
	detail := issueDetailView(t, h)

	ids := make([]int64, len(hostileIssueTitles))
	for i, title := range hostileIssueTitles {
		iss := h.seedIssue(t, title)
		ids[i] = iss.ID
	}
	// One issue in each lane: open (the rest), in progress, hand-off, done.
	workflow := "walk"
	newTask := func(issueID int64, title string) apiclient.TaskDetail {
		t.Helper()
		task, err := h.m.client.CreateTask(ctx, apiclient.CreateTaskRequest{
			ProjectID: h.projectID, Title: title, Workflow: &workflow, IssueID: &issueID,
		})
		if err != nil {
			t.Fatalf("create task: %v", err)
		}
		return task
	}
	running := newTask(ids[4], hostileIssueTitles[4])
	finished := newTask(ids[1], hostileIssueTitles[2])
	for _, id := range []int64{running.ID, finished.ID} {
		h.p.until(20*time.Second, "the gate", func() bool {
			got, err := h.st.GetTask(ctx, id)
			return err == nil && got.State == "awaiting_gate"
		})
	}
	if _, err := h.m.client.Approve(ctx, finished.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := h.st.TransitionIssue(ctx, ids[2], issuestate.Close, "completed", nil, issuestate.Human); err != nil {
		t.Fatalf("close: %v", err)
	}

	check := func(what string) {
		t.Helper()
		for _, sz := range fitIssueSizes {
			h.m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			frame := h.m.View().Content
			t.Run(what, func(t *testing.T) {
				assertFits(t, frame, sz[0], sz[1])
				for _, bad := range []string{"\x1b]0;pwned", "\x1b[2J", "\x1b[H", "\r", "\v", "\x07"} {
					if strings.Contains(frame, bad) {
						t.Errorf("at %dx%d a title's %q reached the terminal", sz[0], sz[1], bad)
					}
				}
			})
		}
		h.m.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	}

	h.send(selectViewMsg{id: viewIssues})
	h.p.until(20*time.Second, "every lane on the board", func() bool {
		lanes := map[string]bool{}
		for _, iss := range list.issues {
			lanes[iss.Lane] = true
		}
		return list.loaded && lanes["in_progress"] && lanes["hand_off"]
	})
	if !list.showDone {
		h.sendKey(keyPress("s"))
	}
	h.p.until(20*time.Second, "the done lane", func() bool {
		for _, iss := range list.issues {
			if iss.Lane == issueLaneDone {
				return true
			}
		}
		return false
	})
	for _, id := range ids {
		list.selectIssue(id)
		check("board")
	}

	// The prompts over the board: the move picker, the close reason, the
	// duplicate picker that lists every title, and the delete confirmation.
	list.selectIssue(ids[4])
	h.sendKey(keyPress(issueMoveKey))
	h.p.until(10*time.Second, "the lane picker", func() bool { return list.w.act != nil && list.w.act.stage == iaMove })
	check("move picker")
	h.sendKey(keyPress("esc"))
	h.sendKey(keyPress("X"))
	h.p.until(10*time.Second, "the reason prompt", func() bool { return list.w.act != nil && list.w.act.stage == iaReason })
	h.sendKey(keyPress("down"))
	h.sendKey(keyPress("down"))
	h.sendKey(keyPress("enter"))
	h.p.until(10*time.Second, "the duplicate targets", func() bool { return list.w.act != nil && list.w.act.pick != nil })
	check("duplicate picker")
	h.sendKey(keyPress("esc"))
	h.sendKey(keyPress("D"))
	check("delete confirmation")
	h.sendKey(keyPress("n"))

	// The handoff form over the hand-off card.
	list.selectIssue(ids[1])
	h.sendKey(keyPress("I"))
	h.p.until(10*time.Second, "the handoff form", func() bool { return list.handoff != nil })
	check("handoff form")
	h.sendKey(keyPress("esc"))
	h.p.until(10*time.Second, "the handoff form closed", func() bool { return list.handoff == nil })

	// The detail, both tabs, for every title.
	for _, id := range ids {
		iss, err := h.st.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		h.send(openIssueMsg{id: id, projectID: iss.ProjectID})
		h.p.until(10*time.Second, "the detail", func() bool {
			return h.m.active == viewIssue && detail.loaded && detail.issue.ID == id
		})
		detail.setTab(issueTabIssue)
		check("detail")
		detail.setTab(issueTabWork)
		check("detail work tab")
		detail.setTab(issueTabIssue)
	}

	// The new-task form seeded from an issue, and the task board and
	// workspace of a task titled like one.
	h.send(selectViewMsg{id: viewIssues})
	h.p.until(10*time.Second, "the board again", func() bool { return h.m.active == viewIssues && list.loaded })
	list.selectIssue(ids[4])
	h.sendKey(keyPress("a"))
	n := h.form(t)
	h.p.until(10*time.Second, "the seeded form", func() bool {
		return h.m.active == viewNewTask && n.loaded && n.issue != nil && n.issue.ID == ids[4]
	})
	h.p.push(n.applyPick(ntWorkflow, "walk", false))
	h.p.until(10*time.Second, "walk's prefill", func() bool {
		_, ok := n.fieldValue("issue")
		return ok
	})
	check("new-task form")
	// The seeded form is a draft: esc asks, and y discards it (#1051).
	h.sendKey(keyPress("esc"))
	h.sendKey(keyPress("y"))

	h.send(selectViewMsg{id: viewHome})
	h.p.until(10*time.Second, "the task board", func() bool { return h.m.active == viewHome })
	check("task board")
	h.send(openTaskMsg{id: running.ID})
	h.p.until(10*time.Second, "the task workspace", func() bool { return h.m.active == viewTask })
	check("task workspace")
}

func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"plain title":                       "plain title",
		"  keeps  inner  spaces  ":          "  keeps  inner  spaces  ",
		"Line one\nline two\r\nthree\tfour": "Line one line two three four",
		"a\vb\fc":                           "a b c",
		"Escape \x1b]0;pwned\x07 and \x1b[2J\x1b[H clear \x1b[31mred": "Escape and clear red",
		"bell\x07 and del\x7f": "bell and del",
		"東京🚀\n🔥":               "東京🚀 🔥",
	} {
		if got := oneLine(in); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
	}
}
