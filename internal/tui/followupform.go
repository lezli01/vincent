package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// fuRow is one line of the follow-up form.
type fuRow int

const (
	// fuForm is the run-form chooser. It comes first because it decides what
	// the row under it means (task 027 decision 3).
	fuForm fuRow = iota
	fuBody
	fuAgent
	fuModel
	fuEffort
	fuRowCount
)

// followUpLoadedMsg carries the two catalogs the form's pickers render: the
// adapters, and the workflows visible to this task's project.
type followUpLoadedMsg struct {
	taskID    int64
	agents    apiclient.Agents
	workflows []apiclient.WorkflowEntry
}

// followUpEditMsg carries the text an $EDITOR session left behind.
type followUpEditMsg struct {
	taskID int64
	text   string
	err    error
}

// followUpForm collects what a §6 follow-up needs (task 027): which of the
// three run forms, what to run, and optionally which agent, model and effort
// to run it with.
//
// It is a form rather than a key that acts — which is what `a`, `s` and `r`
// are — for one reason: three run forms need a chooser, and a key that had to
// guess between "prompt" and "shell command" would guess wrong half the time.
// It shares repairForm's shape, and differs in having a row above the text
// that changes what the text means.
//
// Prompt and command text go to the daemon literally, never as templates
// (§8.4 renders with missingkey=error), so a `{{` typed here is two
// characters and not a broken step.
type followUpForm struct {
	taskID    int64
	projectID int64
	origin    string

	form     string
	prompt   string
	run      string
	workflow string
	agent    string
	model    string
	effort   string

	cursor  fuRow
	editor  textarea.Model
	editing bool

	// agents and workflows feed the pickers; nil until the fetch lands, and
	// nil forever when it fails. Every picker allows free text, so the form
	// stays usable without them — the daemon is the authority on what exists
	// and answers a bad value with a 400 that names it.
	agents    apiclient.Agents
	workflows []apiclient.WorkflowEntry
	picker    *picker

	// openEditor hands the body text to $EDITOR. Injected by the detail view,
	// which owns the exec path (editor.go), so the form itself needs no
	// terminal.
	openEditor func(text string) tea.Cmd

	err        string
	submitting bool
}

func newFollowUpForm(taskID, projectID int64, origin string) *followUpForm {
	ed := textarea.New()
	ed.Prompt = ""
	ed.ShowLineNumbers = false
	ed.DynamicHeight = true
	ed.MinHeight = 1
	ed.MaxHeight = editorLines
	ed.SetHeight(3)
	ed.SetWidth(40)
	return &followUpForm{
		taskID: taskID, projectID: projectID, origin: origin,
		form: apiclient.FollowUpFormAgent, editor: ed,
	}
}

// load fetches the adapter and workflow catalogs the pickers render. Neither
// failure is reported to the human: the pickers fall back to free text, which
// is all a follow-up needs.
func (f *followUpForm) load(client *apiclient.Client) tea.Cmd {
	if client == nil {
		return nil
	}
	taskID, projectID := f.taskID, f.projectID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := followUpLoadedMsg{taskID: taskID}
		if agents, err := client.ListAgents(ctx, false); err == nil {
			msg.agents = agents
		}
		if wfs, err := client.ListWorkflows(ctx, projectID); err == nil {
			msg.workflows = wfs
		}
		return msg
	}
}

// applyLoaded installs catalogs that arrived for this task.
func (f *followUpForm) applyLoaded(msg followUpLoadedMsg) {
	if msg.taskID != f.taskID {
		return
	}
	f.agents, f.workflows = msg.agents, msg.workflows
}

// applyEdit installs what an $EDITOR session produced.
func (f *followUpForm) applyEdit(msg followUpEditMsg) {
	if msg.taskID != f.taskID {
		return
	}
	if msg.err != nil {
		f.err = errString(msg.err)
		return
	}
	f.setBody(strings.TrimSpace(msg.text))
	f.err = ""
}

// paste types into whichever text entry is open.
func (f *followUpForm) paste(text string) tea.Cmd {
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
func (f *followUpForm) update(msg tea.KeyPressMsg, client *apiclient.Client) (cmd tea.Cmd, exit bool) {
	if f.picker != nil {
		res := f.picker.update(msg)
		if res.chosen {
			f.setRow(fuRow(f.picker.row), res.value)
		}
		if res.closed {
			f.picker = nil
		}
		return res.cmd, false
	}
	if f.editing {
		switch msg.String() {
		case "esc":
			// esc discards, enter is a newline: a prompt is prose and may
			// well want more than one line, so the field cannot spend enter
			// on committing. ctrl+s is what leaves it.
			f.editing = false
			f.editor.Blur()
			return nil, false
		case "ctrl+s":
			f.commitBody()
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
		f.cursor = min(f.cursor+1, fuRowCount-1)
	case "enter":
		f.openRow()
	case "e":
		if f.cursor == fuBody && f.bodyIsText() && f.openEditor != nil {
			return f.openEditor(f.body()), false
		}
		f.openRow()
	case "ctrl+s":
		return f.submit(client), false
	case "esc":
		return nil, true
	}
	return nil, false
}

// openRow opens whatever the cursor is on: the form chooser, the body — as a
// text field for the agent and command forms, as a workflow list for the
// third — or one of the three §8.6 pickers.
func (f *followUpForm) openRow() {
	f.err = ""
	switch f.cursor {
	case fuForm:
		f.picker = newPicker(int(fuForm), "run", followUpFormOptions(), false, f.form)
	case fuBody:
		if f.bodyIsText() {
			f.startBody()
			return
		}
		f.picker = newPicker(int(fuBody), "workflow", f.workflowOptions(), true, f.workflow)
	case fuAgent:
		f.picker = newPicker(int(fuAgent), "agent", f.agentOptions(), true, f.agent)
	case fuModel:
		f.picker = newPicker(int(fuModel), "model", f.catalogOptions(f.models()), true, f.model)
	case fuEffort:
		f.picker = newPicker(int(fuEffort), "effort", f.catalogOptions(f.efforts()), true, f.effort)
	case fuRowCount:
	}
}

func (f *followUpForm) setRow(row fuRow, value string) {
	switch row {
	case fuForm:
		f.form = value
	case fuBody:
		f.workflow = value
	case fuAgent:
		f.agent = value
		// The model and effort catalogs belong to an adapter; keeping values
		// chosen from another one would submit a pair that never existed.
		f.model, f.effort = "", ""
	case fuModel:
		f.model = value
	case fuEffort:
		f.effort = value
	case fuRowCount:
	}
}

// bodyIsText reports whether the body row is free text rather than a list.
// The workflow form names something the registry already has; the other two
// are typed.
func (f *followUpForm) bodyIsText() bool {
	return f.form != apiclient.FollowUpFormWorkflow
}

// body is what the body row currently holds, for whichever form is chosen.
func (f *followUpForm) body() string {
	switch f.form {
	case apiclient.FollowUpFormCommand:
		return f.run
	case apiclient.FollowUpFormWorkflow:
		return f.workflow
	default:
		return f.prompt
	}
}

// setBody writes the body row for whichever form is chosen. The three are
// kept apart rather than sharing one field, so switching forms to look at the
// other one and switching back does not lose what was typed.
func (f *followUpForm) setBody(value string) {
	switch f.form {
	case apiclient.FollowUpFormCommand:
		f.run = value
	case apiclient.FollowUpFormWorkflow:
		f.workflow = value
	default:
		f.prompt = value
	}
}

func (f *followUpForm) startBody() {
	f.editing = true
	f.cursor = fuBody
	f.editor.Placeholder = followUpPlaceholder(f.form)
	f.editor.SetValue(f.body())
	f.editor.Focus()
}

func (f *followUpForm) commitBody() {
	f.editing = false
	f.editor.Blur()
	f.setBody(strings.TrimSpace(f.editor.Value()))
	if f.body() != "" {
		f.err = ""
	}
}

// followUpFormOptions is the chooser. Free text is off: these three are the
// forms the daemon compiles, and a fourth would be a 400.
func followUpFormOptions() []pickerOption {
	return []pickerOption{
		{value: apiclient.FollowUpFormAgent, label: "agent", note: "a free-form prompt"},
		{
			value: apiclient.FollowUpFormCommand, label: "command",
			note: "a shell command — /bin/sh, or pwsh on Windows",
		},
		{
			value: apiclient.FollowUpFormWorkflow, label: "workflow",
			note: "a workflow from the registry",
		},
	}
}

func followUpPlaceholder(form string) string {
	if form == apiclient.FollowUpFormCommand {
		return "git rebase origin/main"
	}
	return "what should the agent do in this worktree?"
}

// workflowOptions lists what the registry has for this task's project, with
// the note the workflows view uses: where it came from, and whether it can
// run on this host at all (§8.1.1).
func (f *followUpForm) workflowOptions() []pickerOption {
	out := make([]pickerOption, 0, len(f.workflows))
	for _, w := range f.workflows {
		opt := pickerOption{value: w.Name, label: w.Name, note: w.Scope}
		if w.PlatformSupported != nil && !*w.PlatformSupported {
			opt.note = "⚠ does not run on this host"
		}
		out = append(out, opt)
	}
	return out
}

// agentOptions is the adapter list, with the same availability notes the
// repair and new-task pickers carry: an adapter that is installed but not
// logged in fails every run, and saying so here saves a follow-up that was
// never going to start (§9.5).
func (f *followUpForm) agentOptions() []pickerOption {
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

func (f *followUpForm) models() []apiclient.AgentOption {
	a, ok := f.agents.Find(f.agent)
	if !ok {
		return nil
	}
	return a.Models
}

func (f *followUpForm) efforts() []apiclient.AgentOption {
	a, ok := f.agents.Find(f.agent)
	if !ok {
		return nil
	}
	return a.Efforts
}

// catalogOptions renders a catalog with its provenance tags, the way the
// repair and new-task pickers do: "cli" came from the adapter itself,
// "curated" from vincent's own list and may be stale.
func (f *followUpForm) catalogOptions(opts []apiclient.AgentOption) []pickerOption {
	out := []pickerOption{{value: "", label: "(task or workflow default)"}}
	for _, o := range opts {
		out = append(out, pickerOption{value: o.Value, label: o.Value, note: o.Source})
	}
	return out
}

// request is what the form would submit. Only the field the chosen form owns
// is sent: the daemon refuses a request that names two things to run.
func (f *followUpForm) request() apiclient.FollowUpInput {
	in := apiclient.FollowUpInput{Agent: f.agent, Model: f.model, Effort: f.effort}
	switch f.form {
	case apiclient.FollowUpFormCommand:
		in.Run = f.run
	case apiclient.FollowUpFormWorkflow:
		in.Workflow = f.workflow
	default:
		in.Prompt = f.prompt
	}
	return in
}

// submit posts the follow-up. An empty body is refused here as well as by the
// daemon: a round trip to be told to type something is a round trip wasted.
func (f *followUpForm) submit(client *apiclient.Client) tea.Cmd {
	if f.editing {
		f.commitBody()
	}
	if strings.TrimSpace(f.body()) == "" {
		f.err = followUpEmptyMessage(f.form)
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
		task, _, err := client.FollowUp(ctx, taskID, req)
		return actionResultMsg{
			taskID: taskID, action: apiclient.ActionFollowUp, task: task, err: err,
		}
	}
}

func followUpEmptyMessage(form string) string {
	switch form {
	case apiclient.FollowUpFormCommand:
		return "type the command to run first"
	case apiclient.FollowUpFormWorkflow:
		return "choose a workflow first"
	default:
		return "type what the agent should do first"
	}
}

// height is how many lines the form wants at the width it will be drawn at.
func (f *followUpForm) height(width int) int { return len(f.lines(width)) }

// render draws the form, windowed on the focused element when it does not
// fit.
func (f *followUpForm) render(width, height int) string {
	lines := f.lines(width)
	from, to := f.focusRange(width)
	return strings.Join(windowRange(lines, from, to, height), "\n")
}

// lines is the whole form at width: what is being followed up, the five rows,
// whatever entry is open, and the status line.
func (f *followUpForm) lines(width int) []string {
	out := make([]string, 0, 18)
	out = append(out, styleDim.Render("  "+f.subject()))
	out = append(out, "")
	for _, row := range fuRows() {
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
		out = append(out, styleDim.Render("  queueing the follow-up…"))
	default:
		out = append(out, styleDim.Render(
			"  enter edit · e $EDITOR · ctrl+s start the follow-up · ctrl+t task details · esc leave the form"))
	}
	return out
}

func fuRows() []fuRow { return []fuRow{fuForm, fuBody, fuAgent, fuModel, fuEffort} }

// subject is the one line saying what this follow-up is about, so the popup
// does not rely on the panels behind it to explain itself. It names the state
// the task returns to, because that is the property people get wrong: a
// follow-up decides nothing about a task's verdict.
func (f *followUpForm) subject() string {
	out := fmt.Sprintf("#%d", f.taskID)
	if f.origin != "" {
		out += " · " + f.origin
	}
	return out + " — runs in this task's existing worktree and branch; the task returns to " +
		firstNonEmpty(f.origin, "where it was") + " afterwards"
}

// rowLines draws one row: its label, its value, and — for the body row while
// it is being typed into — the field itself.
func (f *followUpForm) rowLines(row fuRow, width int) []string {
	cursor := "  "
	if f.cursor == row && !f.editing && f.picker == nil {
		cursor = styleFocus.Render("› ")
	}
	line := cursor + f.rowLabel(row) + " " + f.rowValue(row)
	out := []string{line}
	if row == fuBody && f.bodyIsText() {
		if f.editing {
			out = append(out, f.editorView(width)...)
		} else if f.body() != "" {
			for _, l := range wrapPlain(f.body(), width-editorIndent) {
				out = append(out, strings.Repeat(" ", editorIndent)+styleOK.Render(l))
			}
		}
	}
	return out
}

func (f *followUpForm) rowValue(row fuRow) string {
	var v string
	switch row {
	case fuForm:
		return styleOK.Render(f.form)
	case fuBody:
		if f.body() == "" {
			if f.bodyIsText() {
				return styleDim.Render("(nothing typed yet)")
			}
			return styleDim.Render("(none chosen yet)")
		}
		if f.bodyIsText() {
			return "" // the text is rendered under the row
		}
		return styleOK.Render(f.body())
	case fuAgent:
		v = f.agent
	case fuModel:
		v = f.model
	case fuEffort:
		v = f.effort
	case fuRowCount:
	}
	if v == "" {
		return styleDim.Render("(task or workflow default)")
	}
	return styleOK.Render(v)
}

// rowLabel names a row. The body row's label changes with the form, which is
// the whole reason the chooser sits above it.
func (f *followUpForm) rowLabel(row fuRow) string {
	switch row {
	case fuForm:
		return "run   "
	case fuBody:
		switch f.form {
		case apiclient.FollowUpFormCommand:
			return "command"
		case apiclient.FollowUpFormWorkflow:
			return "workflow"
		default:
			return "prompt"
		}
	case fuAgent:
		return "agent "
	case fuModel:
		return "model "
	case fuEffort:
		return "effort"
	case fuRowCount:
	}
	return ""
}

// editorView sizes the body field to the popup's width and indents it clear
// of the cursor gutter, the way the repair form's is.
func (f *followUpForm) editorView(width int) []string {
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
func (f *followUpForm) focusRange(width int) (from, to int) {
	from = 2 // past the subject line and its blank
	for _, row := range fuRows() {
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
