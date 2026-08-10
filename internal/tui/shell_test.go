package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newShellFixture builds a shell with a counting stream opener and a loaded,
// rendered board. The client stays nil: commands returned by updates are
// never executed here, so nothing fetches, and the subscription count is
// exactly the openStream calls syncStream made.
func newShellFixture(t *testing.T, tasks ...apiclient.Task) (*shell, *int) {
	t.Helper()
	s := newShell(testCtx(t))
	s.board.now = func() time.Time { return testNow }
	s.board.bell = func() {}
	subs := new(int)
	s.detail.openStream = func(context.Context, int64, apiclient.StreamOptions) <-chan apiclient.Note {
		*subs++
		return make(chan apiclient.Note)
	}
	s.update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if len(tasks) > 0 {
		s.update(boardLoadedMsg{tasks: tasks})
	}
	s.render(120, 37)
	return s, subs
}

// settle drains the pending settle window the way the runtime would: the
// cursor rested, the tick fired.
func (s *shell) settle() {
	s.update(selectionSettledMsg{id: s.lastSel})
}

// TestShellHoldingDownOpensOneSubscription is the T3.10 done-when: holding
// `down` across the table must open one subscription — for the row the
// cursor settles on — not one per row it passed through.
func TestShellHoldingDownOpensOneSubscription(t *testing.T) {
	tasks := make([]apiclient.Task, 0, 6)
	for id := int64(1); id <= 6; id++ {
		tasks = append(tasks, task(id, stateRunning))
	}
	s, subs := newShellFixture(t, tasks...)

	// The initial selection settles on the first row and subscribes once.
	s.settle()
	if *subs != 1 {
		t.Fatalf("subscriptions after the first settle = %d, want 1", *subs)
	}
	first := s.detail.taskID

	// Hold `down` across the rest of the table. Every move tears the stream
	// down at once and nothing new opens while the cursor is moving.
	for range 5 {
		s.update(tea.KeyPressMsg{Code: tea.KeyDown})
		s.render(120, 37)
	}
	if *subs != 1 {
		t.Fatalf("subscriptions while moving = %d, want still 1", *subs)
	}
	if s.detail.taskID != 0 {
		t.Fatalf("detail still tracks #%d mid-move, want a torn-down panel", s.detail.taskID)
	}
	if s.detail.streamID != 0 {
		t.Fatalf("stream still open for #%d mid-move; the unsubscribe is not immediate", s.detail.streamID)
	}

	// Stale settle windows — armed for rows the cursor already left — are
	// ignored; only the row it rests on opens.
	s.update(selectionSettledMsg{id: first})
	if *subs != 1 || s.detail.taskID != 0 {
		t.Fatal("a stale settle window opened a task")
	}
	s.settle()
	if *subs != 2 {
		t.Fatalf("subscriptions after the cursor rested = %d, want 2", *subs)
	}
	if got, ok := s.board.selected(); !ok || s.detail.taskID != got {
		t.Fatalf("detail tracks #%d, cursor is on #%d", s.detail.taskID, got)
	}
}

// TestShellSubscribesOnlyForRunningTasks: a parked task has no live output,
// so settling on it fetches but never streams.
func TestShellSubscribesOnlyForRunningTasks(t *testing.T) {
	s, subs := newShellFixture(t, task(1, stateBlocked), task(2, stateDone))
	s.settle()
	if s.detail.taskID == 0 {
		t.Fatal("settling did not open the task")
	}
	if *subs != 0 {
		t.Fatalf("subscriptions = %d for a task that is not running, want 0", *subs)
	}
}

// TestShellFocusCycling covers tab/shift+tab and esc-back-to-the-spine.
func TestShellFocusCycling(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning))
	if s.focus != panelTasks {
		t.Fatalf("initial focus = %v, want the task table", s.focus)
	}
	press := func(k tea.KeyPressMsg) { s.update(k) }
	press(tea.KeyPressMsg{Code: tea.KeyTab})
	if s.focus != panelTimeline {
		t.Fatalf("after tab focus = %v, want timeline", s.focus)
	}
	press(tea.KeyPressMsg{Code: tea.KeyTab})
	if s.focus != panelOutput {
		t.Fatalf("after tab tab focus = %v, want output", s.focus)
	}
	press(tea.KeyPressMsg{Code: tea.KeyTab})
	if s.focus != panelTasks {
		t.Fatalf("tab does not wrap: focus = %v", s.focus)
	}
	press(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if s.focus != panelOutput {
		t.Fatalf("shift+tab focus = %v, want output", s.focus)
	}
	// esc is not a focus key (§15 stack): with no popup and no filter it is
	// a no-op, and it never quits.
	press(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.focus != panelOutput {
		t.Fatalf("esc moved focus to %v, want it to stay put", s.focus)
	}
}

// TestShellEnterOpensImmediately: enter is a deliberate action, so it skips
// the settle window and lands focused on the timeline.
func TestShellEnterOpensImmediately(t *testing.T) {
	s, subs := newShellFixture(t, task(9, stateRunning))
	s.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.focus != panelTimeline {
		t.Fatalf("focus = %v, want timeline after enter", s.focus)
	}
	if s.detail.taskID != 9 {
		t.Fatalf("detail task = %d, want 9 without waiting for a settle", s.detail.taskID)
	}
	if *subs != 1 {
		t.Fatalf("subscriptions = %d, want 1", *subs)
	}
}

// TestShellAnswerPopup: the form never opens itself; enter opens it, its
// keys stay inside it, esc closes one layer, and an answered request takes
// the popup with it.
func TestShellAnswerPopup(t *testing.T) {
	s, _ := newShellFixture(t, task(3, stateAwaitingInput))
	s.settle()
	if s.detail.taskID != 3 {
		t.Fatalf("detail task = %d, want 3", s.detail.taskID)
	}
	s.detail.form = newAnswerForm(apiclient.InputRequest{
		Kind: apiclient.InputKindQuestion,
		Questions: []apiclient.InputQuestion{
			{Text: "Which colour?", Options: []string{"teal", "mauve"}},
		},
	})
	if s.popup {
		t.Fatal("the popup opened itself")
	}

	s.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !s.popup {
		t.Fatal("enter did not open the answer popup")
	}
	if !strings.Contains(s.render(120, 37), "Which colour?") {
		t.Fatal("open popup does not render the question")
	}

	// Keys go to the form, not the panels: space picks an option.
	s.update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if got := s.detail.form.answers["Which colour?"]; len(got) != 1 || got[0] != "teal" {
		t.Fatalf("space picked %v, want the first option", got)
	}
	if s.focus != panelTasks {
		t.Fatalf("focus moved to %v while the popup was open", s.focus)
	}

	// esc closes the popup — one layer — and the typed answer survives for
	// the next open.
	s.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if s.popup {
		t.Fatal("esc did not close the popup")
	}
	if got := s.detail.form.answers["Which colour?"]; len(got) != 1 {
		t.Fatalf("closing the popup lost the picked answer: %v", got)
	}

	// A cleared request (the refetch after an answer) closes an open popup.
	s.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s.detail.form = nil
	s.update(boardLoadedMsg{tasks: []apiclient.Task{task(3, stateRunning)}})
	if s.popup {
		t.Fatal("the popup outlived its request")
	}
}

// TestShellReopensAfterALostSettle: a settle window that fires while a
// takeover screen is active is delivered there and lost; returning to the
// home screen must not leave the panels on "loading…" forever.
func TestShellReopensAfterALostSettle(t *testing.T) {
	s, subs := newShellFixture(t, task(1, stateRunning))
	// The cursor rested on row 1, but its settle tick fired into a takeover.
	if s.lastSel != 1 || s.detail.taskID != 0 {
		t.Fatalf("fixture: lastSel=%d taskID=%d, want a pending selection", s.lastSel, s.detail.taskID)
	}
	s.update(viewDeactivatedMsg{id: viewHome})
	s.update(viewActivatedMsg{id: viewHome})
	if s.detail.taskID != 1 {
		t.Fatalf("detail task = %d, want the tracked row reopened", s.detail.taskID)
	}
	if *subs != 1 {
		t.Fatalf("subscriptions = %d, want 1 for the reopened running task", *subs)
	}
}

// TestShellTabCommitsFilter: a filter is view state, not a mode — tab
// commits it and moves focus, the committed value names itself in the
// panel title, and only esc clears it (§15).
func TestShellTabCommitsFilter(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning), task(2, stateDone))
	s.update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !s.board.filtering {
		t.Fatal("/ did not start the filter")
	}
	for _, r := range "running" {
		s.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	s.update(tea.KeyPressMsg{Code: tea.KeyTab})
	if s.board.filtering {
		t.Fatal("tab did not commit the filter")
	}
	if got := s.board.filter.Value(); got != "running" {
		t.Fatalf("filter value = %q, want it kept", got)
	}
	if s.focus != panelTimeline {
		t.Fatalf("focus = %v, want tab to have moved it", s.focus)
	}
	if title := s.panelTitle(panelTasks); !strings.Contains(title, "/running") {
		t.Fatalf("panel title = %q, want the committed filter named", title)
	}
	// esc clears the filter from any panel focus — the filter layer of the
	// stack sits below the popups.
	s.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := s.board.filter.Value(); got != "" {
		t.Fatalf("filter value = %q after esc, want cleared", got)
	}
	if title := s.panelTitle(panelTasks); strings.Contains(title, "/running") {
		t.Fatalf("panel title = %q still names a cleared filter", title)
	}
}

// TestShellJumpAttention: ! cycles through the tasks needing a human,
// opening each immediately — the board has always pinned and belled those
// tasks without offering any way to go to one.
func TestShellJumpAttention(t *testing.T) {
	s, _ := newShellFixture(t,
		task(1, stateRunning), task(2, stateAwaitingInput), task(3, stateBlocked))
	var want []int64
	for _, row := range s.board.visible() {
		if needsAttention(row.State) {
			want = append(want, row.ID)
		}
	}
	if len(want) != 2 {
		t.Fatalf("fixture: %d attention rows, want 2", len(want))
	}

	// The attention rows are pinned to the top, so the cursor already sits
	// on the first one: the jump goes to the *next*, then wraps.
	s.update(jumpAttentionMsg{})
	if s.detail.taskID != want[1] {
		t.Fatalf("first jump opened #%d, want #%d", s.detail.taskID, want[1])
	}
	s.render(120, 37) // seat the cursor on the jumped row
	s.update(jumpAttentionMsg{})
	if s.detail.taskID != want[0] {
		t.Fatalf("second jump opened #%d, want the wrap to #%d", s.detail.taskID, want[0])
	}
	s.render(120, 37)
	s.update(jumpAttentionMsg{})
	if s.detail.taskID != want[1] {
		t.Fatalf("third jump opened #%d, want #%d", s.detail.taskID, want[1])
	}
}

// TestShellRendersThreePanes pins the composed frame: three titled panels,
// exactly one carrying the focus glyph, the action-bar line underneath.
func TestShellRendersThreePanes(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning))
	s.settle()
	got := s.render(120, 37)

	for _, want := range []string{"Tasks", "Timeline — #1", "output", "diff"} {
		if !strings.Contains(got, want) {
			t.Errorf("frame missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, focusGlyph); n != 1 {
		t.Errorf("focus glyph appears %d times, want exactly 1", n)
	}
	// The frame must fill the box heights exactly: body 37 lines.
	if lines := strings.Count(got, "\n") + 1; lines != 37 {
		t.Errorf("frame is %d lines, want 37", lines)
	}
	// No line may exceed the body width — a panel leaking into its
	// neighbour breaks the tiling silently.
	for i, line := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(line); w > 120 {
			t.Errorf("line %d is %d cells wide, want ≤ 120", i, w)
		}
	}
}

// TestShellSinglePanelMode: below 80×20 the focused panel gets the whole
// area and tab swaps which one that is (§15).
func TestShellSinglePanelMode(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning))
	s.update(tea.WindowSizeMsg{Width: 70, Height: 18})
	got := s.render(70, 15)
	if !strings.Contains(got, "Tasks") || strings.Contains(got, "Timeline") {
		t.Fatalf("single-panel mode shows more than the focused panel:\n%s", got)
	}
	s.update(tea.KeyPressMsg{Code: tea.KeyTab})
	got = s.render(70, 15)
	if !strings.Contains(got, "Timeline") || strings.Contains(got, "Tasks") {
		t.Fatalf("tab did not swap the single panel:\n%s", got)
	}
}

// TestShellTooSmall: below 60×15 the shell renders the size it has and the
// size it needs, nothing else (§15).
func TestShellTooSmall(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning))
	s.update(tea.WindowSizeMsg{Width: 58, Height: 14})
	got := s.render(58, 11)
	if !strings.Contains(got, "terminal too small (58×14, need 60×15)") {
		t.Fatalf("too-small floor not stated:\n%s", got)
	}
	if strings.Contains(got, "Tasks") {
		t.Fatalf("too-small mode still renders panels:\n%s", got)
	}
}

// TestShellDisconnectedBanner: the panels stay on screen marked stale behind
// a banner — nothing force-navigates (§15 Disconnected).
func TestShellDisconnectedBanner(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning))
	s.setConnected(false)
	got := s.render(120, 37)
	for _, want := range []string{"daemon unreachable", "retry", "stale", "Tasks"} {
		if !strings.Contains(got, want) {
			t.Errorf("disconnected frame missing %q:\n%s", want, got)
		}
	}
}

// TestShellActionKeysWorkFromEveryPanel: task actions act on the task, not
// on a pane, so the focused panel must not gate them away.
func TestShellActionKeysWorkFromEveryPanel(t *testing.T) {
	s, _ := newShellFixture(t, task(4, stateRunning, func(t *apiclient.Task) {
		t.AvailableActions = []string{apiclient.ActionPause}
	}))
	// A real client, never called: the returned commands are not executed,
	// but a nil client makes the action bar refuse with "not connected".
	c := apiclient.New("http://127.0.0.1:1", "token")
	s.board.client = c
	s.detail.client = c
	s.settle()
	// The detail fetch is a command this test never executes; install its
	// result so detail's bar knows the same available_actions the row does.
	s.detail.applyLoaded(detailLoadedMsg{id: 4, task: apiclient.TaskDetail{
		Task: apiclient.Task{ID: 4, State: stateRunning, AvailableActions: []string{apiclient.ActionPause}},
	}})
	for _, focus := range []panelID{panelTasks, panelTimeline, panelOutput} {
		s.focus = focus
		_, cmd := s.update(tea.KeyPressMsg{Code: 'p', Text: "p"})
		if cmd == nil {
			t.Errorf("p from %v produced no action command", focus)
		}
	}
}

// TestShellColourDowngrade is the §15 colour contract: the palette degrades
// under NO_COLOR (ASCII profile) and on 16-colour terminals without losing
// content, and focus stays discernible because it is a glyph, not only a
// colour.
func TestShellColourDowngrade(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning), task(2, stateAwaitingInput))
	s.update(tea.KeyPressMsg{Code: tea.KeyTab}) // focus timeline: glyph off the default
	frame := s.render(120, 37)

	downgrade := func(p colorprofile.Profile) string {
		var buf bytes.Buffer
		w := &colorprofile.Writer{Forward: &buf, Profile: p}
		if _, err := w.Write([]byte(frame)); err != nil {
			t.Fatalf("downgrade write: %v", err)
		}
		return buf.String()
	}

	t.Run("NO_COLOR", func(t *testing.T) {
		plain := downgrade(colorprofile.ASCII)
		if ansi.Strip(plain) != ansi.Strip(frame) {
			t.Error("stripping colour changed the content")
		}
		if !strings.Contains(plain, focusGlyph+" Timeline") {
			t.Error("focus is not discernible without colour")
		}
		for _, seq := range []string{"[38;5;", "[38;2;", "[48;5;", "[31m", "[32m"} {
			if strings.Contains(plain, seq) {
				t.Errorf("colour sequence %q survived the NO_COLOR downgrade", seq)
			}
		}
	})

	t.Run("16-colour", func(t *testing.T) {
		basic := downgrade(colorprofile.ANSI)
		if ansi.Strip(basic) != ansi.Strip(frame) {
			t.Error("the 16-colour downgrade changed the content")
		}
		for _, seq := range []string{"[38;5;", "[38;2;", "[48;5;", "[48;2;"} {
			if strings.Contains(basic, seq) {
				t.Errorf("extended colour %q survived the 16-colour downgrade", seq)
			}
		}
	})
}
