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
