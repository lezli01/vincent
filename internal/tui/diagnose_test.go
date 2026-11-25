package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
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
	v.update(taskDiagnosisMsg{taskID: 4, key: 12, chat: chat, answer: []string{"The fixture file is missing.", "Restore it."}})
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

// #1153: a newer diagnosis chat with no answer — its turn refused, failed
// or cancelled — does not hide an older one that has one. The card keys its
// read on the newest candidate and shows the chat the answer came from.
func TestFailureCardKeepsAnOlderAnsweredDiagnosis(t *testing.T) {
	run := failedRun(5, 1, "check_failed")
	finished := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	run.FinishedAt = &finished
	v := cardView(t, cardTask("check_failed", run))
	answered := apiclient.Chat{
		ID: 12, Title: diagnoseTitle(2, "verify", "check_failed"), State: "closed",
		Agent: "claude", CreatedAt: finished.Add(time.Minute),
	}
	empty := apiclient.Chat{
		ID: 14, Title: diagnoseTitle(2, "verify", "check_failed"), State: "closed",
		Agent: "codex", CreatedAt: finished.Add(2 * time.Minute),
	}
	v.update(taskChatsMsg{taskID: 4, chats: []apiclient.Chat{answered, empty}})
	if v.diagnosis.chatID != 14 {
		t.Fatalf("the card keyed its read on chat #%d, want the newest candidate (#14)", v.diagnosis.chatID)
	}
	v.update(taskDiagnosisMsg{taskID: 4, key: 14, chat: answered, answer: []string{"The fixture file is missing."}})
	got := ansi.Strip(v.render(100, 50))
	for _, want := range []string{"Diagnosis", "chat #12 · claude", "The fixture file is missing."} {
		if !strings.Contains(got, want) {
			t.Errorf("card misses %q:\n%s", want, got)
		}
	}
}

// #1153: the walk behind the card and behind `Q`'s hand-over reads the
// candidates newest first and stops at the first with an answer.
func TestFindDiagnosisSkipsAnswerlessChats(t *testing.T) {
	cands := []apiclient.Chat{{ID: 14}, {ID: 13}, {ID: 12}, {ID: 11}}
	var read []int64
	ch, answer, err := findDiagnosis(cands, func(id int64) ([]apiclient.ChatTurn, error) {
		read = append(read, id)
		switch id {
		case 14:
			return nil, nil
		case 13:
			return []apiclient.ChatTurn{{Seq: 1, ResultText: "  "}}, nil
		}
		return []apiclient.ChatTurn{{Seq: 1, ResultText: fmt.Sprintf("answer of #%d", id)}}, nil
	})
	if err != nil || ch.ID != 12 || len(answer) != 1 || answer[0] != "answer of #12" {
		t.Fatalf("findDiagnosis = #%d %q %v, want #12's answer", ch.ID, answer, err)
	}
	if fmt.Sprint(read) != "[14 13 12]" {
		t.Errorf("read %v, want newest first and no further than the answer", read)
	}
	if _, _, err := findDiagnosis(cands[:1], func(int64) ([]apiclient.ChatTurn, error) {
		return nil, errors.New("boom")
	}); err == nil {
		t.Error("a failed read with nothing found reports no error")
	}
}

func TestDiagnosisChatsNewestFirst(t *testing.T) {
	c := failureCard{reason: "check_failed", stepNum: 2}
	title := diagnoseTitle(2, "verify", "check_failed")
	got := diagnosisChats([]apiclient.Chat{
		{ID: 3, Title: title, State: "closed"},
		{ID: 9, Title: title, State: "running"},
		{ID: 7, Title: title, State: "closed"},
		{ID: 8, Title: diagnoseTitle(2, "verify", "nonzero_exit"), State: "closed"},
	}, c)
	if len(got) != 2 || got[0].ID != 7 || got[1].ID != 3 {
		t.Fatalf("diagnosisChats = %+v, want #7 then #3", got)
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

// blockedForDiagnosis is a task blocked on check_failed at step 1, with a
// worktree and a transcript, so `Q` has something to ask about.
func (h *actionLiveHarness) blockedForDiagnosis(t *testing.T) *store.Task {
	t.Helper()
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
	return task
}

// TestDiagnoseKeyOpensATitledChatLive is `Q` against the real handlers: on a
// blocked task with a worktree it opens a chat linked to the task, titled
// after the step and the reason it asks about.
func TestDiagnoseKeyOpensATitledChatLive(t *testing.T) {
	h := newActionLiveHarness(t, withChatRunner)
	task := h.blockedForDiagnosis(t)

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

// TestDiagnoseRefusedFirstTurnUnlocksLive is #1153: when the daemon creates
// the chat but refuses its first turn because `max_parallel_chats` chats are
// already running, `Q` closes the empty chat, so the task is not left locked
// by a chat that asked nothing, and the action bar says why.
func TestDiagnoseRefusedFirstTurnUnlocksLive(t *testing.T) {
	h := newActionLiveHarness(t, withChatRunner)
	task := h.blockedForDiagnosis(t)
	ctx := context.Background()
	// Fill the cap (config.Default's three) with chats holding a turn.
	for i := range config.Default().MaxParallelChats {
		c := &store.Chat{
			ProjectID: h.projectID, Title: fmt.Sprintf("busy %d", i), State: chatstate.Idle, Agent: "claude",
			PermissionMode: "full_auto", Branch: fmt.Sprintf("vincent/busy-%d", i), BaseBranch: "main",
		}
		if err := h.st.CreateChat(ctx, c); err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.CreateChatTurn(ctx, c.ID, "busy"); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := h.st.CountChatsHoldingProcess(ctx); err != nil || n != config.Default().MaxParallelChats {
		t.Fatalf("%d chats hold a process (%v), want the cap", n, err)
	}
	h.overviewOf(t, task.ID, "FAIL TestThing")
	t.Cleanup(func() {
		if v := h.m.views[viewChat].(*chatView); v.streamStop != nil {
			v.streamStop()
		}
	})
	h.press(t, opKey(keymap.Diagnose))
	h.press(t, "y")
	h.p.until(30*time.Second, "the refusal on the action bar", func() bool {
		return strings.Contains(ansi.Strip(content(h.m)), "chat_cap_reached")
	})
	if got := detailOf(h.m).actions.status; !strings.Contains(got, "closed so task #") ||
		!strings.Contains(got, "chat_cap_reached") {
		t.Errorf("the bar says %q, want the chat closed and the daemon's reason", got)
	}
	if h.m.active == viewChat {
		t.Error("a refused diagnosis opened the chat workspace")
	}
	chats := h.linkedChats(t, task.ID)
	if len(chats) != 1 || chats[0].State != chatstate.Closed {
		t.Fatalf("the daemon holds %+v, want the one chat closed", chats)
	}
	if open, err := h.st.OpenLinkedChatID(context.Background(), task.ID); err != nil || open != 0 {
		t.Errorf("task still locked by chat #%d (%v)", open, err)
	}
}

// #1153: `Q` has one name. The card, the question and the keymap call it
// diagnose, so the footer and the palette do too.
func TestDiagnoseHasOneName(t *testing.T) {
	target := taskActions{id: 9, state: stateBlocked, actions: stateActions(stateBlocked)}
	line := ansi.Strip(renderFooter(400, nil, &actionBar{}, target, 0, false))
	if !strings.Contains(line, "Q diagnose") || strings.Contains(line, "ask why") {
		t.Errorf("the footer names Q %q", line)
	}
	for _, b := range bindings {
		if b.op == keymap.Diagnose && !strings.HasPrefix(b.label, "diagnose") {
			t.Errorf("the palette names Q %q", b.label)
		}
	}
}
