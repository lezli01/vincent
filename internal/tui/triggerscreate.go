package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The create prompt (§15 view 11, task 096.6). It asks what POST /v1/triggers
// needs — an id and a project — and offers the command and poll interval a
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
	trigCreateRowCommand
	trigCreateRowInterval
	trigCreateRows
)

type trigCreateForm struct {
	row      int
	id       textField
	command  textField
	interval textField
	projects []apiclient.Project
	project  int
	saving   bool
	err      string
}

func (v *triggersView) openCreate() {
	f := &trigCreateForm{
		id: newTextField(), command: newTextField(), interval: newTextField(),
		projects: v.projects,
	}
	f.id.SetPlaceholder("lowercase letters, digits, . _ - — the file becomes {id}.yaml")
	f.command.SetPlaceholder("optional: the poll command's argv, space-separated — run directly, never through a shell")
	f.interval.SetPlaceholder("optional: default 5m")
	// Start on the selected trigger's project: a second trigger for the same
	// repository is the likeliest next one.
	if s, ok := v.current(); ok {
		for i, p := range f.projects {
			if p.ID == s.ProjectID {
				f.project = i
			}
		}
	}
	f.id.Focus()
	v.err, v.note = "", ""
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
	f.row = wrapIndex(row, trigCreateRows)
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
		switch msg.String() {
		case "left", "h":
			f.project = wrapIndex(f.project-1, len(f.projects))
		case "right", "l", "space", " ":
			f.project = wrapIndex(f.project+1, len(f.projects))
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
	case len(f.projects) == 0:
		f.err = "no project is registered — a trigger's tasks are created in one"
		return nil
	case v.client == nil:
		f.err = "not connected"
		return nil
	}
	req := apiclient.CreateTriggerRequest{
		ID:           id,
		ProjectID:    f.projects[f.project].ID,
		PollInterval: strings.TrimSpace(f.interval.Value()),
		Command:      strings.Fields(f.command.Value()),
	}
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
	v.note = "created " + msg.id + " — disabled until you enable it"
	return tea.Batch(v.loadCmd(), cmd)
}
