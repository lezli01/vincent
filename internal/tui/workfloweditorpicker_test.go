package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The closed-set rows and the two validated ones (claim 6).

// agentDefinition is one agent step carrying the three host-state rows.
func agentDefinition() apiclient.WorkflowDefinition {
	def := promptDefinition("do the thing")
	def.Definition.Steps[0].Agent = "claude"
	def.Definition.Steps[0].Model = "sonnet"
	return def
}

// An agent row opens the shared picker rather than a blank text field, and
// starts the fetch that fills its catalog in.
func TestEditorAgentRowOpensThePicker(t *testing.T) {
	w := editorFixtureWith(t, agentDefinition())
	w.editor.path = "steps[0]"
	w.editor.rebuild()
	w.editor.cursor = editorRowIndex(t, w, "agent")
	_, cmd := w.updateKey(registryKey(t, "enter"))
	if cmd == nil {
		t.Error("the picker did not start the agent fetch")
	}
	pick, ok := w.editor.overlay.(*wfEditorPicker)
	if !ok {
		t.Fatalf("overlay = %T, want the picker", w.editor.overlay)
	}
	if pick.control != apiclient.WorkflowControlAgent {
		t.Errorf("control = %q", pick.control)
	}
	if !w.capturesInput() {
		t.Error("the picker does not hold the keyboard")
	}
	// The catalog lands and the options become the host's adapters.
	agents := apiclient.Agents{
		{Name: "claude", Available: true, Version: "1.2", Models: []apiclient.AgentOption{{Value: "opus", Source: "cli"}}},
		{Name: "codex", Available: false, Error: "not found"},
	}
	w.updateEditorMsg(wfEditorAgentsMsg{
		key: w.editor.key, control: apiclient.WorkflowControlAgent, agents: agents,
	})
	labels := map[string]bool{}
	for _, opt := range pick.picker.options {
		labels[opt.value] = true
	}
	if !labels["claude"] || !labels["codex"] {
		t.Errorf("the catalog did not reach the picker: %+v", pick.picker.options)
	}
	// esc closes it without writing.
	if overlay, _ := pick.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); overlay != nil {
		t.Error("esc did not close the picker")
	}
}

// A model row's catalog is the step's own adapter's, resolved from the sibling
// `agent:` row rather than from a second fetch.
func TestEditorModelRowUsesTheStepsAdapter(t *testing.T) {
	w := editorFixtureWith(t, agentDefinition())
	w.editor.path = "steps[0]"
	w.editor.rebuild()
	if got := w.editor.rowAgent("steps[0].model"); got != "claude" {
		t.Errorf("rowAgent = %q, want the step's own agent", got)
	}
	agents := apiclient.Agents{
		{Name: "claude", Available: true, Models: []apiclient.AgentOption{{Value: "opus", Source: "cli"}}},
		{Name: "codex", Available: true, Models: []apiclient.AgentOption{{Value: "gpt", Source: "curated"}}},
	}
	got := map[string]bool{}
	for _, opt := range wfCatalogOptions(agents, "claude", true) {
		got[opt.value] = true
	}
	if !got["opus"] || got["gpt"] {
		t.Errorf("a resolved adapter's catalog is not its own: %v", got)
	}
	// With no adapter chosen the union is offered, because a `model:` above an
	// unset `agent:` still has to be typeable.
	union := map[string]bool{}
	for _, opt := range wfCatalogOptions(agents, "", true) {
		union[opt.value] = true
	}
	if !union["opus"] || !union["gpt"] {
		t.Errorf("the unresolved catalog is not the union: %v", union)
	}
}

// An int or a duration is checked in the row: an obvious mistake should not
// need a daemon round trip, and the daemon stays the authority on the rest.
func TestEditorValidatesIntAndDurationRows(t *testing.T) {
	for _, tc := range []struct {
		control, value string
		bad            bool
	}{
		{apiclient.WorkflowControlInt, "3", false},
		{apiclient.WorkflowControlInt, "0", false},
		{apiclient.WorkflowControlInt, "two", true},
		{apiclient.WorkflowControlInt, "1.5", true},
		{apiclient.WorkflowControlDuration, "30s", false},
		{apiclient.WorkflowControlDuration, "1h30m", false},
		{apiclient.WorkflowControlDuration, "2 minutes", true},
		{apiclient.WorkflowControlDuration, "5", true},
		{apiclient.WorkflowControlString, "anything", false},
	} {
		err := validateRowValue(tc.control, tc.value)
		if (err != "") != tc.bad {
			t.Errorf("%s %q: err %q, want bad=%v", tc.control, tc.value, err, tc.bad)
		}
	}
}

// The refused value stays on its row beside the error — the behaviour §15
// requires of a daemon refusal, kept for the local one.
func TestEditorRefusedDurationStaysOnTheRow(t *testing.T) {
	w := editorFixtureWith(t, agentDefinition())
	w.editor.path = "steps[0]"
	w.editor.rebuild()
	row := editorRowIndex(t, w, "timeout")
	w.editor.cursor = row
	w.updateKey(registryKey(t, "enter"))
	if w.editor.input == nil {
		t.Fatal("a duration row did not open a field")
	}
	w.editor.input.SetValue("2 minutes")
	_, cmd := w.updateEditorInput(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("an unparseable duration was sent to the daemon anyway")
	}
	if w.editor.err == "" {
		t.Error("nothing said why the value was refused")
	}
	if w.editor.rows[row].value != "2 minutes" {
		t.Errorf("the refused value left the row: %q", w.editor.rows[row].value)
	}
}
