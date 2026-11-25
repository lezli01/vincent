package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// outputWithLines is a task view on Output whose feed holds lines.
func outputWithLines(t *testing.T, lines ...string) *taskView {
	t.Helper()
	v := cardView(t, apiclient.TaskDetail{Task: apiclient.Task{ID: 4, State: stateDone, StepTotal: 1}})
	v.setTab(taskTabOutput)
	v.detail.feedLines = lines
	return v
}

func typeQuery(t *taskView, q string) {
	for _, r := range q {
		t.updateKey(keyPress(string(r)))
	}
	t.updateKey(namedKey("enter"))
}

func init() {
	panelKeyProbes[ctxOutput]["/"] = func(t *testing.T) {
		v := outputWithLines(t, "alpha", "beta")
		v.updateKey(registryKey(t, "/"))
		if !v.detail.find.typing {
			t.Fatal("/ on Output did not open the search")
		}
	}
	panelKeyProbes[ctxDiff]["/"] = func(t *testing.T) {
		v := outputWithLines(t)
		v.setTab(taskTabDiff)
		v.updateKey(registryKey(t, "/"))
		if !v.detail.diff.find.typing {
			t.Fatal("/ on Diff did not open the search")
		}
	}
	panelKeyProbes[ctxDiff]["alt+down"] = func(t *testing.T) {
		p := loadedDiff(t, twoFileDiff)
		p.foldAll(true)
		p.render(80, 4)
		p.updateKey(registryKey(t, "alt+down"))
		if p.vp.YOffset() != 1 {
			t.Fatalf("alt+down left the diff at line %d", p.vp.YOffset())
		}
	}
	panelKeyProbes[ctxDiff]["ctrl+r"] = func(t *testing.T) {
		v := outputWithLines(t)
		v.detail.client = offlineClient()
		v.setTab(taskTabDiff)
		v.detail.diff.fetching = false
		if cmd := v.updateKey(registryKey(t, "ctrl+r")); cmd == nil {
			t.Fatal("ctrl+r did not read the diff again")
		}
	}
	findProbe := func(key string, check func(*testing.T, *taskView)) func(*testing.T) {
		return func(t *testing.T) {
			v := outputWithLines(t, "one needle", "two", "three needle")
			v.updateKey(registryKey(t, "/"))
			typeQuery(v, "needle")
			v.updateKey(registryKey(t, key))
			check(t, v)
		}
	}
	panelKeyProbes[ctxPaneFind] = map[string]func(*testing.T){
		"n": findProbe("n", func(t *testing.T, v *taskView) {
			if at, _ := v.detail.find.line(); at != 2 {
				t.Fatalf("n landed on line %d, want the second match", at)
			}
		}),
		"N": findProbe("N", func(t *testing.T, v *taskView) {
			if at, _ := v.detail.find.line(); at != 2 {
				t.Fatalf("N from the first match landed on line %d, want the last (wrapping)", at)
			}
		}),
		"/": findProbe("/", func(t *testing.T, v *taskView) {
			if !v.detail.find.typing || v.detail.find.buf.String() != "needle" {
				t.Fatal("/ during a search did not reopen it with the query")
			}
		}),
		"esc": findProbe("esc", func(t *testing.T, v *taskView) {
			if v.detail.find.active() {
				t.Fatal("esc did not end the search")
			}
		}),
	}
}

// #1074: a search on Output lands on its matches and gives n and N back
// when it ends.
func TestOutputSearchWalksMatches(t *testing.T) {
	v := outputWithLines(t, "one needle", "two", "three NEEDLE")
	v.updateKey(keyPress("/"))
	if v.bindingContext() != ctxPaneFind {
		t.Fatalf("context = %v while typing, want the search's", v.bindingContext())
	}
	typeQuery(v, "needle")
	if got := len(v.detail.find.hits); got != 2 {
		t.Fatalf("%d matches, want 2 (case-insensitive)", got)
	}
	v.updateKey(keyPress("n"))
	if at, _ := v.detail.find.line(); at != 2 {
		t.Fatalf("n landed on line %d", at)
	}
	v.updateKey(namedKey("esc"))
	if v.bindingContext() != ctxOutput {
		t.Fatalf("context = %v after esc, want Output's", v.bindingContext())
	}
}

// #1074: a search on the Diff opens every file to look inside it.
func TestDiffSearchOpensFiles(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)
	p.render(100, 30)
	p.find.open()
	for _, r := range "+" {
		p.findKey(keyPress(string(r)))
	}
	p.findKey(namedKey("enter"))
	if len(p.find.hits) == 0 {
		t.Fatal("a search for + found nothing in a diff with added lines")
	}
	if out := ansi.Strip(p.render(100, 30)); !strings.Contains(out, "match 1/") {
		t.Fatalf("the diff draws no search line:\n%s", out)
	}
}

// builtOutput marks the Output feed as already built for width, so a render
// draws feedLines rather than rebuilding them from transcript records.
func builtOutput(d *detail, width int) {
	d.outputDirty = false
	d.builtWidth, d.width = width, width
	d.builtLinks, d.builtLevel = d.links.get(), d.level.get()
	d.vp.SetContent(strings.Join(d.feedLines, "\n"))
}

// #1154: with a fan-out lane selected the Output tab draws the lane's pane,
// so `/` searches the lane's pane — the parent's search was invisible.
func TestOutputSearchOnALaneSearchesTheLane(t *testing.T) {
	v := outputWithLines(t, "parent line")
	lane := newTestDetail(t)
	lane.taskID = 9
	lane.feedLines = []string{"lane one", "lane pin"}
	builtOutput(lane, 100)
	v.lanes = []apiclient.Task{{ID: 9, State: stateDone}}
	v.laneSel, v.laneDetail = 0, lane

	v.updateKey(keyPress("/"))
	if !v.capturesInput() {
		t.Fatal("typing a lane's search does not hold the global keys back")
	}
	typeQuery(v, "pin")
	if v.detail.find.active() {
		t.Fatal("/ on a lane opened the parent's invisible search")
	}
	if at, ok := lane.find.line(); !ok || at != 1 {
		t.Fatalf("the lane's search is at %d (%v), want its line 1", at, ok)
	}
	if v.bindingContext() != ctxPaneFind {
		t.Fatalf("context = %v, want the search's", v.bindingContext())
	}
	if out := ansi.Strip(v.renderOutput(100, 20)); !strings.Contains(out, "match 1/1") {
		t.Fatalf("the lane's search line is not on screen:\n%s", out)
	}
	v.updateKey(namedKey("esc"))
	if lane.find.active() {
		t.Fatal("esc did not end the lane's search")
	}
}

// #1154: esc ends the search and takes its highlight with it.
func TestOutputSearchEscClearsHighlight(t *testing.T) {
	v := outputWithLines(t, "one needle", "two")
	builtOutput(v.detail, 100)
	plain := v.detail.vp.GetContent()
	v.updateKey(keyPress("/"))
	typeQuery(v, "needle")
	v.detail.renderOutputPane(10)
	if v.detail.vp.GetContent() == plain {
		t.Fatal("the committed search drew no highlight (the test proves nothing)")
	}
	v.updateKey(namedKey("esc"))
	v.detail.renderOutputPane(10)
	if got := v.detail.vp.GetContent(); got != plain {
		t.Fatalf("the highlight outlived esc:\n%q\nwant\n%q", got, plain)
	}
}

// #1154: the Diff's search too.
func TestDiffSearchEscClearsHighlight(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)
	p.foldAll(true)
	p.built = false
	p.render(100, 30)
	plain := p.vp.GetContent()
	p.find.open()
	p.findKey(keyPress("+"))
	p.findKey(namedKey("enter"))
	p.render(100, 30)
	if p.vp.GetContent() == plain {
		t.Fatal("the committed search drew no highlight (the test proves nothing)")
	}
	p.findKey(namedKey("esc"))
	p.render(100, 30)
	if got := p.vp.GetContent(); got != plain {
		t.Fatal("the highlight outlived esc")
	}
}

// #1154: a search belongs to the task it was typed on; opening another
// task starts without one, on Output and on Diff.
func TestSearchDoesNotFollowIntoTheNextTask(t *testing.T) {
	v := outputWithLines(t, "one needle")
	v.updateKey(keyPress("/"))
	typeQuery(v, "needle")
	v.setTab(taskTabDiff)
	v.detail.diff.find.open()
	v.detail.diff.find.query = "x"
	v.detail.open(5, stateDone)
	if v.detail.find.active() {
		t.Fatal("the Output search followed into the next task")
	}
	if v.detail.diff.find.active() {
		t.Fatal("the Diff search followed into the next task")
	}
}

// #1165: the find bar names the filter key in force, not a literal `/` — the
// search is opened by an operation a user may move.
func TestPaneFindBarNamesTheReboundKey(t *testing.T) {
	withKeymap(t, map[string]string{"filter": "f17"})
	var f paneFind
	f.open()
	if got := ansi.Strip(f.bar(120, "")); !strings.HasPrefix(got, "f17") {
		t.Fatalf("the typing bar does not lead with the key in force: %q", got)
	}
	f.typing, f.query = false, "beta"
	got := ansi.Strip(f.bar(120, ""))
	if !strings.Contains(got, "f17 again") || strings.Contains(got, "/ again") {
		t.Fatalf("the bar does not offer the key in force: %q", got)
	}
}
