package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The editor's closed-set overlays (issue #320, the write half). Three of the
// controls the descriptor publishes name a set the descriptor deliberately
// does not carry: `agent`, `model` and `effort` are *host* state — which
// adapters are installed, what each one's catalog says this morning — and
// task 065 decision 3 kept them out of the schema for exactly that reason.
// They come from GET /v1/agents instead, the endpoint the new-task and
// follow-up forms already render their own pickers from.
//
// The picker itself is the TUI's shared one, wrapped rather than reimplemented:
// its filter, its free-text row and its availability notes are behaviour a
// second copy would drift from within two PRs.

// wfEditorPicker is a closed set with the keyboard. It is also what the
// structural `a` key opens on a steps list, where the set is the step types
// the served descriptor marks legal for that context.
type wfEditorPicker struct {
	picker *picker
	// control is the row control the overlay was opened for, so a catalog
	// arriving later can tell whether it is still the answer to this question.
	control string
	opened  string
	commit  func(value string) tea.Cmd
}

func newWFEditorPicker(heading, control, current string, options []pickerOption,
	allowFree bool, commit func(string) tea.Cmd,
) *wfEditorPicker {
	return &wfEditorPicker{
		picker:  newPicker(0, heading, options, allowFree, current),
		control: control,
		opened:  current,
		commit:  commit,
	}
}

// setOptions installs a catalog that arrived after the overlay opened, keeping
// whatever the cursor was on. Opening the list first and filling it in when
// the fetch lands is what keeps the keyboard responsive on a slow probe.
func (o *wfEditorPicker) setOptions(options []pickerOption) {
	current := o.opened
	if opt, ok := o.picker.current(); ok {
		current = opt.value
	}
	o.picker = newPicker(0, o.picker.heading, options, o.picker.allowFree, current)
}

func (o *wfEditorPicker) Update(msg tea.KeyPressMsg) (wfEditorOverlay, tea.Cmd) {
	res := o.picker.update(msg)
	switch {
	case res.chosen:
		return nil, o.commit(res.value)
	case res.closed:
		return nil, res.cmd
	}
	return o, res.cmd
}

func (o *wfEditorPicker) View(width, _ int) string {
	o.picker.setWidth(width)
	return strings.Join(o.picker.renderBody(), "\n")
}

// FullPane is false: a picker draws in the value column of the row it belongs
// to, which is what says which field is being answered.
func (o *wfEditorPicker) FullPane() bool { return false }

// Value is the option under the cursor — what enter would send.
func (o *wfEditorPicker) Value() string {
	if opt, ok := o.picker.current(); ok {
		return opt.value
	}
	return o.opened
}

func (o *wfEditorPicker) Dirty() bool { return o.Value() != o.opened }

// wfEditorAgentsMsg is the adapter catalog landing for an open picker.
type wfEditorAgentsMsg struct {
	key     wfResolveKey
	control string
	agent   string
	agents  apiclient.Agents
}

// editorAgentsCmd fetches the adapter catalog for an open agent/model/effort
// picker. It carries the resolved adapter with it so the reply knows which
// catalog to render without re-reading rows that may have been rebuilt.
func (w *workflowsView) editorAgentsCmd(control, agent string) tea.Cmd {
	client, e := w.client, w.editor
	if client == nil || e == nil {
		return nil
	}
	key := e.key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		agents, err := client.ListAgents(ctx, false)
		if err != nil {
			// A catalog that does not arrive leaves the picker on its free-text
			// row, which is the same offer the follow-up form makes: an
			// adapter's list is a suggestion, and the daemon accepts an
			// unknown value with a warning (§9.6).
			return wfEditorAgentsMsg{key: key, control: control, agent: agent}
		}
		return wfEditorAgentsMsg{key: key, control: control, agent: agent, agents: agents}
	}
}

// wfAgentOptions is the adapter list, with the availability notes the repair,
// new-task and follow-up pickers all carry: an adapter that is installed but
// not logged in fails every run, and saying so here saves a run that was never
// going to start (§9.5).
func wfAgentOptions(agents apiclient.Agents) []pickerOption {
	out := []pickerOption{{value: unsetMarker, label: unsetMarker + " — inherit the workflow default"}}
	for _, a := range agents {
		opt := pickerOption{value: a.Name, label: a.Name}
		switch {
		case !a.Available:
			opt.note = "⚠ unavailable: " + firstNonEmpty(a.Error, "not found")
		case a.NotAuthenticated():
			opt.note = "⚠ installed but not logged in"
		case a.Version != "":
			opt.note = a.Version
		}
		out = append(out, opt)
	}
	return out
}

// wfCatalogOptions renders a model or effort catalog. With an adapter resolved
// — from the step's own `agent:` row or from `defaults.agent` — it is that
// adapter's catalog with its provenance tags; with none, it is every adapter's,
// tagged with the adapter it came from, because a `model:` written above an
// unset `agent:` still has to be typeable.
func wfCatalogOptions(agents apiclient.Agents, agent string, models bool) []pickerOption {
	out := []pickerOption{{value: unsetMarker, label: unsetMarker + " — inherit the workflow default"}}
	catalog := func(a apiclient.Agent) []apiclient.AgentOption {
		if models {
			return a.Models
		}
		return a.Efforts
	}
	if a, ok := agents.Find(agent); ok {
		for _, o := range catalog(a) {
			out = append(out, pickerOption{value: o.Value, label: o.Value, note: o.Source})
		}
		return out
	}
	seen := map[string]bool{}
	for _, a := range agents {
		for _, o := range catalog(a) {
			if seen[o.Value] {
				continue
			}
			seen[o.Value] = true
			out = append(out, pickerOption{value: o.Value, label: o.Value, note: a.Name})
		}
	}
	return out
}

// rowAgent resolves which adapter a `model:` or `effort:` row belongs to: the
// `agent:` row of the same block if it sets one, otherwise the workflow's
// `defaults.agent`. It reads the sibling row rather than the definition so an
// agent chosen a moment ago, and not yet reloaded, is the one whose catalog is
// offered.
func (e *wfEditorLayer) rowAgent(path string) string {
	block := path
	if i := strings.LastIndex(path, "."); i >= 0 {
		block = path[:i]
	}
	for _, row := range e.rows {
		if row.field.Name == "agent" && row.path == block+".agent" {
			if row.value != "" && row.value != unsetMarker {
				return row.value
			}
			break
		}
	}
	if e.def == nil {
		return ""
	}
	return e.def.Defaults.Agent
}

// openValuePicker opens the shared picker on an agent, model or effort row and
// starts the fetch that fills it in.
func (w *workflowsView) openValuePicker(row wfEditRow) tea.Cmd {
	e := w.editor
	agent := ""
	if row.field.Control != apiclient.WorkflowControlAgent {
		agent = e.rowAgent(row.path)
	}
	e.editing = e.cursor
	// Free text is allowed for the reason §9.6 gives: a catalog is a
	// suggestion, a model shipped this morning is not in it, and the daemon
	// takes an unknown value with a warning rather than refusing it.
	e.overlay = newWFEditorPicker(row.field.Name, row.field.Control, row.value,
		wfPickerOptions(row.field.Control, agent, nil), true,
		func(v string) tea.Cmd { return w.commitRow(row, v) })
	return w.editorAgentsCmd(row.field.Control, agent)
}

// wfPickerOptions is the option list one of the three host-state controls
// draws, for a catalog that has arrived or has not.
func wfPickerOptions(control, agent string, agents apiclient.Agents) []pickerOption {
	switch control {
	case apiclient.WorkflowControlAgent:
		return wfAgentOptions(agents)
	case apiclient.WorkflowControlModel:
		return wfCatalogOptions(agents, agent, true)
	default:
		return wfCatalogOptions(agents, agent, false)
	}
}
