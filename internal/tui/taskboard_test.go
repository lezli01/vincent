package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The task board and the wide task workspace (task 150.6). Pure model tests,
// one property each (task 129 decision 7).

var taskBoardNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func taskBoardFixture() *board {
	release := &apiclient.TaskIssue{ID: 14, Title: "Ship signed releases", State: "open"}
	limiter := &apiclient.TaskIssue{ID: 9, Title: "Harden the rate limiter", State: "open"}
	reason := "nonzero_exit"
	b := newBoard()
	b.cards = true
	b.now = func() time.Time { return taskBoardNow }
	b.project = projectSel{id: 1, name: "api"}
	row := func(id int64, state, title string, issue *apiclient.TaskIssue) apiclient.Task {
		return apiclient.Task{
			ID: id, ProjectID: 1, State: state, Title: title, Workflow: "verify-build", Issue: issue,
			CurrentStep: 1, StepTotal: 3, StepName: "verify", UpdatedAt: taskBoardNow.Add(-4 * time.Minute),
		}
	}
	blocked := row(6, stateBlocked, "verify the signed checksums", release)
	blocked.BlockReason = &reason
	blocked.AvailableActions = []string{apiclient.ActionRetry, apiclient.ActionCancel}
	b.tasks = []apiclient.Task{
		blocked,
		row(13, stateAwaitingGate, "sign the darwin archives", release),
		row(15, stateAwaitingInput, "choose a migration strategy", nil),
		row(21, stateRunning, "soak the limiter", limiter),
		row(24, stateQueued, "rotate the signing keys", release),
		row(25, statePaused, "bump the base image", nil),
		row(19, stateDone, "pin the toolchain", release),
		row(17, stateAborted, "publish the nightly", release),
	}
	b.loaded = true
	return b
}

func TestTaskColumnsMatchWhatATaskNeeds(t *testing.T) {
	for state, want := range map[string]int{
		stateBlocked: taskColumnNeedsYou, stateAwaitingInput: taskColumnNeedsYou, stateAwaitingGate: taskColumnNeedsYou,
		stateRunning: taskColumnRunning, stateAwaitingChildren: taskColumnRunning,
		stateQueued: taskColumnQueued, statePaused: taskColumnQueued,
		stateDone: taskColumnFinished, stateAborted: taskColumnFinished,
	} {
		if got := taskColumnOf(state); got != want {
			t.Errorf("%s is in column %d, want %d", state, got, want)
		}
	}
}

// Every column groups by issue, and the tasks with no issue come last.
func TestTaskBoardGroupsEachColumnByIssue(t *testing.T) {
	groups := taskBoardFixture().cardGroups()
	needs := groups[taskColumnNeedsYou]
	if len(needs) != 2 || needs[0].key != "#14" || len(needs[0].tasks) != 2 || needs[1].key != groupNoIssue {
		t.Fatalf("Needs you = %+v, want #14's two tasks, then no issue", needs)
	}
	out := ansi.Strip(taskBoardFixture().renderCards(200, 50))
	for _, want := range []string{
		"! Needs you", "▶ Running", "◷ Queued", "✓ Finished", "▦ #14 Ship signed releases", "○ No issue",
		"#6", "⟡ verify-build", "◷ 4m", "verify the signed checksums", "2/3 verify",
		"! needs your approval", "! has a question", "◷ waiting for a slot", "⏸ paused",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the board is missing %q:\n%s", want, out)
		}
	}
}

// Finished work starts folded, and says what the fold holds.
func TestTaskBoardFinishedStartsFolded(t *testing.T) {
	out := ansi.Strip(taskBoardFixture().renderCards(200, 50))
	if strings.Contains(out, "pin the toolchain") {
		t.Errorf("a finished task is on show:\n%s", out)
	}
	if !strings.Contains(out, "▸ ▦ #14") || !strings.Contains(out, "1 done") {
		t.Errorf("the folded group does not say what it holds:\n%s", out)
	}
}

func TestTaskBoardFitsTheScreen(t *testing.T) {
	for _, size := range [][2]int{{220, 50}, {170, 40}, {132, 30}, {100, 24}, {80, 24}, {60, 15}} {
		b := taskBoardFixture()
		lines := strings.Split(b.renderCards(size[0], size[1]), "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: drew %d lines", size[0], size[1], len(lines))
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got != size[0] {
				t.Errorf("%dx%d: line %d is %d wide", size[0], size[1], i, got)
			}
		}
	}
}

// ↑ and ↓ stay in the column; ← and → cross it at the same height.
func TestTaskBoardKeysMoveByCardAndColumn(t *testing.T) {
	b := taskBoardFixture()
	selected := func() int64 {
		id, _ := b.selected()
		return id
	}
	if got := selected(); got != 6 {
		t.Fatalf("a fresh board selects #%d, want #6", got)
	}
	b.updateKey(keyPress("down"))
	if got := selected(); got != 13 {
		t.Fatalf("down went to #%d, want #13", got)
	}
	b.updateKey(keyPress("right"))
	if got := selected(); got != 21 {
		t.Fatalf("right went to #%d, want #21, the column's last card", got)
	}
	b.updateKey(keyPress("left"))
	b.updateKey(keyPress("end"))
	if got := selected(); got != 15 {
		t.Fatalf("end went to #%d, want #15", got)
	}
	b.updateKey(keyPress("home"))
	if got := selected(); got != 6 {
		t.Fatalf("home went to #%d, want #6", got)
	}
}

// A folded group is somewhere to rest, but not a task: no action reaches a task
// nobody can see.
func TestTaskBoardFoldKey(t *testing.T) {
	b := taskBoardFixture()
	b.updateKey(keyPress("f"))
	if !b.cardFolded(taskColumnNeedsYou, "#14") {
		t.Fatal("f did not fold the group")
	}
	if id, ok := b.selected(); ok {
		t.Fatalf("a folded group selects task #%d", id)
	}
	if out := ansi.Strip(b.renderCards(200, 50)); strings.Contains(out, "verify the signed checksums") && !strings.Contains(out, "#6") {
		t.Errorf("the fold did not hide the group's cards:\n%s", out)
	}
	b.updateKey(keyPress("f"))
	if b.cardFolded(taskColumnNeedsYou, "#14") {
		t.Fatal("f did not open the group again")
	}
	b.updateKey(keyPress("O"))
	if b.cardFolded(taskColumnFinished, "#14") {
		t.Fatal("O left Finished folded")
	}
	b.updateKey(keyPress("C"))
	if !b.cardFolded(taskColumnRunning, "#9") {
		t.Fatal("C left Running open")
	}
}

// Work that starts waiting on a person is never hidden behind a fold.
func TestTaskBoardFoldOpensForAttention(t *testing.T) {
	b := taskBoardFixture()
	b.setCardFold(taskColumnNeedsYou, "#14", true)
	if !b.expandFor(6) || b.cardFolded(taskColumnNeedsYou, "#14") {
		t.Fatal("the fold hiding a task that needs someone stayed shut")
	}
}

// The selected task's issue is lit in every column it has work in, and a click
// selects the card under the pointer.
func TestTaskBoardClickSelectsACard(t *testing.T) {
	b := taskBoardFixture()
	b.renderCards(200, 50)
	var target taskCardHit
	for _, h := range b.cardHits {
		if h.stop.id == 21 {
			target = h
		}
	}
	if target.stop.id == 0 {
		t.Fatalf("task #21 was not drawn: %+v", b.cardHits)
	}
	b.cardClick(target.x0, target.y0)
	if id, _ := b.selected(); id != 21 {
		t.Fatalf("the click selected #%d, want #21", id)
	}
}

func TestTaskPreviewShowsTheTask(t *testing.T) {
	b := taskBoardFixture()
	b.renderCards(200, 50)
	updated := taskBoardNow.Add(-4 * time.Minute)
	b.applyPreview(taskPreviewMsg{key: taskPreviewKey{6, updated.UnixNano()}, updated: updated, steps: []apiclient.StepRun{
		{ID: 1, StepIndex: 0, StepName: "fetch", State: "succeeded"},
		{ID: 2, StepIndex: 1, StepName: "verify", State: "failed", Attempt: 1},
		{ID: 3, StepIndex: 1, StepName: "verify", State: "failed", Attempt: 2},
	}})
	out := ansi.Strip(strings.Join(b.taskPreviewPane(50, 40), "\n"))
	for _, want := range []string{
		"#6", "verify the signed checksums", " blocked ", "▤ Steps", "✓ 1 fetch", "✗ 2 verify  attempt 2",
		"▦ Issue #14", "! 2 need you", "◷ 1 queued", "✓ 2 finished", "⚡ What you can do", "r retry", "c cancel",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the preview is missing %q:\n%s", want, out)
		}
	}
}

// Holding an arrow key reads only the task the selection rests on.
func TestTaskPreviewReadsOnlyWhereTheSelectionRests(t *testing.T) {
	b := taskBoardFixture()
	b.client = offlineClient()
	b.renderCards(200, 50)
	first := taskPreviewKey{6, taskBoardNow.Add(-4 * time.Minute).UnixNano()}
	if b.previewCmd() == nil {
		t.Fatal("selecting a task armed no read")
	}
	if b.previewCmd() != nil {
		t.Fatal("a read armed twice for one task")
	}
	b.moveCard(1)
	if b.previewDue(first) != nil {
		t.Fatal("a wait for a task since left read it")
	}
}

func TestTaskPreviewNeedsAWideTerminal(t *testing.T) {
	b := taskBoardFixture()
	if out := ansi.Strip(b.renderCards(taskPreviewMinW-1, 40)); strings.Contains(out, "▤ Steps") {
		t.Errorf("a narrower terminal drew the preview:\n%s", out)
	}
	if out := ansi.Strip(b.renderCards(taskPreviewMinW, 40)); !strings.Contains(out, "▤ Steps") {
		t.Errorf("a wide terminal drew no preview:\n%s", out)
	}
}

// taskWorkspaceFixture is a blocked task in a terminal of the given width.
func taskWorkspaceFixture(t *testing.T, width int) *taskView {
	t.Helper()
	reason, detail := "nonzero_exit", "The checksum did not match."
	d := newDetail(testCtx(t), newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	d.taskID, d.loaded = 6, true
	d.task = apiclient.TaskDetail{BaseBranch: "main", Task: apiclient.Task{
		ID: 6, Title: "verify the signed checksums", State: stateBlocked, Workflow: "publish-check",
		BranchName: "feat/6-verify", CurrentStep: 2, StepTotal: 3, BlockReason: &reason, BlockDetail: &detail,
		AvailableActions: []string{apiclient.ActionRetry, apiclient.ActionRepair, apiclient.ActionCancel},
		Issue:            &apiclient.TaskIssue{ID: 14, Title: "Ship signed releases"},
	}}
	v := newTaskView(d)
	v.update(tea.WindowSizeMsg{Width: width, Height: 40})
	v.tab = taskTabOutput
	return v
}

// The steps have a rail of their own on a wide terminal and stay a tab on a
// narrow one; neither strip carries Task Details unless the palette opened it
// (task 168 decisions 3 and 14).
func TestTaskWorkspaceTabsFollowTheWidth(t *testing.T) {
	wide := taskWorkspaceFixture(t, taskRailMinW)
	want := []taskViewTab{taskTabOverview, taskTabOutput, taskTabDiff, taskTabStepDetails, taskTabWorkflow}
	if got := wide.tabs(); !slices.Equal(got, want) {
		t.Errorf("the wide strip is %v, want %v", got, want)
	}
	narrow := taskWorkspaceFixture(t, taskRailMinW-1)
	want = []taskViewTab{taskTabOverview, taskTabSteps, taskTabOutput, taskTabDiff, taskTabStepDetails, taskTabWorkflow}
	if got := narrow.tabs(); narrow.frameless() || !slices.Equal(got, want) {
		t.Errorf("the narrow strip is %v, want %v", got, want)
	}
	narrow.tab = taskTabDetails
	if got := narrow.tabs(); got[len(got)-1] != taskTabDetails {
		t.Errorf("an open Task Details is not on the strip: %v", got)
	}
}

func TestTaskWorkspaceWideLayout(t *testing.T) {
	v := taskWorkspaceFixture(t, 180)
	lines := strings.Split(v.render(180, 40), "\n")
	if len(lines) != 40 {
		t.Fatalf("drew %d lines, want 40", len(lines))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != 180 {
			t.Errorf("line %d is %d wide", i, got)
		}
	}
	out := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{
		"#6 verify the signed checksums", " blocked ", "step 3 of 3", "Steps",
		"publish-check", "feat/6-verify", "issue #14 Ship signed releases", "i open", "The checksum did not match.", "r retry", "R repair",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the workspace is missing %q:\n%s", want, out)
		}
	}
}

// A failure is pinned where it can be acted on, but the Overview tells it at
// length itself.
func TestTaskFailureCardIsNotRepeatedOnTheOverview(t *testing.T) {
	v := taskWorkspaceFixture(t, 180)
	if len(v.taskFailureCard(100)) == 0 {
		t.Fatal("a blocked task has no failure card on Output")
	}
	v.tab = taskTabOverview
	if card := v.taskFailureCard(100); card != nil {
		t.Errorf("the card is repeated on the Overview: %v", card)
	}
}

// #1058: approving one of three gates leaves the cursor in Needs you, on the
// card that took its place, not following the task into Running.
func TestCardCursorStaysInItsColumnWhenACardMoves(t *testing.T) {
	b := taskBoardFixture()
	b.moveColumn(-4) // to Needs you
	for {
		id, _ := b.cardSelected()
		if id == 13 {
			break
		}
		before := id
		b.moveCard(1)
		if after, _ := b.cardSelected(); after == before {
			t.Fatal("no card 13 in Needs you")
		}
	}
	for i, tk := range b.tasks {
		if tk.ID == 13 {
			b.tasks[i].State = stateRunning
		}
	}
	id, ok := b.cardSelected()
	if !ok || id == 13 {
		t.Fatalf("the cursor followed the approved task (on %d)", id)
	}
	if tk, _ := b.taskByID(id); taskColumnOfTask(tk) != taskColumnNeedsYou {
		t.Fatalf("the cursor left Needs you for #%d (%s)", id, tk.State)
	}
}

// #1058: a card that vanishes leaves the cursor on the nearest card of the
// same column, not on the board's first card.
func TestCardCursorStaysNearAVanishedCard(t *testing.T) {
	b := taskBoardFixture()
	b.moveColumn(1) // into Running
	b.moveColumn(1) // into Queued
	id, _ := b.cardSelected()
	col := b.cardCol
	b.tasks = slices.DeleteFunc(b.tasks, func(tk apiclient.Task) bool { return tk.ID == id })
	next, ok := b.cardSelected()
	if !ok || next == id {
		t.Fatalf("the cursor is on %d after #%d vanished", next, id)
	}
	if tk, _ := b.taskByID(next); taskColumnOfTask(tk) != col {
		t.Fatalf("the cursor jumped to #%d in another column", next)
	}
	// An explicit open still goes where the task is.
	b.follow(21) // what openNow, selectTaskMsg and a `!` jump do
	if got, _ := b.cardSelected(); got != 21 {
		t.Fatalf("an explicit selection of #21 landed on #%d", got)
	}
}

// #1066: a folded group says how many of its tasks are selected, and a
// marked lane box carries its tick.
func TestMarksShowBehindFoldsAndOnLanes(t *testing.T) {
	b := taskBoardFixture()
	b.marks = b.marks.add(19)
	got := ansi.Strip(strings.Join(b.cardColumns(200, 40, 0), "\n"))
	if !strings.Contains(got, "✔ 1 selected") {
		t.Errorf("the folded Finished group hides its selected task:\n%s", got)
	}
	lane := apiclient.Task{ID: 42, State: stateRunning, LaneID: ptr("api")}
	if box := ansi.Strip(strings.Join(b.laneCard(lane, false, 40), "\n")); strings.Contains(box, "✔") {
		t.Fatal("an unmarked lane has a tick")
	}
	b.marks = b.marks.add(42)
	if box := ansi.Strip(strings.Join(b.laneCard(lane, false, 40), "\n")); !strings.Contains(box, "✔") {
		t.Errorf("a marked lane has no tick:\n%s", box)
	}
}

// placeCursorOn puts the card cursor on want and
// rests it there, so the test starts from a placed cursor the way a user's
// board does.
func placeCursorOn(t *testing.T, b *board, want int64) {
	t.Helper()
	b.follow(want)
	if got, _ := b.cardSelected(); got != want {
		t.Fatalf("could not place the cursor on #%d (on #%d)", want, got)
	}
	b.moveCard(0) // settle: the cursor is now placed, not following
}

// TestExplicitOpensLandOnTheirTask is review finding 2: openNow (a created
// task, the overview) and the root's selectTaskMsg (the palette, a `!` that
// crossed projects) are explicit selections, and the board cursor goes to the
// task even when it sits in another column than the cursor did.
func TestExplicitOpensLandOnTheirTask(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(s *shell, id int64)
	}{
		{"openNow", func(s *shell, id int64) { s.openNow(id) }},
		{"selectTaskMsg", func(s *shell, id int64) { s.routeBoardOnly(selectTaskMsg{id: id}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newShell(testCtx(t), newLevelHolder(), newRawHolder(), newHyperlinkHolder())
			s.board = taskBoardFixture()
			s.board.actions = s.bar
			placeCursorOn(t, s.board, 24) // Queued
			tc.open(s, 6)                 // Needs you
			if got, _ := s.board.cardSelected(); got != 6 {
				t.Fatalf("the cursor is on #%d, want #6", got)
			}
		})
	}
}

// TestACreatedTaskIsFollowedOnceListed: the open arrives before the board
// lists the task, and the refresh that lists it puts the cursor on it.
func TestACreatedTaskIsFollowedOnceListed(t *testing.T) {
	s := newShell(testCtx(t), newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	s.board = taskBoardFixture()
	s.board.actions = s.bar
	placeCursorOn(t, s.board, 6)
	s.openNow(99)
	if _, ok := s.board.cardSelected(); !ok {
		t.Fatal("no cursor while the new task is not listed yet")
	}
	s.board.tasks = append(s.board.tasks, apiclient.Task{ID: 99, ProjectID: 1, State: stateQueued, Title: "new"})
	if got, _ := s.board.cardSelected(); got != 99 {
		t.Fatalf("the cursor is on #%d once #99 is listed", got)
	}
}

// TestACardMovingIntoAFoldLeavesTheCursor: a card that moves on its own into
// a folded group of another column leaves the cursor in its column, as a
// move onto a card does, rather than on the fold header.
func TestACardMovingIntoAFoldLeavesTheCursor(t *testing.T) {
	b := taskBoardFixture()
	b.tasks = append(b.tasks, apiclient.Task{ID: 22, ProjectID: 1, State: stateRunning, Title: "second"})
	placeCursorOn(t, b, 21) // Running
	for i, tk := range b.tasks {
		if tk.ID == 21 {
			b.tasks[i].State = stateDone // into Finished, folded by default
		}
	}
	s, ok := b.cardStop()
	if !ok || s.header {
		t.Fatalf("the cursor followed #21 onto a fold header (%+v)", s)
	}
}

// #1145: on a narrow column the folded group's selected count stays whole;
// the summary in front of it gives way first.
func TestFoldSelectedCountSurvivesNarrowColumn(t *testing.T) {
	b := taskBoardFixture()
	b.marks = b.marks.add(19)
	finished := b.cardGroups()[taskColumnFinished]
	for _, w := range []int{20, 24, 30} {
		got := ansi.Strip(strings.Join(b.cardColumn(taskColumnFinished, finished, taskStop{}, "", 0, 0, w, 10, issueLaneMore{}), "\n"))
		if !strings.Contains(got, "✔ 1 selected") {
			t.Errorf("at width %d the selected count is cut:\n%s", w, got)
		}
	}
}
