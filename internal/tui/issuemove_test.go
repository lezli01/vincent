package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

func init() {
	panelKeyProbes[ctxIssues][issueMoveKey] = func(t *testing.T) {
		v := issuesFixture()
		_, cmd := v.update(registryKey(t, issueMoveKey))
		if cmd == nil {
			t.Fatal("m did not read the selected issue")
		}
		if _, ok := cmd().(issueMoveTargetMsg); !ok {
			t.Fatal("m did not read the issue for its moves")
		}
	}
	panelKeyProbes[ctxIssue][issueMoveKey] = func(t *testing.T) {
		v := issueFixture()
		v.issue.Lane = "in_progress"
		v.issue.AvailableActions = []string{"close"}
		v.update(registryKey(t, issueMoveKey))
		if v.w.act == nil || v.w.act.stage != iaMove {
			t.Fatal("m did not open the lane picker")
		}
	}
}

// laneIssue is an open issue in lane, with close on offer as the daemon
// serves it for every open issue.
func laneIssue(lane string, marker string, active ...int64) apiclient.Issue {
	iss := apiclient.Issue{
		ID: 7, ProjectID: 1, State: "open", Lane: lane,
		AvailableActions: []string{issueActClose},
		Tasks:            apiclient.IssueTasks{ActiveIDs: active},
	}
	if lane == issueLaneDone {
		iss.State, iss.AvailableActions = "closed", []string{issueActReopen}
	}
	if marker != "" {
		iss.Handoff = &apiclient.IssueHandoff{State: marker}
	}
	return iss
}

// The move table (task 145 decision 2): every lane to every other, with
// what it runs or that it is refused.
func TestPlanLaneMove(t *testing.T) {
	handoffID := int64(3)
	claimed := laneIssue("hand_off", markerClaimed)
	claimed.Handoff.HandoffID = &handoffID
	for _, c := range []struct {
		name string
		iss  apiclient.Issue
		to   string
		want laneMoveKind
	}{
		{"open to in progress starts a task", laneIssue("open", ""), "in_progress", moveStartTask},
		{"open to hand-off has nothing finished", laneIssue("open", ""), "hand_off", moveRefused},
		{"open to done closes", laneIssue("open", ""), "done", moveClose},
		{"running to open cancels", laneIssue("in_progress", "", 4, 5), "open", moveCancel},
		{"idle to open has finished work", laneIssue("in_progress", markerIdle), "open", moveRefused},
		{"a chat's hold is not a task to cancel", laneIssue("in_progress", ""), "open", moveRefused},
		{"running to hand-off waits for the task", laneIssue("in_progress", "", 4), "hand_off", moveRefused},
		{"idle to hand-off marks ready", laneIssue("in_progress", markerIdle), "hand_off", moveReady},
		{"in progress to done closes", laneIssue("in_progress", "", 4), "done", moveClose},
		{"pending to in progress marks not ready", laneIssue("hand_off", markerPending), "in_progress", moveUnready},
		{"claimed to in progress returns", claimed, "in_progress", moveReturn},
		{"delivered to in progress is refused", laneIssue("hand_off", markerDelivered), "in_progress", moveRefused},
		{"hand-off to open has finished work", laneIssue("hand_off", markerPending), "open", moveRefused},
		{"hand-off to done closes", laneIssue("hand_off", markerPending), "done", moveClose},
		{"done to open reopens", laneIssue("done", ""), "open", moveReopen},
		{"done to in progress reopens", laneIssue("done", ""), "in_progress", moveReopen},
		{"done to hand-off reopens", laneIssue("done", ""), "hand_off", moveReopen},
		{"a lane to itself", laneIssue("open", ""), "open", moveRefused},
	} {
		t.Run(c.name, func(t *testing.T) {
			mv := planLaneMove(c.iss, c.to)
			if mv.kind != c.want {
				t.Fatalf("kind = %d (%q), want %d", mv.kind, mv.note, c.want)
			}
			if mv.note == "" {
				t.Fatal("the move says nothing about itself")
			}
		})
	}
	// Close and reopen come only from available_actions.
	noClose := laneIssue("open", "")
	noClose.AvailableActions = nil
	if planLaneMove(noClose, "done").kind != moveRefused {
		t.Fatal("close offered without the daemon offering it")
	}
}

// The picker lists every other lane, refused ones disabled with the reason,
// and starts on the first move that can be made.
func TestIssueMovePicker(t *testing.T) {
	a := newIssueMove(offlineClient(), laneIssue("in_progress", markerIdle))
	out, _ := a.lines(120)
	text := ansi.Strip(strings.Join(out, "\n"))
	for _, want := range []string{
		"move from in progress to",
		"open  it has finished work",
		"hand-off  marks it ready",
		"done  closes it",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("picker missing %q:\n%s", want, text)
		}
	}
	if opt, ok := a.pick.current(); !ok || opt.value != "hand_off" {
		t.Fatalf("cursor on %+v, want hand_off", opt)
	}
	// A refused lane says why and leaves the prompt open.
	a.pick.cursor = 0
	if _, done := a.updateKey(registryKey(t, "enter")); done || a.stage != iaMove {
		t.Fatal("a refused move closed the prompt")
	}
}

// Each runnable move lands in the prompt stage its action needs.
func TestIssueMoveDispatch(t *testing.T) {
	cases := []struct {
		iss    apiclient.Issue
		to     string
		stage  iaStage
		action string
	}{
		{laneIssue("open", ""), "done", iaReason, issueActClose},
		{laneIssue("in_progress", "", 4), "open", iaConfirm, issueActCancel},
		{laneIssue("in_progress", markerIdle), "hand_off", iaBusy, issueActReady},
		{laneIssue("hand_off", markerPending), "in_progress", iaBusy, issueActUnready},
		{laneIssue("done", ""), "open", iaBusy, issueActReopen},
	}
	for _, c := range cases {
		a := newIssueMove(offlineClient(), c.iss)
		cmd, done := a.move(c.to)
		if done || a.stage != c.stage || a.action != c.action {
			t.Fatalf("%s → %s: stage %d action %q done %v, want stage %d action %q",
				c.iss.Lane, c.to, a.stage, a.action, done, c.stage, c.action)
		}
		if c.stage == iaBusy && cmd == nil {
			t.Fatalf("%s → %s ran nothing", c.iss.Lane, c.to)
		}
	}
	// Open → in progress hands off to the new-task form and closes.
	a := newIssueMove(offlineClient(), laneIssue("open", ""))
	cmd, done := a.move("in_progress")
	if !done || cmd == nil {
		t.Fatal("open → in progress did not hand off to the form")
	}
	if msg, ok := cmd().(newTaskFromIssueMsg); !ok || msg.issueID != 7 {
		t.Fatalf("produced %#v, want the form seeded with #7", cmd())
	}
	// The cancel confirmation names the tasks.
	c := newIssueMove(offlineClient(), laneIssue("in_progress", "", 4, 5))
	c.move("open")
	if got := strings.Join(c.confirmText(), " "); !strings.Contains(got, "tasks #4, #5") {
		t.Fatalf("confirm = %q", got)
	}
}
