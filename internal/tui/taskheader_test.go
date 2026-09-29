package tui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 129.9: the workspace names itself with a breadcrumb in the app header,
// says its `#id title` once, and while it runs carries a now-line.

func TestBreadcrumbIsTheBoardTheStackTheTaskAndTheTab(t *testing.T) {
	v := newTaskView(taskDetailFixture(t)) // task 7
	v.tab = taskTabOutput
	if got, want := strings.Join(v.crumbs(), crumbSep), "Board › #7 › Output"; got != want {
		t.Errorf("breadcrumb opened from the board = %q, want %q", got, want)
	}

	v.stack = []int64{3, 5}
	v.crumb = map[int64]string{3: "#3", 5: "lane #5 api"}
	v.tab = taskTabDiff
	if got, want := strings.Join(v.crumbs(), crumbSep), "Board › #3 › lane #5 api › #7 › Diff"; got != want {
		t.Errorf("breadcrumb two jumps deep = %q, want %q", got, want)
	}
}

// A task that is a fan-out lane is crumbed the way the Output lane selector
// names it, and the label is recorded when a jump leaves it — the stack holds
// only ids.
func TestPushRecordsTheLaneCrumbLabel(t *testing.T) {
	d := taskDetailFixture(t)
	parent, lane := int64(3), "api"
	d.task.ParentTaskID, d.task.LaneID = &parent, &lane
	v := newTaskView(d)
	if got, want := v.currentCrumb(), "lane #7 api"; got != want {
		t.Fatalf("lane crumb = %q, want %q", got, want)
	}
	v.pushTask(7)
	if got := v.crumb[7]; got != "lane #7 api" {
		t.Fatalf("pushTask recorded crumb %q, want the lane label", got)
	}
	// A fresh open from the board forgets the stack and its labels.
	v.stackPush = 0
	v.update(selectTaskMsg{id: 9})
	if len(v.stack) != 0 || v.crumb != nil {
		t.Fatalf("a fresh open kept stack %v and crumbs %v", v.stack, v.crumb)
	}
}

func TestBreadcrumbTruncatesFromTheLeft(t *testing.T) {
	crumbs := []string{
		"Board", "#101", "lane #102 a-rather-long-lane-name", "#103",
		"lane #104 another-long-lane-name", "lane #105 api", "Step Details",
	}
	for _, width := range []int{80, 40} {
		got := fitCrumbs(crumbs, width)
		if w := ansi.StringWidth(got); w > width {
			t.Errorf("width %d: breadcrumb is %d wide: %q", width, w, got)
		}
		if !strings.HasPrefix(got, "…"+crumbSep) {
			t.Errorf("width %d: dropped crumbs are not marked on the left: %q", width, got)
		}
		if !strings.HasSuffix(got, "lane #105 api"+crumbSep+"Step Details") {
			t.Errorf("width %d: the current task or its tab was dropped: %q", width, got)
		}
	}
	if got := fitCrumbs(crumbs[:3], 80); got != strings.Join(crumbs[:3], crumbSep) {
		t.Errorf("a breadcrumb that fits was cut: %q", got)
	}
}

// The `#id title` line appears exactly once on every tab.
func TestTaskTitleIsDrawnOncePerTab(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabOverview)
	v.detail.task.Title = "a title only drawn once"
	for _, tab := range v.tabs() {
		v.tab = tab
		out := ansi.Strip(v.render(110, 30))
		if n := strings.Count(out, "a title only drawn once"); n != 1 {
			t.Errorf("%v: the title appears %d times, want once:\n%s", tab, n, out)
		}
		if first := strings.SplitN(out, "\n", 2)[0]; !strings.HasPrefix(first, " #4 a title only drawn once") {
			t.Errorf("%v: the first line is %q, want the title line", tab, first)
		}
	}
}

// runningWorkspace is the root on a running task whose live attempt is 2.
func runningWorkspace(t *testing.T, status string) (*root, *taskView) {
	t.Helper()
	m := connectedRoot(t)
	v := m.views[viewTask].(*taskView)
	d := v.detail
	d.taskID = 4
	live := attempt(2, 1, 1, "verify", "running", true)
	if status != "" {
		live.StatusMessage = &status
	}
	loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "succeeded", false), live})
	m.active = viewTask
	return m, v
}

func TestWorkspaceFrameIsUntitledAndTheHeaderIsTheBreadcrumb(t *testing.T) {
	m, _ := runningWorkspace(t, "")
	lines := strings.Split(ansi.Strip(content(m)), "\n")
	if !strings.Contains(lines[0], "Board › #4 › Overview") {
		t.Errorf("app header = %q, want the breadcrumb", lines[0])
	}
	if strings.Contains(lines[0], "[Task") {
		t.Errorf("app header still carries the view tag: %q", lines[0])
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "┌") && strings.Contains(line, "Task") {
			t.Errorf("the workspace frame is titled: %q", line)
		}
	}
	if len(lines) != m.height {
		t.Errorf("the screen is %d lines, want %d: the now-line is not in the body budget", len(lines), m.height)
	}
}

func TestNowLinePrefersTheStatusMessage(t *testing.T) {
	m, v := runningWorkspace(t, "running the unit tests")
	line, ok := v.nowLine(120)
	if !ok {
		t.Fatal("no now-line for a running task")
	}
	if got := ansi.Strip(line); got != " "+statusGlyph+" running the unit tests" {
		t.Errorf("now-line = %q, want the status message behind its glyph", got)
	}
	if want := styleStatus.Render(" " + statusGlyph + " running the unit tests"); line != want {
		t.Errorf("now-line is not styled as the timeline styles a status: %q", line)
	}
	if lines := strings.Split(ansi.Strip(content(m)), "\n"); !strings.Contains(lines[1], "running the unit tests") {
		t.Errorf("the line under the header is %q, want the now-line", lines[1])
	}
	// The Overview's running frame no longer says it a second time.
	if out := ansi.Strip(strings.Join(v.overviewLines(120, 40), "\n")); strings.Contains(out, "latest status") {
		t.Errorf("the running Overview still lists latest status:\n%s", out)
	}
}

func TestNowLineFallsBackToTheAttemptName(t *testing.T) {
	_, v := runningWorkspace(t, "")
	line, ok := v.nowLine(120)
	if !ok {
		t.Fatal("no now-line for a running task")
	}
	if got, want := strings.TrimSpace(ansi.Strip(line)), attemptName(v.detail.runByID(2)); got != want {
		t.Errorf("now-line with nothing said = %q, want the attempt's name %q", got, want)
	}
	// One line, truncated to the width it is given.
	narrow, _ := v.nowLine(24)
	if w := ansi.StringWidth(narrow); w > 24 || !strings.HasSuffix(ansi.Strip(narrow), "…") {
		t.Errorf("now-line at 24 columns = %q (%d wide), want it truncated", ansi.Strip(narrow), w)
	}
}

// A chunk for the live attempt reaches the now-line even while the pane
// displays an earlier attempt, and the next chunk replaces it.
func TestNowLineFollowsTheLiveRunNotTheDisplayedOne(t *testing.T) {
	_, v := runningWorkspace(t, "")
	d := v.detail
	d.displayRun = 1
	for i, text := range []string{"compiling", "PASS ok internal/tui"} {
		payload, _ := json.Marshal(map[string]any{"text": text})
		d.handleChunk(apiclient.OutputNote{Type: "command.output", RunID: 2, Offset: int64(i + 1), Payload: payload})
		line, ok := v.nowLine(120)
		if !ok || strings.TrimSpace(ansi.Strip(line)) != text {
			t.Fatalf("after chunk %q the now-line is %q (shown %v)", text, ansi.Strip(line), ok)
		}
	}
	if len(d.records) != 0 {
		t.Errorf("the live chunks leaked into the displayed attempt's records: %d", len(d.records))
	}
}

func TestNowLineIsOnlyForARunningTask(t *testing.T) {
	for _, state := range []string{
		stateQueued, statePaused, stateAwaitingChildren, stateBlocked,
		stateAwaitingInput, stateAwaitingGate, stateAborted, stateDone, stateArchived,
	} {
		m, v := runningWorkspace(t, "said something")
		v.detail.task.State = state
		if line, ok := v.nowLine(120); ok {
			t.Errorf("%s: now-line %q, want none", state, ansi.Strip(line))
		}
		if strings.Contains(ansi.Strip(content(m)), statusGlyph+" said something") {
			t.Errorf("%s: the screen still draws the now-line", state)
		}
	}
	// The frames that hide the now-line keep the fact.
	for _, state := range []string{stateQueued, statePaused} {
		_, v := runningWorkspace(t, "said something")
		v.detail.task.State = state
		if out := ansi.Strip(strings.Join(v.overviewLines(120, 40), "\n")); !strings.Contains(out, "latest status") {
			t.Errorf("%s: the Overview lost latest status:\n%s", state, out)
		}
	}
}

// With no colour at all the breadcrumb's separators and the now-line's glyph
// still carry their meaning.
func TestHeaderAndNowLineReadWithoutColour(t *testing.T) {
	m, _ := runningWorkspace(t, "halfway there")
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ASCII}
	if _, err := w.Write([]byte(content(m))); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(ansi.Strip(buf.String()), "\n")
	if !strings.Contains(lines[0], "Board"+crumbSep+"#4"+crumbSep+"Overview") {
		t.Errorf("breadcrumb without colour = %q", lines[0])
	}
	if !strings.Contains(lines[1], statusGlyph+" halfway there") {
		t.Errorf("now-line without colour = %q", lines[1])
	}
}
