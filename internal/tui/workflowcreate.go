package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The create and fork prompt (§15 view 5, task 065). It asks the two things
// the daemon needs — a scope and a file name — and nothing else: the starting
// bytes are the §8 skeleton or the fork source, both chosen server-side, so
// no YAML is composed here (decision 2).

// wfCreateForm is the open prompt. fork is set when the entry under the
// cursor is being copied down into a project scope, where it shadows the
// original per §5.2.
type wfCreateForm struct {
	fork bool
	// source is the entry a fork copies, empty for a plain create.
	source string
	// sourceProject scopes the lookup of source.
	sourceProject int64

	name textField
	// scopes are the destinations, in registry order: global first, then
	// each project. A fork of a global entry into the global scope would
	// shadow nothing, so it is still offered — the daemon refuses the
	// duplicate name, and refusing it here would be a second copy of a rule.
	scopes []wfScopeChoice
	scope  int
	// row is which of the two rows has the keyboard.
	row    int
	saving bool
	err    string
}

// wfScopeChoice is one destination: a label, the scope string and the project
// it belongs to.
type wfScopeChoice struct {
	label     string
	scope     string
	projectID int64
}

const (
	wfCreateRowScope = 0
	wfCreateRowName  = 1
)

// openCreate opens the prompt. A fork takes the entry under the cursor as its
// source; a plain create takes only the scope list. It issues no command:
// everything it needs is already in the loaded blocks.
func (w *workflowsView) openCreate(fork bool) {
	f := &wfCreateForm{fork: fork, name: newTextField()}
	f.name.SetPlaceholder("file name (lowercase, no spaces)")
	if fork {
		line, ok := w.currentLine()
		if !ok {
			return
		}
		f.source = line.entry.Name
		if line.block != nil {
			f.sourceProject = line.block.projectID
		}
		// A fork keeps the source's own name:, so the name row addresses the
		// file rather than the workflow. Suggesting the source's name makes
		// the shadowing case one keystroke.
		f.name.SetValue(line.entry.Name)
		f.row = wfCreateRowScope
	} else {
		f.row = wfCreateRowName
		f.name.Focus()
	}
	for _, b := range w.blocks {
		if b.projectID == 0 {
			f.scopes = append(f.scopes, wfScopeChoice{label: "global", scope: "global"})
			continue
		}
		f.scopes = append(f.scopes, wfScopeChoice{
			label: b.name, scope: scopeProject, projectID: b.projectID,
		})
	}
	if len(f.scopes) == 0 {
		w.err = "no scope to write to: the daemon has no global workflow directory"
		return
	}
	// A fork defaults to the first project scope, because forking down into a
	// project is what the operation is for.
	if fork && len(f.scopes) > 1 {
		f.scope = 1
	}
	w.create = f
}

func (f *wfCreateForm) capturing() bool { return f.row == wfCreateRowName && f.name.Focused() }

func (w *workflowsView) updateCreateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	f := w.create
	switch msg.String() {
	case "esc":
		if f.err != "" {
			f.err = ""
			return w, nil
		}
		w.create = nil
		return w, nil
	case "tab", "up", "down":
		f.row = 1 - f.row
		if f.row == wfCreateRowName {
			f.name.Focus()
		} else {
			f.name.Blur()
		}
		return w, nil
	}
	if f.row == wfCreateRowScope {
		switch msg.String() {
		case "left", "h":
			f.scope = (f.scope - 1 + len(f.scopes)) % len(f.scopes)
			return w, nil
		case "right", "l", " ":
			f.scope = (f.scope + 1) % len(f.scopes)
			return w, nil
		case "enter":
			return w, w.submitCreate()
		}
		return w, nil
	}
	if msg.String() == "enter" {
		return w, w.submitCreate()
	}
	in, cmd := f.name.Update(msg)
	f.name = in
	return w, cmd
}

func (w *workflowsView) submitCreate() tea.Cmd {
	client, f := w.client, w.create
	if client == nil || f.saving {
		return nil
	}
	name := strings.TrimSpace(f.name.Value())
	if name == "" {
		f.err = "a file name is required"
		return nil
	}
	dest := f.scopes[f.scope]
	req := apiclient.CreateWorkflowRequest{Scope: dest.scope, ProjectID: dest.projectID, Name: name}
	if f.fork {
		req.From = f.source
		src := f.sourceProject
		req.FromProjectID = &src
	}
	f.saving = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		res, err := client.CreateWorkflow(ctx, req)
		return wfCreatedMsg{result: res, projectID: dest.projectID, err: err}
	}
}

// updateCreateMsg handles the create's own message. On success the prompt
// closes and the structured editor opens on what was just written, which is
// the whole point of creating one.
func (w *workflowsView) updateCreateMsg(msg tea.Msg) (panel, tea.Cmd, bool) {
	created, ok := msg.(wfCreatedMsg)
	if !ok {
		return w, nil, false
	}
	if w.create == nil {
		return w, nil, true
	}
	w.create.saving = false
	if created.err != nil {
		w.create.err = created.err.Error()
		return w, nil, true
	}
	w.create = nil
	key := wfResolveKey{projectID: created.projectID, name: created.result.Name}
	w.editor = &wfEditorLayer{
		key:     key,
		scope:   created.result.Scope,
		file:    created.result.File,
		version: created.result.Version,
		loading: true,
	}
	return w, tea.Batch(w.loadCmd(), w.editorLoadCmd(key)), true
}
