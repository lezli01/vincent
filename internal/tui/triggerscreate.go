package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The create prompt (§15 view 11, task 096.6). It asks what POST /v1/triggers
// needs — an id; the project is the selected one, shown and locked (task 132
// decision 9) — and offers the command and poll interval a
// `type: command` starter is usually edited for first. The daemon renders the
// starter: disabled, with no on_fire line, so it means propose until someone
// writes otherwise and confirms it (decision 7). The form opens on the new
// file straight after, which is where everything else is edited.

type trigCreatedMsg struct {
	id     string
	result apiclient.TriggerWriteResult
	err    error
}

const (
	trigCreateRowID = iota
	trigCreateRowProject
	trigCreateRowSource
	trigCreateRowCommand
	trigCreateRowInterval
	trigCreateRows
)

type trigCreateForm struct {
	row      int
	id       textField
	command  textField
	interval textField
	// projectID is the selection the trigger is created in, read-only:
	// another project's trigger is made from that project (task 132.12).
	projectID   int64
	projectName string
	// source indexes trigCreateSources (#1123): the starter's source.
	source int
	saving bool
	err    string
}

// trigCreateSource is one source a create can start from, with what its
// starter does as written. The values are the API's source_type, which
// refuses anything else.
type trigCreateSource struct{ value, does string }

var trigCreateSources = []trigCreateSource{
	{"command", "runs a command on an interval and creates a task per new event it prints"},
	{"issues", "creates a task when an issue of this project changes here — sync's changes need allowed_actors"},
	{"schedule", "creates a task on a clock — every hour to start, or a cron expression"},
	{"github_prs", "creates a task when a pull request merges on the project's GitHub repository"},
	{"http", "creates a task per event pushed to the daemon, signed with $VINCENT_TRIGGER_SECRET"},
}

// isCommand reports a command source, the one with a command and interval.
func (f *trigCreateForm) isCommand() bool { return trigCreateSources[f.source].value == "command" }

func (v *triggersView) openCreate() {
	f := &trigCreateForm{
		id: newTextField(), command: newTextField(), interval: newTextField(),
		projectID: v.project.id, projectName: v.project.name,
	}
	f.id.SetPlaceholder("lowercase letters, digits, . _ - — the file becomes {id}.yaml")
	f.command.SetPlaceholder("optional: the poll command's argv, space-separated — run directly, never through a shell")
	f.interval.SetPlaceholder("optional: default 5m")
	f.id.Focus()
	v.create = f
}

// field is the text field on the focused row; the project row has none.
func (f *trigCreateForm) field() *textField {
	switch f.row {
	case trigCreateRowID:
		return &f.id
	case trigCreateRowCommand:
		return &f.command
	case trigCreateRowInterval:
		return &f.interval
	}
	return nil
}

func (f *trigCreateForm) focusRow(row int) {
	step := 1
	if row < f.row {
		step = -1
	}
	row = wrapIndex(row, trigCreateRows)
	// The command and interval rows are a command source's alone.
	for !f.isCommand() && (row == trigCreateRowCommand || row == trigCreateRowInterval) {
		row = wrapIndex(row+step, trigCreateRows)
	}
	f.row = row
	f.id.Blur()
	f.command.Blur()
	f.interval.Blur()
	if fl := f.field(); fl != nil {
		fl.Focus()
	}
}

func (v *triggersView) updateCreateKey(msg tea.KeyPressMsg) tea.Cmd {
	f := v.create
	switch msg.String() {
	case "esc":
		v.create = nil
		return nil
	case "tab", "down":
		f.focusRow(f.row + 1)
		return nil
	case "shift+tab", "up":
		f.focusRow(f.row - 1)
		return nil
	case "enter", "ctrl+s":
		return v.createCmd()
	}
	if f.row == trigCreateRowProject {
		return nil // locked to the selection
	}
	if f.row == trigCreateRowSource {
		switch msg.String() {
		case "right", "l", " ", "space":
			f.source = wrapIndex(f.source+1, len(trigCreateSources))
		case "left", "h":
			f.source = wrapIndex(f.source-1, len(trigCreateSources))
		}
		return nil
	}
	fl := f.field()
	var cmd tea.Cmd
	*fl, cmd = fl.Update(msg)
	return cmd
}

func (v *triggersView) createCmd() tea.Cmd {
	f := v.create
	if f.saving {
		return nil
	}
	id := strings.TrimSpace(f.id.Value())
	switch {
	case id == "":
		f.err = "an id is required — it names the file"
		f.focusRow(trigCreateRowID)
		return nil
	case f.projectID == 0:
		f.err = "no project is selected — a trigger's tasks are created in one"
		return nil
	case v.client == nil:
		f.err = "not connected"
		return nil
	}
	req := f.request(id)
	f.err, f.saving = "", true
	client := v.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		res, err := client.CreateTrigger(ctx, req)
		return trigCreatedMsg{id: id, result: res, err: err}
	}
}

// applyCreated closes the prompt on the new file and opens the form on it; a
// refusal (an id in use, one no file could have) stays on the prompt.
func (v *triggersView) applyCreated(msg trigCreatedMsg) tea.Cmd {
	f := v.create
	if f == nil {
		return nil
	}
	f.saving = false
	if msg.err != nil {
		f.err = errString(msg.err)
		return nil
	}
	v.create = nil
	v.selectedID = msg.id
	cmd := v.openFormOn(msg.id, msg.result.File, msg.result.Version)
	v.status().result("created "+msg.id+" — disabled until you enable it", false)
	return tea.Batch(v.loadCmd(), cmd)
}

// request is what the form sends for id: the poll fields only for a command
// source, which is the one that has them.
func (f *trigCreateForm) request(id string) apiclient.CreateTriggerRequest {
	req := apiclient.CreateTriggerRequest{ID: id, ProjectID: f.projectID, SourceType: trigCreateSources[f.source].value}
	if f.isCommand() {
		req.PollInterval = strings.TrimSpace(f.interval.Value())
		req.Command = strings.Fields(f.command.Value())
	}
	return req
}
