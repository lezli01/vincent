package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// rfRow is one line of the repair form.
type rfRow int

const (
	rfPrompt rfRow = iota
	rfAgent
	rfModel
	rfEffort
	rfRowCount
)

// repairAgentsLoadedMsg carries the adapter catalog the pickers render.
type repairAgentsLoadedMsg struct {
	taskID int64
	agents apiclient.Agents
	err    error
}

// repairEditMsg carries the text an $EDITOR session left behind.
type repairEditMsg struct {
	taskID int64
	text   string
	err    error
}

// repairForm collects what a §6 repair needs (task 025): the prompt the agent
// is given, and optionally which agent, model and effort to run it with.
//
// It is modelled on answerForm — a popup that owns the keyboard while it is
// open — and differs in the one way that matters: the answer form exists
// because the daemon is asking, so it is synthesized from task state, while
// this one exists because a human opened it and closes when they submit or
// escape.
//
// The prompt is prose. It goes to the daemon literally, never as a template
// (§8.4 renders with missingkey=error), so a `{{` typed in here is two
// characters and not a broken step.
type repairForm struct {
	taskID      int64
	blockReason string
	stepName    string

	prompt string
	agent  string
	model  string
	effort string

	cursor  rfRow
	editor  textarea.Model
	editing bool

	// agents is the catalog the pickers offer; nil until the fetch lands, and
	// nil forever when it fails. Either way every picker allows free text, so
	// the form is usable without it — the daemon accepts an unknown value
	// with a warning rather than refusing it (§9.6).
	agents apiclient.Agents
	picker *picker

	// openEditor hands the prompt to $EDITOR. Injected by the detail view,
	// which owns the exec path (editor.go), so the form itself needs no
	// terminal.
	openEditor func(text string) tea.Cmd

	err        string
	submitting bool
}

func newRepairForm(taskID int64, blockReason, stepName string) *repairForm {
	ed := textarea.New()
	ed.Placeholder = "what should the agent fix?"
	ed.Prompt = ""
	ed.ShowLineNumbers = false
	ed.DynamicHeight = true
	ed.MinHeight = 1
	ed.MaxHeight = editorLines
	ed.SetHeight(3)
	ed.SetWidth(40)
	return &repairForm{
		taskID: taskID, blockReason: blockReason, stepName: stepName, editor: ed,
	}
}

// loadAgents fetches the adapter catalog for the pickers. A failure is not
// reported to the human: the pickers fall back to free text, which is all a
// repair needs.
func (f *repairForm) loadAgents(client *apiclient.Client) tea.Cmd {
	if client == nil {
		return nil
	}
	taskID := f.taskID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		agents, err := client.ListAgents(ctx, false)
		return repairAgentsLoadedMsg{taskID: taskID, agents: agents, err: err}
	}
}

// applyAgents installs a catalog that arrived for this task.
func (f *repairForm) applyAgents(msg repairAgentsLoadedMsg) {
	if msg.taskID != f.taskID || msg.err != nil {
		return
	}
	f.agents = msg.agents
}

// applyEdit installs what an $EDITOR session produced.
func (f *repairForm) applyEdit(msg repairEditMsg) {
	if msg.taskID != f.taskID {
		return
	}
	if msg.err != nil {
		f.err = errString(msg.err)
		return
	}
	f.prompt = strings.TrimSpace(msg.text)
	f.err = ""
}

// paste types into whichever text entry is open.
func (f *repairForm) paste(text string) tea.Cmd {
	if f.picker != nil {
		return f.picker.paste(text)
	}
	if !f.editing {
		return nil
	}
	var cmd tea.Cmd
	f.editor, cmd = f.editor.Update(tea.PasteMsg{Content: text})
	return cmd
}

// update handles one key. exit=true asks the caller to close the form.
func (f *repairForm) update(msg tea.KeyPressMsg, client *apiclient.Client) (cmd tea.Cmd, exit bool) {
	if f.picker != nil {
		res := f.picker.update(msg)
		if res.chosen {
			f.setRow(rfRow(f.picker.row), res.value)
		}
		if res.closed {
			f.picker = nil
		}
		return res.cmd, false
	}
	if f.editing {
		switch msg.String() {
		case "esc":
			// esc discards, enter is a newline: the prompt is prose and may
			// well want more than one line, so the field cannot spend enter
			// on committing. ctrl+s is what leaves it.
			f.editing = false
			f.editor.Blur()
			return nil, false
		case "ctrl+s":
			f.commitPrompt()
			return nil, false
		}
		var c tea.Cmd
		f.editor, c = f.editor.Update(msg)
		return c, false
	}

	switch msg.String() {
	case "up", "k":
		f.cursor = max(f.cursor-1, 0)
	case "down", "j":
		f.cursor = min(f.cursor+1, rfRowCount-1)
	case "enter":
		f.openRow()
	case "e":
		if f.cursor == rfPrompt && f.openEditor != nil {
			return f.openEditor(f.prompt), false
		}
		f.startPrompt()
	case "ctrl+s":
		return f.submit(client), false
	case "esc":
		return nil, true
	}
	return nil, false
}

// openRow opens whatever the cursor is on: the prompt field, or the picker
// for one of the three §8.6 overrides.
func (f *repairForm) openRow() {
	if f.cursor == rfPrompt {
		f.startPrompt()
		return
	}
	f.err = ""
	switch f.cursor {
	case rfAgent:
		f.picker = newPicker(int(rfAgent), "agent", f.agentOptions(), true, f.agent)
	case rfModel:
		f.picker = newPicker(int(rfModel), "model", f.catalogOptions(f.models()), true, f.model)
	case rfEffort:
		f.picker = newPicker(int(rfEffort), "effort", f.catalogOptions(f.efforts()), true, f.effort)
	case rfPrompt, rfRowCount:
	}
}

func (f *repairForm) setRow(row rfRow, value string) {
	switch row {
	case rfAgent:
		f.agent = value
		// The model and effort catalogs belong to an adapter; keeping values
		// chosen from another one would submit a pair that never existed.
		f.model, f.effort = "", ""
	case rfModel:
		f.model = value
	case rfEffort:
		f.effort = value
	case rfPrompt, rfRowCount:
	}
}

func (f *repairForm) startPrompt() {
	f.editing = true
	f.cursor = rfPrompt
	f.editor.SetValue(f.prompt)
	f.editor.Focus()
}

func (f *repairForm) commitPrompt() {
	f.editing = false
	f.editor.Blur()
	f.prompt = strings.TrimSpace(f.editor.Value())
	if f.prompt != "" {
		f.err = ""
	}
}

// agentOptions is the adapter list, with the same availability notes the
// new-task picker carries: an adapter that is installed but not logged in
// fails every run, and saying so here saves a repair that was never going to
// start (§9.5).
func (f *repairForm) agentOptions() []pickerOption {
	out := []pickerOption{{value: "", label: "(task or workflow default)"}}
	for _, a := range f.agents {
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

func (f *repairForm) models() []apiclient.AgentOption {
	a, ok := f.agents.Find(f.agent)
	if !ok {
		return nil
	}
	return a.Models
}

func (f *repairForm) efforts() []apiclient.AgentOption {
	a, ok := f.agents.Find(f.agent)
	if !ok {
		return nil
	}
	return a.Efforts
}

// catalogOptions renders a catalog with its provenance tags, the way the
// new-task pickers do: "cli" came from the adapter itself, "curated" from
// vincent's own list and may be stale.
func (f *repairForm) catalogOptions(opts []apiclient.AgentOption) []pickerOption {
	out := []pickerOption{{value: "", label: "(task or workflow default)"}}
	for _, o := range opts {
		out = append(out, pickerOption{value: o.Value, label: o.Value, note: o.Source})
	}
	return out
}

// request is what the form would submit.
func (f *repairForm) request() apiclient.RepairInput {
	return apiclient.RepairInput{
		Prompt: f.prompt, Agent: f.agent, Model: f.model, Effort: f.effort,
	}
}

// submit posts the repair. The empty prompt is refused here as well as by the
// daemon: a round trip to be told to type something is a round trip wasted.
func (f *repairForm) submit(client *apiclient.Client) tea.Cmd {
	if f.editing {
		f.commitPrompt()
	}
	if strings.TrimSpace(f.prompt) == "" {
		f.err = "type what the agent should fix first"
		return nil
	}
	if client == nil {
		f.err = "not connected"
		return nil
	}
	f.err = ""
	f.submitting = true
	taskID, req := f.taskID, f.request()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		task, _, err := client.Repair(ctx, taskID, req)
		return actionResultMsg{
			taskID: taskID, action: apiclient.ActionRepair, task: task, err: err,
		}
	}
}

// height is how many lines the form wants at the width it will be drawn at.
func (f *repairForm) height(width int) int { return len(f.lines(width)) }

// render draws the form, windowed on the focused element when it does not
// fit.
func (f *repairForm) render(width, height int) string {
	lines := f.lines(width)
	from, to := f.focusRange(width)
	return strings.Join(windowRange(lines, from, to, height), "\n")
}

// lines is the whole form at width: what is being repaired, the four rows,
// whatever entry is open, and the status line.
func (f *repairForm) lines(width int) []string {
	out := make([]string, 0, 16)
	out = append(out, styleDim.Render("  "+f.subject()))
	out = append(out, "")
	for _, row := range []rfRow{rfPrompt, rfAgent, rfModel, rfEffort} {
		out = append(out, f.rowLines(row, width)...)
	}
	if f.picker != nil {
		out = append(out, f.picker.renderBody()...)
	}
	switch {
	case f.err != "":
		out = append(out, styleBad.Render("  ⚠ "+f.err))
	case f.editing:
		out = append(out, styleDim.Render("  ctrl+s keeps this text · esc discards it"))
	case f.picker != nil:
		out = append(out, styleDim.Render("  ↑/↓ choose · / filter · enter pick · esc close the list"))
	case f.submitting:
		out = append(out, styleDim.Render("  starting the repair agent…"))
	default:
		out = append(out, styleDim.Render(
			"  enter edit · e $EDITOR · ctrl+s start the repair · esc leave the form"))
	}
	return out
}

// subject is the one line saying what this repair is about, so the popup does
// not rely on the panels behind it to explain itself.
func (f *repairForm) subject() string {
	out := fmt.Sprintf("#%d", f.taskID)
	if f.stepName != "" {
		out += " · blocked at " + f.stepName
	}
	if f.blockReason != "" {
		out += " · " + f.blockReason
	}
	return out + " — the agent runs in this task's worktree; the task stays blocked afterwards"
}

// rowLines draws one row: its label, its value, and — for the prompt row
// while it is being typed into — the field itself.
func (f *repairForm) rowLines(row rfRow, width int) []string {
	cursor := "  "
	if f.cursor == row && !f.editing && f.picker == nil {
		cursor = styleFocus.Render("› ")
	}
	line := cursor + rfLabel(row) + " " + f.rowValue(row)
	out := []string{line}
	if row == rfPrompt {
		if f.editing {
			out = append(out, f.editorView(width)...)
		} else if f.prompt != "" {
			for _, l := range wrapPlain(f.prompt, width-editorIndent) {
				out = append(out, strings.Repeat(" ", editorIndent)+styleOK.Render(l))
			}
		}
	}
	return out
}

func (f *repairForm) rowValue(row rfRow) string {
	var v string
	switch row {
	case rfPrompt:
		if f.prompt == "" {
			return styleDim.Render("(nothing typed yet)")
		}
		return ""
	case rfAgent:
		v = f.agent
	case rfModel:
		v = f.model
	case rfEffort:
		v = f.effort
	case rfRowCount:
	}
	if v == "" {
		return styleDim.Render("(task or workflow default)")
	}
	return styleOK.Render(v)
}

func rfLabel(row rfRow) string {
	switch row {
	case rfPrompt:
		return "repair prompt"
	case rfAgent:
		return "agent "
	case rfModel:
		return "model "
	case rfEffort:
		return "effort"
	case rfRowCount:
	}
	return ""
}

// editorView sizes the prompt field to the popup's width and indents it clear
// of the cursor gutter, the way the answer form's free-text field is.
func (f *repairForm) editorView(width int) []string {
	f.editor.SetWidth(max(width-editorIndent, 8))
	out := strings.Split(f.editor.View(), "\n")
	pad := strings.Repeat(" ", editorIndent)
	for i := range out {
		out[i] = pad + out[i]
	}
	return out
}

// focusRange is the line range the focused element occupies, so windowing
// keeps the whole of it on screen. An open picker is the focus; otherwise the
// cursor's row is.
func (f *repairForm) focusRange(width int) (from, to int) {
	from = 2 // past the subject line and its blank
	for _, row := range []rfRow{rfPrompt, rfAgent, rfModel, rfEffort} {
		n := len(f.rowLines(row, width))
		if row == f.cursor {
			to = from + n
			if f.picker != nil {
				to += len(f.picker.renderBody())
			}
			return from, to
		}
		from += n
	}
	return 0, 0
}
