package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// retriedDetail is one step retried n times: n attempts under one header,
// the shape #799 W11 was reported against (39 of them).
func retriedDetail(t *testing.T, n int) *detail {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 799
	rows := make([]apiclient.StepRun, 0, n)
	for i := 1; i <= n; i++ {
		rows = append(rows, attempt(int64(i), 0, i, "implement", "failed", false))
	}
	loadDetail(d, rows)
	d.focus = focusTimeline
	return d
}

// TestStepsTabJumpsToEitherEnd: reaching the first of 39 attempts took 39
// presses of ↑. g and home are one press to the first, G and end one to the
// last, and pgup/pgdown move a page.
func TestStepsTabJumpsToEitherEnd(t *testing.T) {
	d := retriedDetail(t, 39)
	d.renderTimeline(20)
	if d.selectedRun != 39 {
		t.Fatalf("arrival selection = %d, want the latest attempt 39", d.selectedRun)
	}

	for _, k := range []tea.KeyPressMsg{key("g"), {Code: tea.KeyHome}} {
		d.selectedRun = 39
		d.updateKey(k)
		if d.selectedRun != 1 {
			t.Errorf("%s: selection = %d, want the first attempt 1", k.String(), d.selectedRun)
		}
	}
	for _, k := range []tea.KeyPressMsg{key("G"), {Code: tea.KeyEnd}} {
		d.selectedRun = 1
		d.updateKey(k)
		if d.selectedRun != 39 {
			t.Errorf("%s: selection = %d, want the last attempt 39", k.String(), d.selectedRun)
		}
	}

	// A page is what the window showed, so pgup from the bottom lands on a
	// row that was above it and is now on screen.
	d.selectedRun = 39
	d.renderTimeline(20)
	page := d.timelinePage
	if page < 2 {
		t.Fatalf("page = %d stops, want a window's worth", page)
	}
	d.updateKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if want := int64(39 - page); d.selectedRun != want {
		t.Errorf("pgup: selection = %d, want %d", d.selectedRun, want)
	}
	d.renderTimeline(20)
	if !slices.Contains(d.visibleRuns, d.selectedRun) {
		t.Errorf("pgup left the selection %d off screen", d.selectedRun)
	}
	from, page := d.selectedRun, d.timelinePage
	d.updateKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if want := min(from+int64(page), 39); d.selectedRun != want {
		t.Errorf("pgdown: selection = %d, want %d", d.selectedRun, want)
	}
}

// TestStepsTabWindowHoldsStillInside: the window used to re-centre on every
// move, so the list slid under a cursor that never left the screen. A move
// that stays inside the window moves only the highlight.
func TestStepsTabWindowHoldsStillInside(t *testing.T) {
	d := retriedDetail(t, 39)
	const height = 20
	d.renderTimeline(height)
	first := d.timelineOffset
	visible := d.timelinePage
	if visible < 3 {
		t.Fatalf("window shows %d attempts, want room to move inside it", visible)
	}
	for range visible - 1 {
		d.updateKey(key("k"))
		d.renderTimeline(height)
		if d.timelineOffset != first {
			t.Fatalf("↑ to %d moved the window from %d to %d", d.selectedRun, first, d.timelineOffset)
		}
	}
	// One more leaves it, and the window follows by as little as it can.
	d.updateKey(key("k"))
	got := ansi.Strip(d.renderTimeline(height))
	if d.timelineOffset >= first {
		t.Errorf("leaving the top did not move the window (offset %d, was %d)", d.timelineOffset, first)
	}
	if !slices.Contains(d.visibleRuns, d.selectedRun) {
		t.Errorf("selection %d off screen:\n%s", d.selectedRun, got)
	}
	lines := strings.Split(got, "\n")
	if !strings.Contains(lines[0], "↑") || !strings.Contains(lines[0], "more") {
		t.Errorf("rows hidden above with no cue:\n%s", got)
	}
	if !strings.Contains(lines[len(lines)-1], "↓") || !strings.Contains(lines[len(lines)-1], "more") {
		t.Errorf("rows hidden below with no cue:\n%s", got)
	}
	if len(lines) > height {
		t.Errorf("window drew %d rows, want at most %d", len(lines), height)
	}
}

// TestStepsTabExpandedLoopSaysWhatIsAbove: an opened loop scrolled to
// `iteration 10` used to start there with nothing saying nine passes and a
// header were above it. It says so, and the top edge is a whole entry.
func TestStepsTabExpandedLoopSaysWhatIsAbove(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 11
	rows := []apiclient.StepRun{attempt(1, 0, 1, "build", "succeeded", false)}
	id := int64(2)
	for it := 1; it <= 12; it++ {
		for _, name := range []string{"suite", "repair"} {
			rows = append(rows, iteration(id, it, name, "succeeded", fmt.Sprintf("item-%d", it)))
			id++
		}
	}
	d.applyLoaded(detailLoadedMsg{
		id: d.taskID,
		task: apiclient.TaskDetail{
			Task:  apiclient.Task{ID: d.taskID, Title: "looped", State: stateRunning, StepTotal: 2},
			Steps: rows,
			WorkflowSteps: []apiclient.WorkflowStep{
				{Index: 0, ID: "build", Type: "command"},
				{Index: 1, ID: "green", Type: "loop"},
			},
		},
	})
	d.focus = focusTimeline
	d.updateKey(key("O")) // every tier open
	// Walk up from the latest to iteration 10's first row, the way a reader
	// would; the window follows.
	const height = 10
	want := int64(2 + 9*2) // iteration 10, `suite`
	for range rows {
		d.renderTimeline(height)
		if d.selectedRun == want {
			break
		}
		d.updateKey(key("k"))
	}
	if d.selectedRun != want {
		t.Fatalf("never reached iteration 10 (selection %d)", d.selectedRun)
	}
	got := ansi.Strip(d.renderTimeline(height))
	lines := strings.Split(got, "\n")
	if !strings.Contains(lines[0], "↑") || !strings.Contains(lines[0], "more") {
		t.Errorf("the window starts mid-loop with no more-above cue:\n%s", got)
	}
	if !strings.Contains(got, "iteration 10") {
		t.Errorf("iteration 10 not on screen:\n%s", got)
	}
	if len(lines) > height {
		t.Errorf("window drew %d rows, want at most %d", len(lines), height)
	}
}

// TestTimelineWindowNeverSplitsAnEntry: the top edge snaps back to the line
// an entry starts on, whatever offset it was handed.
func TestTimelineWindowNeverSplitsAnEntry(t *testing.T) {
	// Entries of three lines each: 0,3,6,…
	starts := func(i int) bool { return i%3 == 0 }
	for offset := range 30 {
		start, end := timelineWindow(30, starts, starts, 27, 30, offset, 8)
		if start%3 != 0 {
			t.Errorf("offset %d: window starts mid-entry at %d", offset, start)
		}
		if end < 30 {
			t.Errorf("offset %d: selection's last line cut (end %d)", offset, end)
		}
	}
}

// TestOutputTabJumpsToTheTop: the Output pane had G/end for the tail and no
// way back to the start. g and home scroll to the top and drop follow.
func TestOutputTabJumpsToTheTop(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{key("g"), {Code: tea.KeyHome}} {
		d := newTestDetail(t)
		d.focus = focusOutput
		d.vp.SetWidth(40)
		d.vp.SetHeight(5)
		body := make([]string, 50)
		for i := range body {
			body[i] = fmt.Sprintf("line %d", i)
		}
		d.vp.SetContent(strings.Join(body, "\n"))
		d.vp.GotoBottom()
		d.following = true

		d.updateKey(k)
		if d.vp.YOffset() != 0 {
			t.Errorf("%s: offset = %d, want the top", k.String(), d.vp.YOffset())
		}
		if d.following {
			t.Errorf("%s: still following at the top", k.String())
		}
	}
}
