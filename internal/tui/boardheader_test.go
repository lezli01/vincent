package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The board header's §11 slot count and the clauses that explain it (issue
// #324). The count itself is served — `store.CountSlotHolders` over every
// task row, lanes included — and proved end to end against the real handlers
// in boardslotslive_test.go. What is proved here is the rendering: which
// clauses appear, which are dropped, what a narrow panel sheds, and what a
// board that never reached /v1/info falls back to.

// headerCap is the cap every board here was configured with, so the header's
// denominator is a known quantity and its numerator is the figure under test.
const headerCap = 6

// servedBoard is a board that has heard from /v1/info, with the slot figures
// the daemon computed.
func servedBoard(slots apiclient.InfoSlots, tasks ...apiclient.Task) *board {
	b := testBoard()
	b.updateLoaded(boardLoadedMsg{tasks: tasks})
	b.update(boardInfoMsg{info: apiclient.Info{
		MaxParallelTasks: headerCap,
		Slots:            slots,
	}})
	return b
}

func headerText(b *board) string { return ansi.Strip(b.headerLine()) }

// TestHeaderRendersTheServedSlotCount: the numerator is the daemon's, not a
// walk of the listed rows. The board here lists one running root and one on a
// question while six slots are held — the reporter's board.
func TestHeaderRendersTheServedSlotCount(t *testing.T) {
	b := servedBoard(apiclient.InfoSlots{Used: 6, Lanes: 5, AwaitingInput: 1},
		task(1, stateAwaitingChildren), task(2, stateAwaitingInput))

	if got := headerText(b); !strings.Contains(got, "6/6 running") {
		t.Errorf("header = %q, want the served 6/6 rather than a count of the listed rows", got)
	}
}

// TestHeaderBreakdownExplainsAFullCap is the clause set the reporter asked
// for: a full cap with no rows to show for it is only an explanation once it
// says where the slots went.
func TestHeaderBreakdownExplainsAFullCap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		slots apiclient.InfoSlots
		want  string
	}{
		{"lanes only", apiclient.InfoSlots{Used: 6, Lanes: 6}, "6/6 running · 6 lanes"},
		{
			"lanes and input",
			apiclient.InfoSlots{Used: 3, Lanes: 2, AwaitingInput: 1},
			"3/6 running · 2 lanes · 1 on input",
		},
		{"input only", apiclient.InfoSlots{Used: 1, AwaitingInput: 1}, "1/6 running · 1 on input"},
		// A lone lane is a lane, not "1 lanes".
		{"one lane", apiclient.InfoSlots{Used: 1, Lanes: 1}, "1/6 running · 1 lane"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := servedBoard(tc.slots)
			if got := headerText(b); !strings.Contains(got, tc.want) {
				t.Errorf("header = %q, want it to carry %q", got, tc.want)
			}
		})
	}
}

// TestHeaderDropsZeroBreakdownClauses: an ordinary board — no fan-out, nobody
// on a question — reads exactly as it did before the clauses existed.
func TestHeaderDropsZeroBreakdownClauses(t *testing.T) {
	b := servedBoard(apiclient.InfoSlots{Used: 2},
		task(1, stateRunning), task(2, stateRunning))

	got := headerText(b)
	if !strings.Contains(got, "2/6 running") {
		t.Fatalf("header = %q, want the count", got)
	}
	for _, unwanted := range []string{"lane", "on input"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("header = %q, want no %q clause at zero", got, unwanted)
		}
	}
}

// TestHeaderCountsAQuestionTwice is an acceptance criterion of the issue: a
// task parked on a question is holding a slot *and* waiting on a person, so
// it belongs in the numerator and in the attention badge both. Fixing one
// count must not quietly move the task out of the other.
func TestHeaderCountsAQuestionTwice(t *testing.T) {
	b := servedBoard(apiclient.InfoSlots{Used: 1, AwaitingInput: 1},
		task(1, stateAwaitingInput))

	got := headerText(b)
	if !strings.Contains(got, "1/6 running") {
		t.Errorf("header = %q, want the question counted against the cap", got)
	}
	if !strings.Contains(got, "1 need attention") {
		t.Errorf("header = %q, want the question still counted as needing attention", got)
	}
}

// TestHeaderShedsBreakdownOnANarrowPanel: the clauses are the first thing to
// go when the line does not fit, last one first — the lanes outrank the
// question because a lane has no row on the board at all, while a task on a
// question is on screen to be read.
func TestHeaderShedsBreakdownOnANarrowPanel(t *testing.T) {
	b := servedBoard(apiclient.InfoSlots{Used: 3, Lanes: 2, AwaitingInput: 1},
		task(1, stateAwaitingChildren), task(2, stateAwaitingInput))

	b.width = 0
	full := b.headerLine()
	if got := ansi.Strip(full); !strings.Contains(got, "2 lanes") || !strings.Contains(got, "1 on input") {
		t.Fatalf("unsized header = %q, want both clauses", got)
	}

	// One cell short of the whole line: the last clause goes and no more.
	b.width = lipgloss.Width(full) - 1
	got := headerText(b)
	if strings.Contains(got, "on input") {
		t.Errorf("header = %q at width %d, want the last clause shed", got, b.width)
	}
	if !strings.Contains(got, "2 lanes") {
		t.Errorf("header = %q at width %d, want the lanes kept", got, b.width)
	}

	// A panel too narrow for either.
	b.width = 20
	got = headerText(b)
	for _, unwanted := range []string{"lane", "on input"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("header = %q at width 20, want no %q clause", got, unwanted)
		}
	}
	if !strings.Contains(got, "3/6 running") {
		t.Errorf("header = %q at width 20, want the count itself kept", got)
	}
}

// TestHeaderFallsBackWhenInfoNeverLoaded: a board that never reached the
// daemon renders what every earlier version rendered — a walk of the listed
// rows against an unknown cap — rather than a confident zero from an
// unpopulated struct.
func TestHeaderFallsBackWhenInfoNeverLoaded(t *testing.T) {
	b := testBoard()
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{
		task(1, stateRunning), task(2, stateRunning), task(3, stateQueued),
	}})
	if b.infoOK {
		t.Fatal("the fixture reached /v1/info, which is not what this tests")
	}

	got := headerText(b)
	if !strings.Contains(got, "2/? running") {
		t.Errorf("header = %q, want the listed-row count against an unknown cap", got)
	}
	for _, unwanted := range []string{"lane", "on input"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("header = %q, want no %q clause from figures that never arrived", got, unwanted)
		}
	}
}

// TestQuitReminderUsesTheServedSlotCount is §15's exit line on the same
// figure: a walk of the listed rows tells the person leaving that nothing is
// running while the fan-out lanes underneath a parked parent are.
func TestQuitReminderUsesTheServedSlotCount(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	b := m.views[viewHome].(*shell).board
	b.loaded = true

	t.Run("lanes with no running row", func(t *testing.T) {
		// The board lists one parked fan-out parent, which holds no slot
		// itself (§7.6). Its six lanes do.
		b.tasks = []apiclient.Task{task(1, stateAwaitingChildren)}
		b.update(boardInfoMsg{info: apiclient.Info{
			MaxParallelTasks: 6,
			Slots:            apiclient.InfoSlots{Used: 6, Lanes: 6},
		}})
		line, ok := m.quitReminder()
		if !ok {
			t.Fatal("no reminder with six lanes running")
		}
		if !strings.Contains(line, "6 tasks are still running") {
			t.Errorf("reminder = %q, want the six lanes reported", line)
		}
	})

	t.Run("one slot held", func(t *testing.T) {
		b.update(boardInfoMsg{info: apiclient.Info{
			MaxParallelTasks: 6,
			Slots:            apiclient.InfoSlots{Used: 1, AwaitingInput: 1},
		}})
		line, ok := m.quitReminder()
		if !ok {
			t.Fatal("no reminder with a slot held")
		}
		if !strings.Contains(line, "1 task is still running") {
			t.Errorf("reminder = %q, want the singular", line)
		}
	})

	t.Run("no slot held", func(t *testing.T) {
		b.tasks = []apiclient.Task{task(1, stateRunning)}
		b.update(boardInfoMsg{info: apiclient.Info{
			MaxParallelTasks: 6, Slots: apiclient.InfoSlots{},
		}})
		// The served zero wins over the stale row: the daemon is the one that
		// knows, and printing at zero is what §15 says not to do.
		if line, ok := m.quitReminder(); ok {
			t.Errorf("reminder = %q, want none when the daemon reports no slot held", line)
		}
	})
}

// TestRefreshRefetchesTheSlotCount is the header's refresh discipline: the
// count rides /v1/info, which is otherwise fetched on connect and on
// agent.quota_changed alone — so without this the one number that explains a
// full cap goes stale exactly when the cap fills (issue #324).
//
// The fetch leads the task list rather than being batched beside it: the
// runtime delivers batched commands in completion order, and /v1/info is the
// slower request, so a batch paints a count a window behind the rows it is
// explaining. The chain is what the live regression observes.
func TestRefreshRefetchesTheSlotCount(t *testing.T) {
	b := testBoard()
	// A closed port: the fetch fails fast, and what is under test is which
	// request goes out and in what order, not what comes back.
	b.client = apiclient.New("http://127.0.0.1:1", "token")
	b.refreshPending = true

	_, cmd := b.update(boardRefreshMsg{})
	if cmd == nil {
		t.Fatal("the debounce window closed without refetching anything")
	}
	msg, ok := cmd().(boardInfoMsg)
	if !ok {
		t.Fatalf("the refresh issued %T, want the slot count to lead", cmd())
	}
	if !msg.thenLoad {
		t.Fatal("the info fetch does not chain the task fetch; the rows would never refresh")
	}

	if _, cmd = b.update(msg); cmd == nil {
		t.Fatal("a failed info fetch swallowed the task refetch; the rows are what the board is for")
	}
}
