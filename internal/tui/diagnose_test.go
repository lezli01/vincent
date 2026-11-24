package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
	"github.com/lezli01/vincent/internal/store"
)

func TestDiagnoseTitleRoundTrips(t *testing.T) {
	title := diagnoseTitle(3, "verify", "check_failed")
	if title != "why step 3 verify stopped · check_failed" {
		t.Fatalf("title = %q", title)
	}
	step, reason, ok := parseDiagnoseTitle(title)
	if !ok || step != 3 || reason != "check_failed" {
		t.Fatalf("parse = %d %q %v", step, reason, ok)
	}
	for _, not := range []string{"why it broke", "card task", "why step x stopped · check_failed"} {
		if _, _, ok := parseDiagnoseTitle(not); ok {
			t.Errorf("%q parsed as a diagnosis title", not)
		}
	}
}

// #1087: the question carries what the card shows, so the agent does not
// spend its first tool calls finding it.
func TestDiagnoseMessageNamesTheFailure(t *testing.T) {
	run := failedRun(5, 2, "check_failed")
	run.StepType, run.CheckExitCode = "agent", ptr(1)
	run.StatusMessage = ptr("running the suite")
	task := cardTask("check_failed", run)
	c := deriveFailureCard(task, laneBlame{}, false)
	ev := readEvidence(c, []apiclient.TranscriptRecord{
		commandStarted("check", "go test ./..."),
		{Type: "vincent.output", Phase: "check", Text: "--- FAIL: TestThing (0.01s)"},
		{Type: "vincent.output", Phase: "check", Stream: "stderr", Text: "exit status 1"},
	}, evidenceLines)
	earlier := apiclient.Chat{ID: 12}
	msg := diagnoseMessage(task, c, ev, earlier, []string{"The fixture file is missing."})
	for _, want := range []string{
		"Why did this task stop?",
		"Diagnose only",
		`Task #4 blocked at step 2 "verify", attempt 2: check failed (check_failed).`,
		"The check exited 1.",
		"    go test ./...",
		"    --- FAIL: TestThing (0.01s)",
		"    [stderr] exit status 1",
		`"running the suite"`,
		"An earlier chat (#12) concluded:",
		"    The fixture file is missing.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message misses %q:\n%s", want, msg)
		}
	}
	if strings.Contains(diagnoseMessage(task, c, ev, apiclient.Chat{}, nil), "earlier chat") {
		t.Error("a first diagnosis mentions an earlier chat")
	}
}

func TestOffersDiagnoseOnlyOnAStoppedTask(t *testing.T) {
	for state, want := range map[string]bool{
		stateBlocked: true, stateAborted: true, stateDone: false, stateAwaitingGate: false,
	} {
		target := taskActions{id: 4, state: state, actions: []string{apiclient.ActionChat}}
		if got := target.offersDiagnose(); got != want {
			t.Errorf("%s offers Q = %v, want %v", state, got, want)
		}
	}
	locked := taskActions{id: 4, state: stateBlocked, openChatID: 9}
	if !locked.offersDiagnose() {
		t.Error("a locked stopped task does not offer Q to reopen its chat")
	}
	bulk := taskActions{id: 4, state: stateBlocked, actions: []string{apiclient.ActionChat}, marked: []markedTask{{id: 4}, {id: 5}}}
	if bulk.offersDiagnose() {
		t.Error("Q offered under a bulk selection")
	}
}

// diagnosedView is a card over a task with one closed diagnosis chat on it,
// whose first answer has been read.
func diagnosedView(t *testing.T, chatCreated time.Time) *taskView {
	t.Helper()
	run := failedRun(5, 1, "check_failed")
	finished := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	run.FinishedAt = &finished
	v := cardView(t, cardTask("check_failed", run))
	chat := apiclient.Chat{
		ID: 12, Title: diagnoseTitle(2, "verify", "check_failed"), State: "closed",
		Agent: "claude", CreatedAt: chatCreated,
	}
	v.update(taskChatsMsg{taskID: 4, chats: []apiclient.Chat{
		{ID: 11, Title: "card task", State: "closed"},
		{ID: 13, Title: diagnoseTitle(2, "verify", "nonzero_exit"), State: "closed"},
		chat,
	}})
	if v.diagnosis.chatID != 12 {
		t.Fatalf("the card picked chat #%d, want the diagnosis of this step and reason (#12)", v.diagnosis.chatID)
	}
	v.update(taskDiagnosisMsg{taskID: 4, chat: chat, answer: []string{"The fixture file is missing.", "Restore it."}})
	return v
}

// #1087: once the chat is closed the card shows the first answer's opening
// with the chat it came from.
func TestFailureCardShowsTheDiagnosis(t *testing.T) {
	v := diagnosedView(t, time.Date(2026, 11, 20, 12, 5, 0, 0, time.UTC))
	got := ansi.Strip(v.render(100, 50))
	for _, want := range []string{"Diagnosis", "chat #12 · claude", "The fixture file is missing.", "Q      diagnose"} {
		if !strings.Contains(got, want) {
			t.Errorf("card misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Stopped here before") {
		t.Errorf("a diagnosis of this stop reads as an earlier one:\n%s", got)
	}
}

// #1087: a task that stops again at the same step for the same reason shows
// the earlier diagnosis before a new chat is started.
func TestFailureCardOffersTheEarlierDiagnosis(t *testing.T) {
	v := diagnosedView(t, time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC))
	got := ansi.Strip(v.render(100, 50))
	for _, want := range []string{"Stopped here before", "same step, same reason · chat #12 (claude) said:", "Restore it."} {
		if !strings.Contains(got, want) {
			t.Errorf("card misses %q:\n%s", want, got)
		}
	}
}

func TestFirstAnswerSkipsEmptyTurnsAndLines(t *testing.T) {
	got := firstAnswer([]apiclient.ChatTurn{
		{Seq: 2, ResultText: "second"},
		{Seq: 1, ResultText: "  \n"},
		{Seq: 0, ResultText: ""},
		{Seq: 3, ResultText: "third"},
	}, 2)
	if len(got) != 1 || got[0] != "second" {
		t.Fatalf("first answer = %q", got)
	}
}

// TestDiagnoseKeyOpensATitledChatLive is `Q` against the real handlers: on a
// blocked task with a worktree it opens a chat linked to the task, titled
// after the step and the reason it asks about.
func TestDiagnoseKeyOpensATitledChatLive(t *testing.T) {
	h := newActionLiveHarness(t)
	task := h.createParkedTask(t, "diagnosable")
	ctx := context.Background()
	wt := t.TempDir()
	if _, _, err := h.st.TransitionTask(ctx, task.ID, store.TaskQueued, store.TaskRunning,
		store.TaskChange{WorktreePath: &wt}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	path := filepath.Join(t.TempDir(), "0-1.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"vincent.output","phase":"check","stream":"stderr","text":"FAIL TestThing"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := h.st.CreateStepRun(ctx, &store.StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "implement", StepType: "agent", Attempt: 1,
		State: store.StepFailed, FailureReason: "check_failed", TranscriptPath: path,
		StartedAt: now, FinishedAt: &now,
	}); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	reason := "check_failed"
	if _, _, err := h.st.TransitionTask(ctx, task.ID, store.TaskRunning, store.TaskBlocked,
		store.TaskChange{BlockReason: &reason}); err != nil {
		t.Fatalf("block: %v", err)
	}

	got := h.overviewOf(t, task.ID, "FAIL TestThing")
	if !strings.Contains(got, "Q      diagnose") {
		t.Errorf("the card does not name Q:\n%s", got)
	}
	if !paletteOffersKey(h.m, opKey(keymap.Diagnose)) {
		t.Fatal("a blocked task does not offer Q in the palette")
	}
	h.press(t, opKey(keymap.Diagnose))
	// Q asks first (#1162): nothing is opened until y.
	if chats := h.linkedChats(t, task.ID); len(chats) != 0 {
		t.Fatalf("Q opened a chat before it was answered: %+v", chats)
	}
	h.press(t, "y")
	chat := h.m.views[viewChat].(*chatView)
	t.Cleanup(func() {
		if chat.streamStop != nil {
			chat.streamStop()
		}
	})
	h.p.until(30*time.Second, "Q to open the chat workspace", func() bool {
		return h.m.active == viewChat && chat.chat != nil &&
			chat.chat.LinkedTaskID != nil && *chat.chat.LinkedTaskID == task.ID
	})
	chats := h.linkedChats(t, task.ID)
	if len(chats) != 1 || chats[0].Title != "why step 1 implement stopped · check_failed" {
		t.Fatalf("the daemon holds %+v, want one chat titled after the stop", chats)
	}
}
