package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The Workflows screen's wide layout (task 150.7). Pure model tests, one
// property each (task 129 decision 7).

func wideWorkflows(t *testing.T, width int) *workflowsView {
	t.Helper()
	w := newWorkflowsView()
	w.client = offlineClient()
	w.project = projectSel{id: 1, name: "api"}
	w.update(tea.WindowSizeMsg{Width: width, Height: 44})
	w.global = []apiclient.WorkflowEntry{
		{Name: "adhoc", Scope: "builtin", Description: "one agent step"},
		{Name: "nightly", Scope: "global", File: "/config/workflows/nightly.yaml"},
		{Name: "review", Scope: "global", File: "/config/workflows/review.yaml"},
	}
	w.own = []apiclient.WorkflowEntry{
		{Name: "review", Scope: scopeProject, File: "/repo/.vincent/workflows/review.yaml", Description: "the project's own review"},
		{Name: "ship", Scope: scopeProject, File: "/repo/.vincent/workflows/ship.yaml"},
	}
	w.loaded = true
	w.render(width, 44)
	return w
}

func currentName(t *testing.T, w *workflowsView) string {
	t.Helper()
	line, ok := w.currentLine()
	if !ok {
		t.Fatal("nothing is selected")
	}
	return line.entry.Scope + "/" + line.entry.Name
}

func TestWorkflowsWideGroupsTheRegistryByScope(t *testing.T) {
	w := wideWorkflows(t, 180)
	out := w.render(180, 44)
	assertFills(t, out, 180, 44)
	plain := ansi.Strip(out)
	last := -1
	for _, want := range []string{"This project", "Global", "Built-in"} {
		at := strings.Index(plain, want)
		if at <= last {
			t.Fatalf("%q is missing or out of order:\n%s", want, plain)
		}
		last = at
	}
}

// The arrows walk the entries in the order they are drawn.
func TestWorkflowsWideArrowsFollowTheGroups(t *testing.T) {
	w := wideWorkflows(t, 180)
	want := []string{"project/review", "project/ship", "global/nightly", "global/review", "builtin/adhoc"}
	for i, name := range want {
		if got := currentName(t, w); got != name {
			t.Fatalf("stop %d is %s, want %s", i, got, name)
		}
		w.updateKey(keyPress("down"))
	}
}

func TestWorkflowsDetailsNeedTheWidth(t *testing.T) {
	if out := ansi.Strip(wideWorkflows(t, 180).render(180, 44)); !strings.Contains(out, "Details") {
		t.Errorf("a wide terminal has no details:\n%s", out)
	}
	narrow := wideWorkflows(t, wfDetailsMinW-1)
	out := narrow.render(wfDetailsMinW-1, 44)
	assertFills(t, out, wfDetailsMinW-1, 44)
	if strings.Contains(ansi.Strip(out), "Details") {
		t.Errorf("%d columns drew the details:\n%s", wfDetailsMinW-1, ansi.Strip(out))
	}
}

// A short terminal scrolls the registry to keep the selected entry on screen.
func TestWorkflowsWideShortTerminalKeepsTheSelection(t *testing.T) {
	w := wideWorkflows(t, 180)
	for range 4 {
		w.updateKey(keyPress("down"))
	}
	out := w.render(180, 10)
	assertFills(t, out, 180, 10)
	if !strings.Contains(ansi.Strip(out), "▸ adhoc") {
		t.Errorf("the selected entry scrolled away:\n%s", ansi.Strip(out))
	}
}

// Crossing the breakpoint reorders the list, and the selection stays put.
func TestWorkflowsResizeKeepsTheSelection(t *testing.T) {
	w := wideWorkflows(t, 180)
	w.updateKey(keyPress("down"))
	w.updateKey(keyPress("down"))
	want := currentName(t, w)
	w.update(tea.WindowSizeMsg{Width: 100, Height: 44})
	if got := currentName(t, w); got != want {
		t.Fatalf("narrowing moved the selection from %s to %s", want, got)
	}
	w.update(tea.WindowSizeMsg{Width: 180, Height: 44})
	if got := currentName(t, w); got != want {
		t.Fatalf("widening moved the selection from %s to %s", want, got)
	}
}

// Passing over an entry reads nothing: only where the selection rests is read.
func TestWorkflowsPreviewWaitsForTheSelectionToRest(t *testing.T) {
	w := wideWorkflows(t, 180)
	passed, _ := w.currentLine()
	key := passed.key()
	w.updateKey(keyPress("down"))
	if w.previewDue(key) != nil || w.preview != nil {
		t.Fatal("an entry the selection moved off was read")
	}
	rest, _ := w.currentLine()
	if w.previewArmed != rest.key() {
		t.Fatal("the entry under the selection armed no read")
	}
	if w.previewCmd() != nil {
		t.Fatal("a second read was armed while the first waits")
	}
	if w.previewDue(rest.key()) == nil || w.preview == nil {
		t.Fatal("the rested entry was not read")
	}
}

func landPreview(t *testing.T, w *workflowsView) {
	t.Helper()
	line, _ := w.currentLine()
	w.previewArmed = wfResolveKey{}
	if w.previewDue(line.key()) == nil {
		t.Fatal("the preview was not read")
	}
	w.applyDefinition(workflowDefinitionMsg{key: line.key(), def: graphDefinition()})
	if !w.preview.loaded {
		t.Fatal("the definition did not land")
	}
}

// `g` takes the picture already on screen, and esc hands the keyboard back.
func TestWorkflowsGraphTakesThePreviewWithoutReadingAgain(t *testing.T) {
	w := wideWorkflows(t, 180)
	landPreview(t, w)
	if _, cmd := w.updateKey(keyPress("g")); cmd != nil || w.graph != w.preview {
		t.Fatal("g did not move the keyboard into the preview")
	}
	if got := w.bindingContext(); got != ctxWorkflowGraph {
		t.Fatalf("the keyboard is in %q", got)
	}
	w.updateKey(keyPress("esc"))
	if w.graph != nil || w.preview == nil {
		t.Fatal("esc did not return to the list with the preview still drawn")
	}
}

// With the steps always on screen, enter goes on into the graph.
func TestWorkflowsWideEnterExploresTheGraph(t *testing.T) {
	w := wideWorkflows(t, 180)
	landPreview(t, w)
	w.updateKey(keyPress("enter"))
	if w.graph == nil || w.expanded {
		t.Fatalf("enter left graph=%v expanded=%v", w.graph != nil, w.expanded)
	}
}

func TestWorkflowsWideClickSelectsAnEntry(t *testing.T) {
	w := wideWorkflows(t, 180)
	hit := w.hits[len(w.hits)-1]
	w.update(tea.MouseClickMsg{X: hit.x0, Y: hit.y0, Button: tea.MouseLeft})
	if got := currentName(t, w); got != "builtin/adhoc" {
		t.Fatalf("the click selected %s", got)
	}
}

// An entry that does not parse says why where its graph would be.
func TestWorkflowsWideInvalidEntryExplainsItself(t *testing.T) {
	w := wideWorkflows(t, 180)
	w.own[0].Errors = []apiclient.WorkflowFinding{{Line: 3, Message: "unknown step type"}}
	out := ansi.Strip(w.render(180, 44))
	for _, want := range []string{"does not parse", "line 3: unknown step type"} {
		if !strings.Contains(out, want) {
			t.Errorf("the screen is missing %q:\n%s", want, out)
		}
	}
}
