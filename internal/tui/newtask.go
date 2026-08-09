package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// loadTimeout bounds the three catalog fetches the form opens with.
const loadTimeout = 10 * time.Second

// ntRow identifies one line of the form. The order is §15's: project →
// workflow → title → description → fields → base branch → priority →
// agent/model/effort → create. It is one screen rather than nine, because
// the arrows in §15 are field order, not screen count, and a form gets
// back-navigation and review-before-submit for free.
type ntRow int

const (
	ntProject ntRow = iota
	ntWorkflow
	ntTitle
	ntDescription
	ntFields
	ntBranch
	ntPriority
	ntAgent
	ntModel
	ntEffort
	ntCreate
	ntRowCount
)

// ntMode is what the form is doing with the keyboard.
type ntMode int

const (
	ntNavigating ntMode = iota // moving between rows
	ntEditing                  // a text row owns the keys
	ntPicking                  // a picker is open
	ntFieldsOpen               // the key/value editor is open
	ntConfirming               // "discard this draft?"
)

// New-task messages.
type (
	// newTaskMsg opens the form, seeded with the project the caller was
	// looking at.
	newTaskMsg struct{ projectID int64 }
	// ntLoadedMsg carries the three catalogs the form needs. They are
	// fetched together because a form missing any of them cannot render a
	// single picker honestly.
	ntLoadedMsg struct {
		projects  []apiclient.Project
		workflows []apiclient.WorkflowEntry
		agents    apiclient.Agents
		err       error
	}
	// ntWorkflowsMsg carries a workflow list refetched for a new project:
	// the registry is project-scoped (§5.2), so switching project changes
	// which workflows exist.
	ntWorkflowsMsg struct {
		projectID int64
		entries   []apiclient.WorkflowEntry
		err       error
	}
	// ntDescriptionMsg carries the result of editing the description in
	// $EDITOR.
	ntDescriptionMsg struct {
		text string
		err  error
	}
	// taskCreatedMsg reports a successful POST /v1/tasks. The root routes it:
	// the form does not own where the user goes next.
	taskCreatedMsg struct{ task apiclient.TaskDetail }
	// ntFailedMsg carries a create that the daemon rejected.
	ntFailedMsg struct{ err error }
)

// kv is one custom field. Order is preserved so a human sees the rows where
// they left them; the wire format is a map.
type kv struct{ key, value string }

// newTask is the §15 new-task flow.
type newTask struct {
	client *apiclient.Client
	// exec runs $EDITOR. Injected so tests drive the description path
	// without a terminal, the same seam edit+retry uses.
	exec execFunc

	projects  []apiclient.Project
	workflows []apiclient.WorkflowEntry
	agents    apiclient.Agents
	loaded    bool
	loadErr   error
	// hintProject is the project the caller was looking at; it wins over
	// "the only project" but loses to an explicit pick.
	hintProject int64
	// opened records that the form has been entered at least once, so a
	// reconnect refreshes it instead of probing adapters nobody asked about.
	opened bool

	projectID int64
	workflow  string
	titleIn   textinput.Model
	desc      textarea.Model
	fields    []kv
	branch    textinput.Model
	priority  textinput.Model
	agent     string
	model     string
	effort    string

	cursor   ntRow
	mode     ntMode
	pick     *picker
	fieldsEd *fieldsEditor

	// rowErr is the daemon's complaint parked on the row it named.
	rowErr map[ntRow]string
	// err is a failure that belongs to no single row.
	err        string
	submitting bool
	touched    bool

	width, height int
}

func newNewTask() *newTask {
	title := textinput.New()
	title.Placeholder = "what should the agent do?"
	branch := textinput.New()
	branch.Placeholder = "base branch"
	priority := textinput.New()
	priority.Placeholder = "0"
	priority.SetValue("0")
	desc := textarea.New()
	desc.Placeholder = "describe the task (markdown); e opens $EDITOR"
	desc.SetHeight(5)
	return &newTask{
		exec:     tea.ExecProcess,
		titleIn:  title,
		branch:   branch,
		priority: priority,
		desc:     desc,
		rowErr:   map[ntRow]string{},
	}
}

func (n *newTask) title() string { return "New task" }

// titleText is the trimmed title as it would be sent.
func (n *newTask) titleText() string { return strings.TrimSpace(n.titleIn.Value()) }

// setClient wires the form to a connected daemon. A form that was never
// opened does not load — a session that never presses n never probes an
// adapter — but one left open across a reconnect refetches, because its
// catalogs are as old as the connection that dropped.
func (n *newTask) setClient(c *apiclient.Client) tea.Cmd {
	n.client = c
	if !n.opened {
		return nil
	}
	return n.loadCmd(false)
}

// capturesInput reports that a text field owns the keyboard, so the global
// single-key bindings stand down while someone is typing a title.
func (n *newTask) capturesInput() bool {
	if n.mode == ntEditing {
		return true
	}
	return n.mode == ntPicking && n.pick != nil && n.pick.editing
}

// loadCmd fetches the three catalogs in one command. refresh forces a live
// adapter probe; the plain path rides the daemon's binary-identity cache.
func (n *newTask) loadCmd(refresh bool) tea.Cmd {
	client := n.client
	projectID := n.projectID
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		projects, err := client.ListProjects(ctx)
		if err != nil {
			return ntLoadedMsg{err: err}
		}
		agents, err := client.ListAgents(ctx, refresh)
		if err != nil {
			return ntLoadedMsg{err: err}
		}
		workflows, err := client.ListWorkflows(ctx, projectID)
		if err != nil {
			return ntLoadedMsg{err: err}
		}
		return ntLoadedMsg{projects: projects, workflows: workflows, agents: agents}
	}
}

func (n *newTask) workflowsCmd(projectID int64) tea.Cmd {
	client := n.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		entries, err := client.ListWorkflows(ctx, projectID)
		return ntWorkflowsMsg{projectID: projectID, entries: entries, err: err}
	}
}

func (n *newTask) update(msg tea.Msg) (view, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		n.width, n.height = msg.Width, msg.Height
		n.desc.SetWidth(max(20, msg.Width-8))
		return n, nil
	case newTaskMsg:
		return n, n.open(msg.projectID)
	case ntLoadedMsg:
		return n, n.applyLoaded(msg)
	case ntWorkflowsMsg:
		if msg.projectID == n.projectID && msg.err == nil {
			n.workflows = msg.entries
			n.selectDefaultWorkflow()
		}
		return n, nil
	case ntDescriptionMsg:
		n.applyDescription(msg)
		return n, nil
	case ntFailedMsg:
		n.applyFailure(msg.err)
		return n, nil
	case tea.KeyPressMsg:
		return n.updateKey(msg)
	}
	return n, nil
}

// open resets the draft and reloads the catalogs. Nothing is sticky between
// opens: a half-remembered draft from twenty minutes ago is a worse default
// than the context the user is standing in.
func (n *newTask) open(projectID int64) tea.Cmd {
	n.hintProject = projectID
	n.opened = true
	n.reset()
	return n.loadCmd(false)
}

func (n *newTask) reset() {
	n.titleIn.SetValue("")
	n.desc.SetValue("")
	n.branch.SetValue("")
	n.priority.SetValue("0")
	n.fields = nil
	n.agent, n.model, n.effort = "", "", ""
	n.workflow = ""
	n.projectID = 0
	n.cursor = ntProject
	n.mode = ntNavigating
	n.pick, n.fieldsEd = nil, nil
	n.rowErr = map[ntRow]string{}
	n.err, n.submitting, n.touched = "", false, false
	n.loaded, n.loadErr = false, nil
}

func (n *newTask) applyLoaded(msg ntLoadedMsg) tea.Cmd {
	if msg.err != nil {
		n.loadErr = msg.err
		return nil
	}
	n.projects, n.workflows, n.agents = msg.projects, msg.workflows, msg.agents
	n.loaded, n.loadErr = true, nil
	n.selectDefaultProject()
	n.selectDefaultWorkflow()
	// The project the picker settled on may not be the one the workflow list
	// was fetched for.
	if p, ok := n.project(); ok {
		return n.workflowsCmd(p.ID)
	}
	return nil
}

// selectDefaultProject prefers the project the caller was looking at, then
// the only project when there is exactly one. With several and no hint it
// leaves the row empty rather than guessing.
func (n *newTask) selectDefaultProject() {
	if n.projectID != 0 {
		return
	}
	for _, p := range n.projects {
		if p.ID == n.hintProject {
			n.setProject(p)
			return
		}
	}
	if len(n.projects) == 1 {
		n.setProject(n.projects[0])
	}
}

func (n *newTask) setProject(p apiclient.Project) {
	n.projectID = p.ID
	n.branch.SetValue(p.DefaultBranch)
}

// selectDefaultWorkflow points at the project's default, else adhoc, else
// the first valid entry — the same fallback handleTaskCreate applies.
func (n *newTask) selectDefaultWorkflow() {
	if n.workflow != "" && n.workflowEntry(n.workflow) != nil {
		return
	}
	p, ok := n.project()
	want := apiclient.AdhocWorkflow
	if ok {
		want = p.Workflow()
	}
	if e := n.workflowEntry(want); e != nil {
		n.workflow = e.Name
		return
	}
	for _, e := range n.workflows {
		if e.Valid() {
			n.workflow = e.Name
			return
		}
	}
	n.workflow = ""
}

func (n *newTask) project() (apiclient.Project, bool) {
	for _, p := range n.projects {
		if p.ID == n.projectID {
			return p, true
		}
	}
	return apiclient.Project{}, false
}

func (n *newTask) workflowEntry(name string) *apiclient.WorkflowEntry {
	for i := range n.workflows {
		if n.workflows[i].Name == name {
			return &n.workflows[i]
		}
	}
	return nil
}

func (n *newTask) updateKey(msg tea.KeyPressMsg) (view, tea.Cmd) {
	switch n.mode {
	case ntEditing:
		return n, n.updateEditing(msg)
	case ntPicking:
		return n, n.updatePicking(msg)
	case ntFieldsOpen:
		return n, n.updateFields(msg)
	case ntConfirming:
		return n.updateConfirm(msg)
	case ntNavigating:
	}
	return n.updateNavigating(msg)
}

func (n *newTask) updateNavigating(msg tea.KeyPressMsg) (view, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		n.moveCursor(-1)
	case "down", "j", "tab":
		n.moveCursor(1)
	case "shift+tab":
		n.moveCursor(-1)
	case "enter":
		return n, n.activate()
	case "e":
		if n.cursor == ntDescription {
			return n, n.editDescription()
		}
	case "R":
		return n, n.loadCmd(true)
	case "+", "=":
		n.nudgePriority(1)
	case "-", "_":
		n.nudgePriority(-1)
	case "ctrl+s":
		return n, n.submit()
	case "esc":
		return n.abandon()
	}
	return n, nil
}

func (n *newTask) moveCursor(delta int) {
	next := int(n.cursor) + delta
	if next < 0 {
		next = 0
	}
	if next >= int(ntRowCount) {
		next = int(ntRowCount) - 1
	}
	n.cursor = ntRow(next)
}

// activate opens whatever the focused row edits.
func (n *newTask) activate() tea.Cmd {
	switch n.cursor {
	case ntProject, ntWorkflow, ntAgent, ntModel, ntEffort:
		n.openPicker(n.cursor)
	case ntTitle, ntBranch, ntPriority, ntDescription:
		n.startEditing()
	case ntFields:
		n.fieldsEd = newFieldsEditor(n.fields)
		n.mode = ntFieldsOpen
	case ntCreate:
		return n.submit()
	case ntRowCount:
	}
	return nil
}

func (n *newTask) startEditing() {
	n.mode = ntEditing
	n.touched = true
	switch n.cursor {
	case ntTitle:
		n.titleIn.Focus()
	case ntBranch:
		n.branch.Focus()
	case ntPriority:
		n.priority.Focus()
	case ntDescription:
		n.desc.Focus()
	case ntProject, ntWorkflow, ntFields, ntAgent, ntModel, ntEffort, ntCreate, ntRowCount:
	}
}

func (n *newTask) stopEditing() {
	n.mode = ntNavigating
	n.titleIn.Blur()
	n.branch.Blur()
	n.priority.Blur()
	n.desc.Blur()
}

func (n *newTask) updateEditing(msg tea.KeyPressMsg) tea.Cmd {
	// esc leaves the field; the text stays. Abandoning the whole draft is
	// esc from the body, one level out — losing a paragraph to a stray esc
	// is not a thing a form should do.
	if msg.String() == "esc" {
		n.stopEditing()
		return nil
	}
	// A textarea needs enter for newlines; a single-line row commits on it.
	if msg.String() == "enter" && n.cursor != ntDescription {
		n.stopEditing()
		return nil
	}
	var cmd tea.Cmd
	switch n.cursor {
	case ntTitle:
		n.titleIn, cmd = n.titleIn.Update(msg)
	case ntBranch:
		n.branch, cmd = n.branch.Update(msg)
	case ntPriority:
		n.priority, cmd = n.priority.Update(msg)
	case ntDescription:
		n.desc, cmd = n.desc.Update(msg)
	case ntProject, ntWorkflow, ntFields, ntAgent, ntModel, ntEffort, ntCreate, ntRowCount:
	}
	delete(n.rowErr, n.cursor)
	return cmd
}

func (n *newTask) nudgePriority(delta int) {
	v, err := strconv.Atoi(strings.TrimSpace(n.priority.Value()))
	if err != nil {
		v = 0
	}
	n.priority.SetValue(strconv.Itoa(v + delta))
	n.touched = true
}

// abandon leaves the form. A touched draft asks first; an untouched one is
// nothing to lose.
func (n *newTask) abandon() (view, tea.Cmd) {
	if !n.touched {
		return n, func() tea.Msg { return selectViewMsg{id: viewBoard} }
	}
	n.mode = ntConfirming
	return n, nil
}

func (n *newTask) updateConfirm(msg tea.KeyPressMsg) (view, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		n.reset()
		return n, func() tea.Msg { return selectViewMsg{id: viewBoard} }
	default:
		n.mode = ntNavigating
		return n, nil
	}
}

// editDescription opens $EDITOR on the draft description, seeded with
// whatever is already typed.
func (n *newTask) editDescription() tea.Cmd {
	path, err := writeEditorFile("new-task-description", ".md", n.desc.Value())
	if err != nil {
		n.err = "description: " + errString(err)
		return nil
	}
	argv := append(editorCommand(), path)
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // the editor is the user's own choice
	return n.exec(cmd, func(runErr error) tea.Msg {
		defer func() { _ = os.Remove(path) }()
		if runErr != nil {
			return ntDescriptionMsg{err: runErr}
		}
		edited, readErr := os.ReadFile(path) //nolint:gosec // path is this process's temp file
		if readErr != nil {
			return ntDescriptionMsg{err: readErr}
		}
		return ntDescriptionMsg{text: string(edited)}
	})
}

func (n *newTask) applyDescription(msg ntDescriptionMsg) {
	if msg.err != nil {
		n.err = "description: " + errString(msg.err)
		return
	}
	n.err = ""
	n.desc.SetValue(msg.text)
	n.touched = true
}

// submit validates what the client can decide alone, then posts. Base branch
// and catalog membership are deliberately not checked here: the daemon
// validates both, and a second implementation would only drift from it.
func (n *newTask) submit() tea.Cmd {
	n.rowErr = map[ntRow]string{}
	n.err = ""
	if n.projectID == 0 {
		n.rowErr[ntProject] = "pick a project"
	}
	if n.titleText() == "" {
		n.rowErr[ntTitle] = "a title is required"
	}
	if e := n.workflowEntry(n.workflow); n.workflow != "" && e != nil && !e.Valid() {
		n.rowErr[ntWorkflow] = "this workflow does not validate: " + e.FirstError()
	}
	if len(n.rowErr) > 0 {
		return nil
	}
	if n.client == nil {
		n.err = "not connected"
		return nil
	}
	req := n.request()
	client := n.client
	n.submitting = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		task, err := client.CreateTask(ctx, req)
		if err != nil {
			return ntFailedMsg{err: err}
		}
		return taskCreatedMsg{task: task}
	}
}

// request builds the wire body. Every field the human left alone is omitted
// rather than sent empty, so the daemon's own fallbacks still apply.
func (n *newTask) request() apiclient.CreateTaskRequest {
	req := apiclient.CreateTaskRequest{ProjectID: n.projectID, Title: n.titleText()}
	if n.workflow != "" {
		req.Workflow = ptr(n.workflow)
	}
	if d := strings.TrimSpace(n.desc.Value()); d != "" {
		req.Description = ptr(d)
	}
	if f := n.fieldMap(); len(f) > 0 {
		req.Fields = f
	}
	if b := strings.TrimSpace(n.branch.Value()); b != "" {
		req.BaseBranch = ptr(b)
	}
	if p, err := strconv.Atoi(strings.TrimSpace(n.priority.Value())); err == nil && p != 0 {
		req.Priority = ptr(p)
	}
	if n.agent != "" {
		req.Agent = ptr(n.agent)
	}
	if n.model != "" {
		req.Model = ptr(n.model)
	}
	if n.effort != "" {
		req.Effort = ptr(n.effort)
	}
	return req
}

// fieldMap flattens the ordered rows. A later duplicate key wins, which is
// also what the editor shows.
func (n *newTask) fieldMap() map[string]string {
	if len(n.fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(n.fields))
	for _, f := range n.fields {
		if f.key != "" {
			out[f.key] = f.value
		}
	}
	return out
}

// applyFailure parks the daemon's message on the row it names. The server
// writes field names into its validation messages ("base_branch %q does not
// resolve…"), so the mapping reads them rather than guessing from the code.
func (n *newTask) applyFailure(err error) {
	n.submitting = false
	msg := errString(err)
	for _, m := range []struct {
		needle string
		row    ntRow
	}{
		{"base_branch", ntBranch},
		{"workflow", ntWorkflow},
		{"title", ntTitle},
		{"project", ntProject},
		{"model", ntModel},
		{"effort", ntEffort},
		{"agent", ntAgent},
	} {
		if strings.Contains(msg, m.needle) {
			n.rowErr[m.row] = msg
			n.cursor = m.row
			return
		}
	}
	n.err = msg
}

// unavailableSteps lists the agent steps of a workflow whose resolved agent
// is a known adapter that is not usable. A step with no agent resolves to
// the adapter default (§8.6 level 4), which the registry cannot report, so
// it is never accused.
func (n *newTask) unavailableSteps(e apiclient.WorkflowEntry) []string {
	var out []string
	for _, s := range e.Steps {
		if n.agents.Unavailable(s.Agent) {
			out = append(out, fmt.Sprintf("%s → %s", firstNonEmpty(s.Name, s.ID), s.Agent))
		}
	}
	return out
}

func ptr[T any](v T) *T { return &v }
