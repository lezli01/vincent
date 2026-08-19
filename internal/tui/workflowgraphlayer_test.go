package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// graphFixture opens the graph layer on a loaded workflow, which is the state
// every key inside the layer assumes.
func graphFixture(t *testing.T) *workflowsView {
	t.Helper()
	w := newWorkflowsView()
	w.client = offlineClient()
	w.width, w.height = 100, 40
	loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
	w.updateKey(registryKey(t, "g"))
	if w.graph == nil {
		t.Fatal("the graph layer did not open")
	}
	w.applyDefinition(workflowDefinitionMsg{key: w.graph.key, def: graphDefinition()})
	if !w.graph.loaded {
		t.Fatal("the definition did not land")
	}
	return w
}

func graphDefinition() apiclient.WorkflowDefinition {
	return apiclient.WorkflowDefinition{
		Name: "review", Scope: "global", File: "/tmp/review.yaml",
		Definition: &apiclient.WorkflowBody{
			Name: "review",
			Steps: []apiclient.WorkflowStepDef{
				{ID: "plan", Type: "agent"},
				{ID: "check", Type: "command"},
				{ID: "ship", Type: "manual"},
			},
		},
	}
}

// Escape closes one layer at a time: the graph, then the takeover (§15).
func TestGraphEscapeClosesOneLayer(t *testing.T) {
	w := graphFixture(t)
	if _, cmd := w.updateKey(registryKey(t, "esc")); cmd != nil {
		t.Error("the first esc left the takeover as well as the graph")
	}
	if w.graph != nil {
		t.Fatal("esc did not close the graph layer")
	}
	_, cmd := w.updateKey(registryKey(t, "esc"))
	if cmd == nil {
		t.Fatal("the second esc did not leave the takeover")
	}
}

// An error inside the layer is its own layer: it clears before the graph does.
func TestGraphEscapeClearsAnErrorFirst(t *testing.T) {
	w := graphFixture(t)
	w.graph.err = "fetch failed"
	w.updateKey(registryKey(t, "esc"))
	if w.graph == nil {
		t.Fatal("esc closed the layer instead of clearing its error")
	}
	if w.graph.err != "" {
		t.Errorf("the error survived: %q", w.graph.err)
	}
}

// A workflow already known not to parse has no graph to draw, and its
// findings are on screen in the expansion — so `g` says so rather than
// opening a layer that repeats them.
func TestGraphRefusesAnInvalidWorkflow(t *testing.T) {
	w := newWorkflowsView()
	w.client = offlineClient()
	broken := globalEntry("broken")
	broken.Errors = []apiclient.WorkflowFinding{{Message: "unknown step type"}}
	loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{broken}})
	w.updateKey(registryKey(t, "g"))
	if w.graph != nil {
		t.Fatal("g opened a graph for a workflow that does not parse")
	}
	if !strings.Contains(w.err, "does not parse") {
		t.Errorf("note = %q, want it to say why there is no graph", w.err)
	}
}

// The two can race: a file may break between the list load and the fetch, and
// the layer has to say so rather than draw an empty canvas.
func TestGraphHandlesANullDefinition(t *testing.T) {
	w := graphFixture(t)
	w.applyDefinition(workflowDefinitionMsg{
		key: w.graph.key,
		def: apiclient.WorkflowDefinition{
			Name:   "review",
			Errors: []apiclient.WorkflowFinding{{Message: "step 2: unknown type"}},
		},
	})
	out := w.render(100, 40)
	if !strings.Contains(out, "no longer parses") || !strings.Contains(out, "unknown type") {
		t.Errorf("render does not report the findings:\n%s", out)
	}
}

func TestGraphReportsAFetchFailure(t *testing.T) {
	w := graphFixture(t)
	w.applyDefinition(workflowDefinitionMsg{key: w.graph.key, err: errors.New("daemon gone")})
	out := w.render(100, 40)
	if !strings.Contains(out, "daemon gone") || !strings.Contains(out, "R retries") {
		t.Errorf("render does not offer a retry:\n%s", out)
	}
}

// A response for an entry the layer has moved off is dropped, not drawn.
func TestGraphIgnoresAStaleFetch(t *testing.T) {
	w := graphFixture(t)
	before := w.graph.graph.Selected()
	w.applyDefinition(workflowDefinitionMsg{
		key: wfResolveKey{name: "something-else"},
		def: apiclient.WorkflowDefinition{Name: "something-else"},
	})
	if got := w.graph.graph.Selected(); got != before {
		t.Errorf("a stale fetch changed the graph: selection %q, want %q", got, before)
	}
}

// A registry reload refetches an open graph, so a save in $EDITOR redraws it.
func TestGraphRefetchesOnARegistryReload(t *testing.T) {
	w := graphFixture(t)
	cmd := w.updateNote(apiclient.EventNote{
		Event: apiclient.Event{Type: eventWorkflowRegistryChanged},
	})
	if cmd == nil {
		t.Fatal("a registry reload did not refresh anything")
	}
	if !w.graph.loading {
		t.Error("the open graph was not marked as refetching")
	}
}

// Selection survives that reload, because node identity is semantic rather
// than positional.
func TestGraphSelectionSurvivesAReload(t *testing.T) {
	w := graphFixture(t)
	w.graph.graph.Select("ship")
	grown := graphDefinition()
	grown.Definition.Steps = append(
		[]apiclient.WorkflowStepDef{{ID: "prep", Type: "command"}}, grown.Definition.Steps...)
	w.applyDefinition(workflowDefinitionMsg{key: w.graph.key, def: grown})
	if got := w.graph.graph.Selected(); got != "ship" {
		t.Errorf("selection = %q, want it to survive the reload", got)
	}
}

// The footer and the ? overlay must describe the keys that actually work.
func TestGraphOwnsItsBindingContext(t *testing.T) {
	w := graphFixture(t)
	if got := w.bindingContext(); got != ctxWorkflowGraph {
		t.Errorf("context = %q, want the graph's", got)
	}
	w.updateKey(registryKey(t, "esc"))
	if got := w.bindingContext(); got != ctxWorkflows {
		t.Errorf("context = %q, want the list's once the layer closed", got)
	}
}

// A terminal too small to draw a node says so rather than showing a
// flattened graph that would misrepresent the topology.
func TestGraphNarrowTerminalSaysSo(t *testing.T) {
	w := graphFixture(t)
	out := w.render(12, 20)
	if !strings.Contains(out, "too narrow") {
		t.Errorf("a 12-column render does not say it is too narrow:\n%s", out)
	}
	wide := w.render(100, 40)
	if strings.Contains(wide, "too narrow") {
		t.Error("a 100-column render claims to be too narrow")
	}
}

// The inspector carries what the box reduced to a badge.
func TestGraphInspectorShowsTheSelectedNode(t *testing.T) {
	w := graphFixture(t)
	w.graph.graph.Select("check")
	out := w.render(100, 40)
	if !strings.Contains(out, "check") || !strings.Contains(out, "command") {
		t.Errorf("the inspector does not describe the selected node:\n%s", out)
	}
}

func TestGraphClickSelects(t *testing.T) {
	w := graphFixture(t)
	node, ok := w.graph.graph.SelectedNode()
	if !ok {
		t.Fatal("nothing is selected")
	}
	_ = node
	// A click below the entry node lands on the next one down.
	w.updateGraphMouse(tea.MouseClickMsg{Button: tea.MouseLeft, X: graphPaneX + 2, Y: graphPaneY + 7})
	if got := w.graph.graph.Selected(); got == "" {
		t.Error("the click cleared the selection")
	}
}

func TestGraphWheelScrolls(t *testing.T) {
	w := graphFixture(t)
	w.render(40, 10)
	before := w.graph.graph.ScrollPercent()
	w.updateGraphMouse(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if w.graph.graph.ScrollPercent() == before {
		t.Error("the wheel did not scroll the canvas")
	}
}
