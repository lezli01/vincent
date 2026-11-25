package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Issue #1150: the daemon's snake_case vocabulary never reaches the screen as
// prose — a reason reads as its catalogue title, a state as its words (§15,
// task 129.7).

func TestLaneRowNamesItsBlockReasonInWords(t *testing.T) {
	lines := timelineText(fanOutDetail(t))
	web := lineIndex(lines, "#52")
	if web < 0 {
		t.Fatalf("lane row missing:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[web], "check failed") || strings.Contains(lines[web], "check_failed") {
		t.Fatalf("lane row %q should name its block reason by title, not code", lines[web])
	}
}

func TestQueuedHoldSentenceNamesTheReasonInWords(t *testing.T) {
	reason := "usage_limit"
	got := overviewSentence(apiclient.TaskDetail{Task: apiclient.Task{State: stateQueued, QueuedReason: &reason}}, 0)
	if strings.Contains(got, "usage_limit") || !strings.Contains(got, "usage limit reached") {
		t.Fatalf("hold sentence = %q, want the reason's title", got)
	}
}

func TestChatPreviewFailReasonInWords(t *testing.T) {
	c := testChat(1, "idle", "x")
	p := &chatPreview{chat: c, turns: []apiclient.ChatTurn{{
		Seq: 1, Prompt: "hi", FailReason: "usage_limit", ErrorMessage: "quota spent",
		StartedAt: time.Date(2026, 8, 31, 11, 0, 0, 0, time.UTC),
	}}}
	got := ansi.Strip(strings.Join(chatPreviewLines(c, p, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC), 80), "\n"))
	if !strings.Contains(got, "usage limit reached (usage_limit): quota spent") {
		t.Fatalf("preview fail line should read like the chat's own:\n%s", got)
	}
}

func TestPaletteItemsNameStatesInWords(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	s := m.views[viewHome].(*shell)
	s.board.project = projectSel{id: 7, name: "repo"}
	s.board.tasks = []apiclient.Task{{ID: 3, ProjectID: 7, Title: "fan", State: stateAwaitingChildren}}
	m.views[viewChats] = chatsFixture()
	var labels []string
	for _, e := range m.paletteItems() {
		labels = append(labels, e.label)
	}
	all := strings.Join(labels, "\n")
	for _, raw := range []string{"awaiting_children", "awaiting_input"} {
		if strings.Contains(all, raw) {
			t.Fatalf("palette rows carry the raw state %q:\n%s", raw, all)
		}
	}
	if !strings.Contains(all, "waiting on lanes") || !strings.Contains(all, "waiting on you") {
		t.Fatalf("palette rows lack the state words:\n%s", all)
	}
}

func TestUnlinkWordingDoesNotNameTheReconciler(t *testing.T) {
	for _, b := range bindings {
		if strings.Contains(b.label, "reconciler") {
			t.Errorf("binding label %q names an internal component", b.label)
		}
	}
	v := pullRequestsFixture(testPull(11, "ship it", claimedBy(7, "human")))
	v.askUnlink()
	if v.confirm == nil {
		t.Fatal("u on a claimed row asked nothing")
	}
	if strings.Contains(v.confirm.text, "reconciler") {
		t.Errorf("unlink question %q names an internal component", v.confirm.text)
	}
}

func TestPullCreatePickerNamesTaskStatesInWords(t *testing.T) {
	v := pullRequestsFixture()
	v.tasks = []apiclient.Task{{ID: 9, ProjectID: v.project.id, Title: "x", State: stateAwaitingGate, BranchName: "vincent/9-x"}}
	opts := v.createOptions()
	if len(opts) != 1 || !strings.HasPrefix(opts[0].note, "awaiting approval") {
		t.Fatalf("picker options = %+v, want the state in words", opts)
	}
}

func TestCloseOnATerminalChatNamesItsStateInWords(t *testing.T) {
	v := chatViewFixture()
	task := int64(3)
	v.chat.LinkedTaskID, v.chat.State = &task, "handed_off"
	v.askClose()
	if !strings.Contains(v.note, "handed off") || strings.Contains(v.note, "handed_off") {
		t.Fatalf("note = %q, want the chat state in words", v.note)
	}
}
