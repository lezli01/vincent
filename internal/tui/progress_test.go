package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestSpinnerFramesCycle holds the frame table's contract: consecutive frames
// differ, the cycle wraps, and a counter that has gone negative — which no
// caller does today, and which a future one must not be able to panic on —
// still yields a glyph.
func TestSpinnerFramesCycle(t *testing.T) {
	n := len(spinnerFrames)
	if n < 2 {
		t.Fatalf("the frame table has %d frames; nothing would move", n)
	}
	if spinnerFrame(0) == spinnerFrame(1) {
		t.Fatalf("frames 0 and 1 are both %q; the indicator would not move", spinnerFrame(0))
	}
	for i := range n {
		if got, want := spinnerFrame(i+n), spinnerFrame(i); got != want {
			t.Fatalf("frame %d wrapped to %q, want %q", i+n, got, want)
		}
	}
	if got := spinnerFrame(-1); got != spinnerFrames[n-1] {
		t.Fatalf("frame -1 = %q, want the last frame %q", got, spinnerFrames[n-1])
	}
}

// TestProgressLabelSpellsElapsedLikeTheRestOfTheTUI pins the second half of
// the indicator to formatElapsed at the boundaries where the spelling changes,
// and pins the clamp: a daemon clock ahead of this one must read as "just
// started", never as `-1s`.
func TestProgressLabelSpellsElapsedLikeTheRestOfTheTUI(t *testing.T) {
	cases := []struct {
		since time.Duration
		want  string
	}{
		{0, "0s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m00s"},
		{59*time.Minute + 59*time.Second, "59m59s"},
		{time.Hour, "1h00m"},
		{-5 * time.Second, "0s"},
	}
	for _, c := range cases {
		got := ProgressLabel(3, c.since)
		if !strings.HasPrefix(got, spinnerFrame(3)+" working… ") {
			t.Errorf("ProgressLabel(3, %v) = %q, want the frame and the word first", c.since, got)
		}
		if !strings.HasSuffix(got, c.want) {
			t.Errorf("ProgressLabel(3, %v) = %q, want it to end in %q", c.since, got, c.want)
		}
	}
}

// runningTurnAt builds one running chat turn that started `ago` before the
// fixture's pinned clock.
func runningTurnAt(v *chatView, ago time.Duration) apiclient.ChatTurn {
	return apiclient.ChatTurn{ID: 9, Seq: 1, State: "running", StartedAt: v.now().Add(-ago)}
}

// TestChatViewIndicatorSurvivesQuietAndScroll is issue #330's sharpest
// criterion: a running turn that has produced *no records at all*, read at
// levelQuiet, still says something is happening — and says it above the
// composer, so a reader who scrolled up has not lost it.
func TestChatViewIndicatorSurvivesQuietAndScroll(t *testing.T) {
	v := chatViewFixture()
	v.level.set(levelQuiet)
	v.turns = []apiclient.ChatTurn{runningTurnAt(v, 14*time.Second)}

	if body := plainLines(v.bodyLines(80)); strings.Contains(strings.Join(body, "\n"), "working…") {
		t.Fatalf("the indicator leaked into the scrolling body:\n%s", strings.Join(body, "\n"))
	}
	foot := strings.Join(plainLines(v.footerLines(80)), "\n")
	if !strings.Contains(foot, "working… 14s") {
		t.Fatalf("a running turn with no records shows no indicator:\n%s", foot)
	}

	// Follow off — the reader has scrolled up — must change nothing.
	v.following = false
	if foot := strings.Join(plainLines(v.footerLines(80)), "\n"); !strings.Contains(foot, "working…") {
		t.Fatalf("the indicator vanished with follow off:\n%s", foot)
	}
}

// TestChatViewIndicatorOnlyWhileRunning holds the other half: every state a
// turn can leave `running` for stops it, `awaiting_input` included — that turn
// is waiting on the human, not working, and the header already says so.
func TestChatViewIndicatorOnlyWhileRunning(t *testing.T) {
	for _, state := range []string{"done", "failed", "interrupted", "awaiting_input"} {
		v := chatViewFixture()
		turn := runningTurnAt(v, time.Minute)
		turn.State = state
		v.turns = []apiclient.ChatTurn{turn}
		if foot := strings.Join(plainLines(v.footerLines(80)), "\n"); strings.Contains(foot, "working…") {
			t.Errorf("a %s turn still animates:\n%s", state, foot)
		}
	}
}

// TestChatViewArmsTickOnlyWhileRunning covers both halves of the guard: a
// running turn arms exactly one tick, a chat with nothing running arms none,
// and a stray tick — tea.Tick cannot be cancelled, so one always survives the
// turn that armed it — is a no-op rather than a fresh ticker.
func TestChatViewArmsTickOnlyWhileRunning(t *testing.T) {
	v := chatViewFixture()
	_, cmd := v.update(chatLoadedMsg{id: 1, chat: v.chat, turns: []apiclient.ChatTurn{runningTurnAt(v, 0)}})
	if cmd == nil || !v.ticking {
		t.Fatalf("a running turn armed no tick (cmd=%v ticking=%v)", cmd != nil, v.ticking)
	}
	// A second message while one tick is already in flight must not arm a
	// second: two tickers would double the repaint rate for good.
	if _, cmd := v.update(tea.WindowSizeMsg{Width: 80, Height: 24}); cmd != nil {
		t.Fatal("a second message armed a second ticker")
	}
	// The turn ends, then the tick armed before it ended arrives.
	v.turns[0].State = "done"
	_, cmd = v.update(chatTickMsg(v.now()))
	if v.ticking {
		t.Fatal("a stray tick left the guard set")
	}
	if cmd != nil {
		t.Fatal("a tick delivered after the turn ended armed another")
	}

	idle := chatViewFixture()
	_, cmd = idle.update(chatLoadedMsg{
		id: 1, chat: idle.chat,
		turns: []apiclient.ChatTurn{{ID: 9, Seq: 1, State: "done"}},
	})
	if cmd != nil || idle.ticking {
		t.Fatalf("a chat with no running turn armed a tick (cmd=%v ticking=%v)", cmd != nil, idle.ticking)
	}
}

// TestChatsBoardAnimatesOnlyRunningRows covers the board's row: `running`
// carries the frame beside its label, nothing else does, and the repaint the
// tick causes is what makes the existing "last activity" cell advance.
func TestChatsBoardAnimatesOnlyRunningRows(t *testing.T) {
	v := chatsFixture()
	row := func(state string) string {
		c := testChat(3, state, "third")
		return strings.Join(plainLines([]string{v.rowLine(chatRow{chat: &c}, false, 120)}), "")
	}
	running := row("running")
	if !strings.Contains(running, "running "+spinnerFrame(v.frame)) {
		t.Fatalf("a running row carries no frame beside its label: %q", running)
	}
	for _, state := range []string{"idle", "awaiting_input", "archived", "handed_off"} {
		line := row(state)
		for _, f := range spinnerFrames {
			if strings.Contains(line, f) {
				t.Errorf("a %s row animates: %q", state, line)
			}
		}
	}

	// The activity cell is time-since-the-turn-started for a running chat and
	// was only ever missing a repaint.
	c := testChat(3, "running", "third")
	before := chatActivity(c, v.now())
	after := chatActivity(c, v.now().Add(time.Minute))
	if before == after {
		t.Fatalf("the activity cell did not advance across a simulated minute: %q", before)
	}
}

// TestChatsBoardArmsTickOnlyWhileRunning is the board's half of the arming
// contract, including the stray tick.
func TestChatsBoardArmsTickOnlyWhileRunning(t *testing.T) {
	v := newChatsView()
	v.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	_, cmd := v.update(chatsLoadedMsg{
		chats: []apiclient.Chat{testChat(1, "running", "first")},
		names: map[int64]string{7: "repo"},
	})
	if cmd == nil || !v.ticking {
		t.Fatalf("a running chat armed no tick (cmd=%v ticking=%v)", cmd != nil, v.ticking)
	}
	v.chats[0].State = "idle"
	if _, cmd := v.update(chatsTickMsg(v.now())); cmd != nil || v.ticking {
		t.Fatalf("a tick after the last running chat stopped armed another (cmd=%v)", cmd != nil)
	}

	idle := newChatsView()
	idle.now = v.now
	_, cmd = idle.update(chatsLoadedMsg{
		chats: []apiclient.Chat{testChat(1, "idle", "first"), testChat(2, "awaiting_input", "second")},
		names: map[int64]string{7: "repo"},
	})
	if cmd != nil || idle.ticking {
		t.Fatalf("a board with nothing running armed a tick (cmd=%v ticking=%v)", cmd != nil, idle.ticking)
	}
}

// TestDetailIndicatorOnOutputTabOnly covers the task workspace: the border
// title carries the indicator for a live attempt on the output tab, and
// nowhere else. The gate is StepRun.Live() and not the step's type — a long
// `command:` step is silent for the same reason and for as long.
func TestDetailIndicatorOnOutputTabOnly(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 5
	loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
	title := func() string { return plainLines([]string{d.outputTitle()})[0] }
	if !strings.Contains(title(), "working… 1m00s") {
		t.Fatalf("a live attempt shows no indicator in the pane title: %q", title())
	}

	run := attempt(1, 0, 1, "implement", "running", true)
	run.StepType = "command"
	d.task.Steps = []apiclient.StepRun{run}
	if !strings.Contains(title(), "working…") {
		t.Fatalf("a live command step shows no indicator: %q", title())
	}

	d.tab = tabDiff
	if strings.Contains(title(), "working…") {
		t.Fatalf("the diff tab draws the output pane's indicator: %q", title())
	}

	d.tab = tabOutput
	d.task.Steps = []apiclient.StepRun{attempt(1, 0, 1, "implement", "succeeded", false)}
	if strings.Contains(title(), "working…") {
		t.Fatalf("a finished attempt still animates: %q", title())
	}
}

// TestDetailArmsTickOnlyWhileLive is the task workspace's half of the arming
// contract, including the stray tick.
func TestDetailArmsTickOnlyWhileLive(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 5
	loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
	if cmd := d.update(tea.WindowSizeMsg{Width: 120, Height: 30}); cmd == nil || !d.ticking {
		t.Fatalf("a live attempt armed no tick (cmd=%v ticking=%v)", cmd != nil, d.ticking)
	}
	d.task.Steps = []apiclient.StepRun{attempt(1, 0, 1, "implement", "succeeded", false)}
	if cmd := d.update(detailTickMsg(fixedNow)); cmd != nil || d.ticking {
		t.Fatalf("a tick after the attempt finished armed another (cmd=%v)", cmd != nil)
	}

	done := newTestDetail(t)
	done.taskID = 5
	loadDetail(done, []apiclient.StepRun{attempt(1, 0, 1, "implement", "succeeded", false)})
	if cmd := done.update(tea.WindowSizeMsg{Width: 120, Height: 30}); cmd != nil || done.ticking {
		t.Fatalf("a finished task armed a tick (cmd=%v ticking=%v)", cmd != nil, done.ticking)
	}
}
