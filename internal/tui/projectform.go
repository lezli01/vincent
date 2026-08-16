package tui

import (
	"context"
	"os"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// pfRow identifies one line of the project form.
type pfRow int

const (
	pfPath pfRow = iota
	pfName
	pfBranch
	pfWorkflow
	pfCap
	pfSave
	pfRowCount
)

func (r pfRow) label() string {
	switch r {
	case pfPath:
		return "repository"
	case pfName:
		return "name"
	case pfBranch:
		return "default branch"
	case pfWorkflow:
		return "default workflow"
	case pfCap:
		return "max parallel tasks"
	case pfSave:
		return "save"
	case pfRowCount:
	}
	return ""
}

// pfWorkflowsMsg carries the registry the default-workflow picker offers.
type pfWorkflowsMsg struct {
	entries []apiclient.WorkflowEntry
	err     error
}

// projectForm adds or edits one project. It is deliberately not the new-task
// form generalised: that one validates three rules locally and posts once,
// while this one PATCHes only what changed against server-side path and
// branch validation the client cannot reproduce.
type projectForm struct {
	client *apiclient.Client
	// original is nil when adding. When editing it is what the PATCH diffs
	// against, so an untouched field is absent from the body rather than
	// resent — a resend would stomp a concurrent edit.
	original *apiclient.Project

	path     textinput.Model
	name     textinput.Model
	branch   textinput.Model
	cap      textinput.Model
	workflow string
	// workflowSet records that the workflow row was touched, so clearing it
	// on an edit sends null rather than being mistaken for "unchanged".
	workflowSet bool

	workflows []apiclient.WorkflowEntry

	cursor  pfRow
	editing bool
	pick    *picker

	rowErr map[pfRow]string
	err    string
	saving bool
}

func newProjectForm(client *apiclient.Client, p *apiclient.Project) *projectForm {
	f := &projectForm{
		client:   client,
		original: p,
		path:     textinput.New(),
		name:     textinput.New(),
		branch:   textinput.New(),
		cap:      textinput.New(),
		rowErr:   map[pfRow]string{},
	}
	f.path.Placeholder = "path to a git repository"
	f.name.Placeholder = "(the directory name)"
	f.branch.Placeholder = "(detected from the repository)"
	f.cap.Placeholder = "(no project cap)"
	if p != nil {
		f.path.SetValue(p.Path)
		f.name.SetValue(p.Name)
		f.branch.SetValue(p.DefaultBranch)
		if p.DefaultWorkflow != nil {
			f.workflow = *p.DefaultWorkflow
		}
		if p.MaxParallelTasks != nil {
			f.cap.SetValue(strconv.Itoa(*p.MaxParallelTasks))
		}
		f.cursor = pfName
	} else {
		// Adding: the path is the only field that has to be typed, so the
		// cursor starts there and it prefills with where the TUI was
		// launched — usually the repository the user means, and one ctrl+u
		// away when it is not.
		f.path.SetValue(workingDir())
	}
	return f
}

// workingDir is the process cwd, or empty when it cannot be determined —
// an unprefilled field is a smaller problem than a wrong one.
func workingDir() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

func (f *projectForm) adding() bool { return f.original == nil }

func (f *projectForm) capturesInput() bool {
	// The picker's filter types as surely as its free-text row does; both must
	// stand the global single-key bindings down.
	return f.editing || (f.pick != nil && (f.pick.editing || f.pick.filtering))
}

// paste types into the row being edited, or into the picker's free-text
// entry when one is open.
func (f *projectForm) paste(text string) tea.Cmd {
	if f.pick != nil {
		return f.pick.paste(text)
	}
	if !f.editing {
		return nil
	}
	var cmd tea.Cmd
	switch f.cursor {
	case pfPath:
		f.path, cmd = f.path.Update(tea.PasteMsg{Content: text})
	case pfName:
		f.name, cmd = f.name.Update(tea.PasteMsg{Content: text})
	case pfBranch:
		f.branch, cmd = f.branch.Update(tea.PasteMsg{Content: text})
	case pfCap:
		f.cap, cmd = f.cap.Update(tea.PasteMsg{Content: text})
	case pfWorkflow, pfSave, pfRowCount:
		return nil
	}
	delete(f.rowErr, f.cursor)
	return cmd
}

// loadCmd fetches the registry for the default-workflow picker. It is
// project-scoped (§5.2), so an edit sees the project's own workflows and an
// add sees the global ones — the only registry a project that does not exist
// yet can have.
func (f *projectForm) loadCmd() tea.Cmd {
	client := f.client
	if client == nil {
		return nil
	}
	var id int64
	if f.original != nil {
		id = f.original.ID
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		entries, err := client.ListWorkflows(ctx, id)
		return pfWorkflowsMsg{entries: entries, err: err}
	}
}

// refreshFrom re-reads the project under edit after a refetch, so a rename
// made elsewhere does not get overwritten by this form's stale copy. Fields
// the human has already touched are left alone.
func (f *projectForm) refreshFrom(projects []apiclient.Project) {
	if f.original == nil {
		return
	}
	for i := range projects {
		if projects[i].ID == f.original.ID {
			f.original = &projects[i]
			return
		}
	}
}

func (f *projectForm) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	if f.pick != nil {
		return f.updatePicking(msg)
	}
	if f.editing {
		return f.updateEditing(msg)
	}
	switch msg.String() {
	case "up", "k", "shift+tab":
		f.move(-1)
	case "down", "j", "tab":
		f.move(1)
	case "enter":
		return f.activate()
	case "ctrl+s":
		return f.submit()
	case "esc":
		return func() tea.Msg { return projectFormClosedMsg{} }
	}
	return nil
}

// projectFormClosedMsg asks the view to drop the form.
type projectFormClosedMsg struct{}

func (f *projectForm) move(delta int) {
	next := int(f.cursor) + delta
	if next < 0 {
		next = 0
	}
	if next >= int(pfRowCount) {
		next = int(pfRowCount) - 1
	}
	f.cursor = pfRow(next)
}

func (f *projectForm) activate() tea.Cmd {
	switch f.cursor {
	case pfPath, pfName, pfBranch, pfCap:
		f.editing = true
		f.focus(f.cursor)
	case pfWorkflow:
		f.pick = newPicker(int(pfWorkflow), "default workflow",
			f.workflowOptions(), false, f.workflow)
	case pfSave:
		return f.submit()
	case pfRowCount:
	}
	return nil
}

func (f *projectForm) focus(row pfRow) {
	switch row {
	case pfPath:
		f.path.Focus()
	case pfName:
		f.name.Focus()
	case pfBranch:
		f.branch.Focus()
	case pfCap:
		f.cap.Focus()
	case pfWorkflow, pfSave, pfRowCount:
	}
}

func (f *projectForm) blurAll() {
	f.path.Blur()
	f.name.Blur()
	f.branch.Blur()
	f.cap.Blur()
}

func (f *projectForm) updateEditing(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "enter":
		f.editing = false
		f.blurAll()
		return nil
	}
	var cmd tea.Cmd
	switch f.cursor {
	case pfPath:
		f.path, cmd = f.path.Update(msg)
	case pfName:
		f.name, cmd = f.name.Update(msg)
	case pfBranch:
		f.branch, cmd = f.branch.Update(msg)
	case pfCap:
		f.cap, cmd = f.cap.Update(msg)
	case pfWorkflow, pfSave, pfRowCount:
	}
	delete(f.rowErr, f.cursor)
	return cmd
}

func (f *projectForm) updatePicking(msg tea.KeyPressMsg) tea.Cmd {
	res := f.pick.update(msg)
	if res.chosen {
		f.workflow = res.value
		f.workflowSet = true
		delete(f.rowErr, pfWorkflow)
	}
	if res.closed {
		f.pick = nil
	}
	return res.cmd
}

// workflowOptions offers every registry entry plus an explicit "none", which
// is how a project's default is cleared. An invalid entry is listed and
// refused for the same reason the new-task picker lists it: a workflow that
// vanishes when you typo it looks like a workflow you lost.
func (f *projectForm) workflowOptions() []pickerOption {
	out := []pickerOption{{value: "", label: "(none — new tasks use adhoc)"}}
	for _, e := range f.workflows {
		opt := pickerOption{value: e.Name, label: e.Name, note: e.Scope}
		if !e.Valid() {
			opt.disabled = true
			opt.note = "invalid: " + e.FirstError()
		} else if !e.RunsHere() {
			// Selectable, unlike in the new-task picker: a project's default
			// is repository configuration that may well be shared with hosts
			// where the workflow does run. It is only a task created *here*
			// that the restriction refuses (task 010).
			opt.note = e.Scope + " · ⚠ not on this platform, " + e.PlatformNote()
		}
		out = append(out, opt)
	}
	return out
}

// submit blocks locally on the one thing decidable without the daemon and
// sends everything else. Whether the path is a repository, whether the
// branch resolves, whether the name collides and whether the cap is in range
// are all the daemon's checks; a second implementation here would only drift.
func (f *projectForm) submit() tea.Cmd {
	f.rowErr = map[pfRow]string{}
	f.err = ""
	if strings.TrimSpace(f.path.Value()) == "" {
		f.rowErr[pfPath] = "a repository path is required"
		f.cursor = pfPath
		return nil
	}
	if f.client == nil {
		f.err = "not connected"
		return nil
	}
	f.saving = true
	if f.adding() {
		return f.createCmd()
	}
	return f.patchCmd()
}

// createRequest builds the POST body. Every field left alone is omitted so
// the daemon's own derivation applies: the directory name, and the branch it
// detects in the repository.
func (f *projectForm) createRequest() apiclient.CreateProjectRequest {
	req := apiclient.CreateProjectRequest{Path: strings.TrimSpace(f.path.Value())}
	if v := strings.TrimSpace(f.name.Value()); v != "" {
		req.Name = ptr(v)
	}
	if v := strings.TrimSpace(f.branch.Value()); v != "" {
		req.DefaultBranch = ptr(v)
	}
	if f.workflow != "" {
		req.DefaultWorkflow = ptr(f.workflow)
	}
	if v, ok := f.capValue(); ok {
		req.MaxParallelTasks = ptr(v)
	}
	return req
}

func (f *projectForm) createCmd() tea.Cmd {
	client := f.client
	req := f.createRequest()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		p, err := client.CreateProject(ctx, req)
		return projectSavedMsg{project: p, err: err}
	}
}

func (f *projectForm) patchCmd() tea.Cmd {
	client, id := f.client, f.original.ID
	req := f.patchRequest()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		p, err := client.PatchProject(ctx, id, req)
		return projectSavedMsg{project: p, err: err}
	}
}

// patchRequest sends only what differs from the project as last fetched.
// The two nullable fields are where the three states earn their keep: an
// empty workflow row is a clear, an empty cap row is "no project cap", and
// neither is the same as leaving the row alone.
func (f *projectForm) patchRequest() apiclient.PatchProjectRequest {
	o := f.original
	var req apiclient.PatchProjectRequest
	if v := strings.TrimSpace(f.name.Value()); v != o.Name {
		req.Name = apiclient.SetOpt(v)
	}
	if v := strings.TrimSpace(f.path.Value()); v != o.Path {
		req.Path = apiclient.SetOpt(v)
	}
	if v := strings.TrimSpace(f.branch.Value()); v != o.DefaultBranch {
		req.DefaultBranch = apiclient.SetOpt(v)
	}
	if f.workflowSet {
		switch {
		case f.workflow == "" && o.DefaultWorkflow != nil:
			req.DefaultWorkflow = apiclient.NullOpt[string]()
		case f.workflow != "" && (o.DefaultWorkflow == nil || *o.DefaultWorkflow != f.workflow):
			req.DefaultWorkflow = apiclient.SetOpt(f.workflow)
		}
	}
	switch v, ok := f.capValue(); {
	case ok && (o.MaxParallelTasks == nil || *o.MaxParallelTasks != v):
		req.MaxParallelTasks = apiclient.SetOpt(v)
	case !ok && o.MaxParallelTasks != nil:
		req.MaxParallelTasks = apiclient.NullOpt[int]()
	}
	return req
}

// capValue reads the cap row. An empty row is "no project cap"; anything
// that is not an integer is sent as zero so the daemon rejects it with its
// own message rather than the form inventing a second rule.
func (f *projectForm) capValue() (int, bool) {
	raw := strings.TrimSpace(f.cap.Value())
	if raw == "" {
		return 0, false
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, true
	}
	return v, true
}

// applyFailure parks the daemon's complaint on the row it names, the same
// routing the new-task form uses: the server writes field names into its
// validation messages, so the mapping reads them instead of guessing.
func (f *projectForm) applyFailure(err error) {
	f.saving = false
	msg := errString(err)
	for _, m := range []struct {
		needle string
		row    pfRow
	}{
		{"max_parallel_tasks", pfCap},
		{"default_workflow", pfWorkflow},
		{"default_branch", pfBranch},
		{"branch", pfBranch},
		{"name", pfName},
		{"path", pfPath},
		{"repository", pfPath},
	} {
		if strings.Contains(msg, m.needle) {
			f.rowErr[m.row] = msg
			f.cursor = m.row
			return
		}
	}
	f.err = msg
}
