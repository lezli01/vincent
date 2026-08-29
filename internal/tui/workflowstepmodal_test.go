package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The step-detail modal (task 053): `enter` opens the selected node in full,
// `esc` closes it back to the graph with that node still selected, and the
// picture underneath does not change while it is shut.

// detailedDefinition is a workflow whose first step carries more than the
// two-line strip can hold — a long name, a multi-line prompt, an env block, a
// check and a timeout — beside a value it inherits from `defaults`.
func detailedDefinition() apiclient.WorkflowDefinition {
	timeout := "45m"
	def := graphDefinition()
	def.Definition.Description = "review a change end to end"
	def.Definition.Defaults = apiclient.WorkflowDefaults{Agent: "claude", Model: "opus"}
	def.Definition.Fields = []apiclient.WorkflowField{{Name: "branch", Type: "string", Required: true}}
	def.Definition.Steps[0] = apiclient.WorkflowStepDef{
		ID:      "plan",
		Type:    "agent",
		Name:    "Run the integration suite on the merged branch",
		Model:   "sonnet",
		Prompt:  strings.Repeat("read every line of the diff and say what it changes. ", 12),
		Check:   "go build ./...",
		Timeout: &timeout,
	}
	def.Definition.Steps[1] = apiclient.WorkflowStepDef{
		ID:   "check",
		Type: "command",
		Run:  "go test ./...\ngo vet ./...\ngo run mage.go lint",
		Env:  map[string]string{"CI": "1"},
	}
	return def
}

func modalFixture(t *testing.T) *workflowsView {
	t.Helper()
	w := graphFixture(t)
	w.applyDefinition(workflowDefinitionMsg{key: w.graph.key, def: detailedDefinition()})
	w.render(100, 40)
	w.updateKey(registryKey(t, "enter"))
	if w.graph.modal == nil {
		t.Fatal("enter did not open the step detail")
	}
	return w
}

// Escape closes one layer at a time, and the modal is now the innermost:
// modal, then graph, then the takeover (§15).
func TestStepModalEscapeClosesOneLayer(t *testing.T) {
	w := modalFixture(t)
	selected := w.graph.graph.Selected()

	if _, cmd := w.updateKey(registryKey(t, "esc")); cmd != nil {
		t.Error("the first esc left more than the modal")
	}
	if w.graph == nil || w.graph.modal != nil {
		t.Fatal("esc did not close the modal back to the graph")
	}
	if got := w.graph.graph.Selected(); got != selected {
		t.Errorf("selection = %q, want %q — the node stays selected", got, selected)
	}
	w.updateKey(registryKey(t, "esc"))
	if w.graph != nil {
		t.Fatal("the second esc did not close the graph")
	}
	if _, cmd := w.updateKey(registryKey(t, "esc")); cmd == nil {
		t.Fatal("the third esc did not leave the takeover")
	}
}

// The graph is unchanged while the modal is shut: the canvas, the node boxes,
// the edges and the inspector strip all render byte for byte as they did
// before the modal existed, which is what makes the strip's "untouched, bytes
// included" decision checkable.
func TestGraphRendersIdenticallyWithNoModalOpen(t *testing.T) {
	w := graphFixture(t)
	w.applyDefinition(workflowDefinitionMsg{key: w.graph.key, def: detailedDefinition()})
	before := w.render(100, 40)

	w.updateKey(registryKey(t, "enter"))
	opened := w.render(100, 40)
	if opened == before {
		t.Fatal("opening the modal changed nothing on screen")
	}

	w.updateKey(registryKey(t, "esc"))
	if after := w.render(100, 40); after != before {
		t.Errorf("closing the modal did not restore the layer byte for byte:\n--- before ---\n%s\n--- after ---\n%s",
			before, after)
	}
}

// Nothing in the modal is truncated: a long prompt and a multi-line `run:`
// body wrap inside it, which is the whole reason it exists.
func TestStepModalWrapsLongValues(t *testing.T) {
	w := modalFixture(t)
	lines := w.modalLines(60)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "prompt") {
		t.Fatalf("the modal does not show the prompt:\n%s", joined)
	}
	for i, line := range lines {
		if ansi.StringWidth(line) > 60 {
			t.Errorf("line %d is %d columns wide, past the popup's %d: %q",
				i, ansi.StringWidth(line), 60, line)
		}
	}

	w.graph.graph.Select("check")
	w.openStepModal()
	run := strings.Join(w.modalLines(60), "\n")
	for _, want := range []string{"go test ./...", "go vet ./...", "go run mage.go lint", "CI=1"} {
		if !strings.Contains(run, want) {
			t.Errorf("the modal does not show %q:\n%s", want, run)
		}
	}
}

// Content taller than the popup scrolls rather than being cut.
func TestStepModalScrolls(t *testing.T) {
	w := modalFixture(t)
	// A short terminal is what makes the popup shorter than its content: the
	// modal is capped so the graph it was opened from stays visible around it.
	w.render(100, 16)
	m := w.graph.modal
	if m.vp.TotalLineCount() <= m.vp.Height() {
		t.Fatalf("the fixture fits in %d rows; it cannot prove scrolling", m.vp.Height())
	}
	before := m.vp.View()
	w.updateKey(registryKey(t, "down"))
	if w.graph.modal.vp.View() == before {
		t.Error("down did not scroll the modal")
	}
	if got := w.graph.graph.Selected(); got != "plan" {
		t.Errorf("selection = %q — the modal owns the keyboard while it is open", got)
	}
}

// An inherited value is shown as the effective one and marked; an authored
// one is shown unmarked.
func TestStepModalMarksInheritedValues(t *testing.T) {
	w := modalFixture(t)
	out := strings.Join(w.modalLines(80), "\n")
	if !strings.Contains(out, "claude") || !strings.Contains(out, "inherited") {
		t.Errorf("the modal does not mark the inherited agent:\n%s", out)
	}
	if !strings.Contains(out, "sonnet") {
		t.Errorf("the modal does not show the authored model:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "sonnet") && strings.Contains(line, "inherited") {
			t.Errorf("the authored model is marked as inherited: %q", line)
		}
	}
}

// The workflow-level header is the context a step read on its own does not
// carry.
func TestStepModalCarriesTheWorkflowHeader(t *testing.T) {
	w := modalFixture(t)
	out := strings.Join(w.modalLines(80), "\n")
	for _, want := range []string{"review a change end to end", "branch*", "/tmp/review.yaml"} {
		if !strings.Contains(out, want) {
			t.Errorf("the modal header is missing %q:\n%s", want, out)
		}
	}
}

// A terminal below MinWidth has no node drawn and therefore none selected:
// the layer shows its hint and `enter` opens nothing (decision 8).
func TestStepModalIsSuppressedWhenTooNarrow(t *testing.T) {
	w := graphFixture(t)
	w.render(12, 20)
	w.updateKey(registryKey(t, "enter"))
	if w.graph.modal != nil {
		t.Fatal("enter opened a modal on a terminal too narrow to draw the node")
	}
	if out := w.render(12, 20); !strings.Contains(out, "too narrow") {
		t.Errorf("the narrow hint is gone:\n%s", out)
	}
}

// A refetch re-renders an open modal from the definition that landed: this is
// the edit-save-watch loop the layer's live reload is for.
func TestStepModalRedrawsOnARefetch(t *testing.T) {
	w := modalFixture(t)
	grown := detailedDefinition()
	grown.Definition.Steps[0].Prompt = "a different instruction entirely"
	w.applyDefinition(workflowDefinitionMsg{key: w.graph.key, def: grown})
	if w.graph.modal == nil {
		t.Fatal("the refetch closed a modal whose node still exists")
	}
	if out := strings.Join(w.modalLines(80), "\n"); !strings.Contains(out, "a different instruction") {
		t.Errorf("the modal still shows the old definition:\n%s", out)
	}
}

// A node that went away closes the modal back to the graph rather than
// leaving a reading of a step the file no longer has (decision 19).
func TestStepModalClosesWhenItsNodeIsGone(t *testing.T) {
	w := modalFixture(t)
	shrunk := detailedDefinition()
	shrunk.Definition.Steps = shrunk.Definition.Steps[1:]
	w.applyDefinition(workflowDefinitionMsg{key: w.graph.key, def: shrunk})
	if w.graph == nil {
		t.Fatal("the refetch closed the graph as well")
	}
	if w.graph.modal != nil {
		t.Error("the modal survived the step it was reading")
	}
}

// Every node opens something — `enter` is never inert, synthetic nodes
// included.
func TestStepModalOpensOnEveryNode(t *testing.T) {
	w := graphFixture(t)
	w.applyDefinition(workflowDefinitionMsg{key: w.graph.key, def: fanOutDefinition()})
	w.render(100, 40)
	for _, node := range []string{"plan", "spread", "#merge:spread", "#ref:spread:web", "#end"} {
		w.graph.modal = nil
		w.graph.graph.Select(node)
		if got := w.graph.graph.Selected(); got != node {
			t.Fatalf("could not select %q (on %q)", node, got)
		}
		w.updateKey(registryKey(t, "enter"))
		if w.graph.modal == nil {
			t.Errorf("enter on %q opened nothing", node)
			continue
		}
		if out := strings.Join(w.modalLines(80), "\n"); strings.TrimSpace(out) == "" {
			t.Errorf("the modal for %q is empty", node)
		}
	}
}

// The footer and the ? overlay describe the keys that actually work: the
// modal's while it is open, the graph's once it is not.
func TestStepModalOwnsItsBindingContext(t *testing.T) {
	w := modalFixture(t)
	if got := w.bindingContext(); got != ctxWorkflowStep {
		t.Errorf("context = %q, want the modal's", got)
	}
	w.updateKey(registryKey(t, "esc"))
	if got := w.bindingContext(); got != ctxWorkflowGraph {
		t.Errorf("context = %q, want the graph's once the modal closed", got)
	}
}

// fanOutDefinition is a fan_out with a named lane and an agent merge, which is
// what puts a merge, a collapsed reference and a group header on screen at
// once.
func fanOutDefinition() apiclient.WorkflowDefinition {
	def := graphDefinition()
	spread := apiclient.WorkflowStepDef{ID: "spread", Type: "fan_out"}
	spread.Lanes = []apiclient.WorkflowLaneDef{
		{ID: "api", Steps: []apiclient.WorkflowStepDef{{ID: "api_impl", Type: "agent"}}},
		{ID: "web", Workflow: "web-feature"},
	}
	spread.Merge = &apiclient.WorkflowMergeDef{
		OnConflict: "agent",
		Agent:      &apiclient.WorkflowStepDef{ID: "fixup", Type: "agent", Prompt: "resolve it"},
	}
	def.Definition.Steps = []apiclient.WorkflowStepDef{{ID: "plan", Type: "agent"}, spread}
	return def
}
