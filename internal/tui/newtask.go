package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// loadTimeout bounds the three catalog fetches the form opens with.
const loadTimeout = 10 * time.Second

// ntRow identifies one line of the form. The order is §15's: project →
// workflow → issue → title → description → fields → base branch → branch name →
// priority → agent/model/effort → create. Wide terminals group that order into
// six visual stages, but this remains the one cursor: back-navigation and
// review-before-submit do not need a second wizard state machine (task 020).
//
// ntIssue is **conditional**: it is present only when the daemon's capability
// probe says this project's issues can be read (task 035). Every other row is
// always there, so rowVisible is the one place that difference lives.
type ntRow int

const (
	ntProject ntRow = iota
	ntWorkflow
	ntIssue
	ntTitle
	ntDescription
	ntFields
	ntBranch
	ntBranchName
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
	ntNavigating   ntMode = iota // moving between rows
	ntEditing                    // a text row owns the keys
	ntPicking                    // a picker is open
	ntFieldsOpen                 // the key/value editor is open
	ntFieldPicking               // an enum field's value list is open over it
	ntConfirming                 // "discard this draft?"
)

// New-task messages.
type (
	// newTaskMsg opens the form, seeded with the project the caller was
	// looking at.
	newTaskMsg struct{ projectID int64 }
	// newTaskFromChatMsg opens the form as the handoff form for a chat
	// (task 074): the project is fixed, and the branch and base branch are
	// the chat's, shown but not editable.
	newTaskFromChatMsg struct{ chat apiclient.Chat }

	// newTaskFromPullMsg opens the form seeded with a pull request (task
	// 064). It carries the row the takeover was looking at, not a prefill:
	// the daemon computes prefills, and the form asks for one against the
	// workflow it settles on (035 decision 2).
	newTaskFromPullMsg struct {
		projectID int64
		pull      *apiclient.GitHubPullRequest
	}
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
	// ntResolvedMsg carries POST /v1/resolve for one draft state. The key
	// travels with it because the user keeps typing while it is in flight;
	// a reply for a draft that has moved on is dropped, not rendered.
	ntResolvedMsg struct {
		key        resolveKey
		resolution apiclient.Resolution
		err        error
	}
	// ntGitHubMsg carries the §13.2 capability probe for a project (task
	// 035). The project travels with it because the user may have switched
	// projects while it was in flight; a reply for another project is
	// dropped, not applied.
	ntGitHubMsg struct {
		projectID int64
		status    apiclient.GitHubStatus
		err       error
	}
	// ntIssuesMsg carries a project's issue listing, with the prefill the
	// daemon computed for the workflow that was selected when it was asked.
	// The key travels with it for the same reason.
	ntIssuesMsg struct {
		key    issuesKey
		issues []apiclient.GitHubIssue
		err    error
	}
	// ntPullPrefillMsg carries the listing a seeded draft's prefill is read
	// out of (task 064). The project, workflow and number travel with it so a
	// reply that no longer describes the draft is dropped rather than applied.
	ntPullPrefillMsg struct {
		projectID int64
		workflow  string
		number    int
		pulls     []apiclient.GitHubPullRequest
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

// kv is one task field row. A declared row carries the workflow's presentation
// and validation contract; a custom row leaves declared false. Both flatten to
// the same open map on the wire (task 022 decision 3).
type kv struct {
	key, value string
	declared   bool
	definition apiclient.WorkflowField
}

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
	// workflowPicked records that the Workflow row was chosen by hand, which
	// is the only thing that distinguishes it from a name the form derived
	// for itself. The registry is project-scoped (§5.2) and the first listing
	// is fetched before a project is selected, so a derived name must be
	// re-derived when the project-scoped catalog lands; a deliberate pick
	// must survive it.
	workflowPicked bool
	titleIn        textField
	desc           textarea.Model
	fields         []kv
	branch         textField
	// branchName is the task's own branch, distinct from branch above, which is
	// the *base* it forks from. Two adjacent rows about branches need labels that
	// cannot be misread as one having been renamed (task 001).
	branchName textField
	priority   textField
	agent      string
	model      string
	effort     string

	// github is the daemon's capability probe for githubProject (task 035).
	// The TUI holds no GitHub state the daemon does not have: it never parses
	// a remote, never reads a token, and never calls GitHub — it renders this
	// answer and asks the daemon for issues when it says yes.
	github        apiclient.GitHubStatus
	githubProject int64
	// issues is the listing the picker offers, and issuesFor the draft it was
	// fetched for. The workflow is part of that key because each row carries
	// the prefill computed against the workflow's declared fields.
	issues    []apiclient.GitHubIssue
	issuesFor issuesKey
	issuesErr string
	// issue is the issue this draft is linked to, nil when none. It is what
	// `github_issue` on the create request carries.
	issue *apiclient.GitHubIssue
	// pull is the pull request this draft was seeded from (task 064), nil for
	// every other draft. It is what `github_pull` carries, and unlike issue
	// it is never picked from inside the form: a task runs on a pull
	// request's head branch, which is a decision made where the pull request
	// is on screen.
	pull *apiclient.GitHubPullRequest
	// pullPrefilled records that the daemon's prefill for pull has landed, so
	// a second listing reply cannot overwrite rows the human has since edited.
	pullPrefilled bool
	// handoff is the chat this draft hands off (task 074), nil for every
	// other draft. It turns the form into the handoff form: the project is
	// the chat's, and the base branch and the branch are the chat's and are
	// shown read-only — they name a worktree that already exists, so there is
	// nothing here to decide. Submitting posts to the chat's handoff route
	// rather than to POST /v1/tasks.
	handoff *apiclient.Chat

	cursor   ntRow
	mode     ntMode
	pick     *picker
	fieldsEd *fieldsEditor

	// resolution is what the daemon says the committed draft would actually
	// run, per §8.6 — fetched, never derived: the TUI must not own a second
	// implementation of the precedence (PR L decision, T4.7). resolvedFor
	// records the draft it describes, so a stale reply cannot be shown.
	resolution  apiclient.Resolution
	resolvedFor resolveKey

	// rowErr is the daemon's complaint parked on the row it named.
	rowErr map[ntRow]string
	// err is a failure that belongs to no single row.
	err        string
	submitting bool
	touched    bool

	width, height int
}

func newNewTask() *newTask {
	title := newTextField()
	title.SetPlaceholder("what should the agent do?")
	branch := newTextField()
	branch.SetPlaceholder("base branch")
	priority := newTextField()
	priority.SetPlaceholder("0")
	priority.SetValue("0")
	desc := textarea.New()
	desc.Placeholder = "describe the task (markdown); e opens $EDITOR"
	desc.SetHeight(5)
	return &newTask{
		exec:    tea.ExecProcess,
		titleIn: title,
		branch:  branch,
		// Constructed here rather than left as a zero value: a text field owns
		// a viewport, so it has to be built before anything sizes or types
		// into it (issue #299).
		branchName: newTextField(),
		priority:   priority,
		desc:       desc,
		rowErr:     map[ntRow]string{},
	}
}

func (n *newTask) title() string {
	if n.handoff != nil {
		return "Hand off to task"
	}
	return "New task"
}

// inherited reports whether row names something the chat decided and this
// form may only display (task 074). The project, the base branch and the
// branch all belong to a worktree that already exists.
func (n *newTask) inherited(row ntRow) bool {
	if n.handoff == nil {
		return false
	}
	return row == ntProject || row == ntBranch || row == ntBranchName
}

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
	switch n.mode {
	case ntEditing:
		return true
	case ntPicking:
		// The filter types too. Omitting it here would repeat the M3 gate
		// finding below one row up: "q" typed into a picker filter would quit
		// the TUI mid-selection.
		return n.pick != nil && (n.pick.editing || n.pick.filtering)
	case ntFieldsOpen:
		// The key/value editor types too, and was missing here: a "q" typed
		// into a field name quit the TUI (M3 gate finding).
		return n.fieldsEd != nil && n.fieldsEd.editing != 0
	case ntFieldPicking:
		return n.pick != nil && (n.pick.editing || n.pick.filtering)
	case ntNavigating, ntConfirming:
	}
	return false
}

// paste hands pasted text to the field that owns the keyboard. Pasting a
// description or a branch name is the ordinary way to fill this form.
func (n *newTask) paste(text string) tea.Cmd {
	var cmd tea.Cmd
	switch n.mode {
	case ntEditing:
		switch n.cursor {
		case ntTitle:
			n.titleIn, cmd = n.titleIn.Update(tea.PasteMsg{Content: text})
		case ntBranch:
			n.branch, cmd = n.branch.Update(tea.PasteMsg{Content: text})
		case ntBranchName:
			n.branchName, cmd = n.branchName.Update(tea.PasteMsg{Content: text})
		case ntPriority:
			n.priority, cmd = n.priority.Update(tea.PasteMsg{Content: text})
		case ntDescription:
			n.desc, cmd = n.desc.Update(tea.PasteMsg{Content: text})
		case ntProject, ntWorkflow, ntIssue, ntFields, ntAgent, ntModel, ntEffort, ntCreate, ntRowCount:
			return nil
		}
		delete(n.rowErr, n.cursor)
	case ntPicking:
		if n.pick == nil {
			return nil
		}
		return n.pick.paste(text)
	case ntFieldsOpen:
		if n.fieldsEd == nil {
			return nil
		}
		return n.fieldsEd.paste(text)
	case ntFieldPicking:
		if n.pick == nil {
			return nil
		}
		return n.pick.paste(text)
	case ntNavigating, ntConfirming:
	}
	return cmd
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

// resolveKey identifies the draft state a resolution describes: everything
// POST /v1/resolve takes as input. Equality is the whole point — a reply
// whose key no longer matches the draft is discarded.
type resolveKey struct {
	projectID               int64
	workflow                string
	agent, model, effortSel string
	// The branch inputs, so a reply previewing a name the draft has moved past is
	// dropped like any other stale one (task 001). fields is digested rather than
	// held as a map because resolveKey is compared with ==.
	title, baseBranch, branchName, fields string
}

func (n *newTask) resolveKey() resolveKey {
	return resolveKey{
		projectID:  n.projectID,
		workflow:   n.workflow,
		agent:      n.agent,
		model:      n.model,
		effortSel:  n.effort,
		title:      n.titleText(),
		baseBranch: strings.TrimSpace(n.branch.Value()),
		branchName: strings.TrimSpace(n.branchName.Value()),
		fields:     fieldsDigest(n.fieldMap()),
	}
}

// fieldsDigest renders a field map as a comparable, order-independent string, so
// the map can take part in resolveKey's equality.
func fieldsDigest(f map[string]string) string {
	if len(f) == 0 {
		return ""
	}
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(f[k])
		b.WriteByte('\n')
	}
	return b.String()
}

// resolveCmd asks the daemon what the current draft would run. It fires on
// every change to the workflow or the override triple — five fields, one
// cheap loopback call each, and the alternative is a form that says
// "(workflow default)" about a spend decision (the T3.8 finding).
func (n *newTask) resolveCmd() tea.Cmd {
	client := n.client
	key := n.resolveKey()
	if client == nil || key.workflow == "" {
		return nil
	}
	req := apiclient.ResolveRequest{
		Workflow:   key.workflow,
		Agent:      key.agent,
		Model:      key.model,
		Effort:     key.effortSel,
		Title:      key.title,
		BaseBranch: key.baseBranch,
		BranchName: key.branchName,
		Fields:     n.fieldMap(),
	}
	if key.projectID != 0 {
		req.ProjectID = ptr(key.projectID)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		res, err := client.Resolve(ctx, req)
		return ntResolvedMsg{key: key, resolution: res, err: err}
	}
}

// applyResolution takes a reply only when it still describes the draft. A
// failure clears the resolution rather than keeping a stale one: the summary
// lines fall back to naming no resolved value, which is honest, where stale
// text would name the wrong model.
func (n *newTask) applyResolution(msg ntResolvedMsg) {
	if msg.key != n.resolveKey() {
		return
	}
	if msg.err != nil {
		n.resolution, n.resolvedFor = apiclient.Resolution{}, resolveKey{}
		return
	}
	n.resolution, n.resolvedFor = msg.resolution, msg.key
}

// resolved reports the resolution when it describes the draft on screen.
func (n *newTask) resolved() (apiclient.Resolution, bool) {
	if n.resolvedFor == (resolveKey{}) || n.resolvedFor != n.resolveKey() {
		return apiclient.Resolution{}, false
	}
	return n.resolution, true
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

func (n *newTask) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		n.width, n.height = msg.Width, msg.Height
		n.desc.SetWidth(max(20, msg.Width-8))
		return n, nil
	case newTaskMsg:
		return n, n.open(msg.projectID)
	case newTaskFromPullMsg:
		cmd := n.open(msg.projectID)
		n.pull = msg.pull
		return n, cmd
	case newTaskFromChatMsg:
		// open() resets first, so the inherited values are written after it,
		// exactly as the pull-request seed is.
		cmd := n.open(msg.chat.ProjectID)
		chat := msg.chat
		n.handoff = &chat
		n.titleIn.SetValue(chat.Title)
		n.branch.SetValue(chat.BaseBranch)
		n.branchName.SetValue(chat.Branch)
		// The title is the one seeded row worth landing on: it is the task's
		// objective, and the chat's title was written for a conversation.
		n.cursor = ntTitle
		return n, cmd
	case ntLoadedMsg:
		return n, n.applyLoaded(msg)
	case ntWorkflowsMsg:
		if msg.projectID == n.projectID && msg.err == nil {
			n.workflows = msg.entries
			n.selectDefaultWorkflow()
			return n, tea.Batch(n.resolveCmd(), n.issuesCmd(), n.pullPrefillCmd())
		}
		return n, nil
	case ntResolvedMsg:
		n.applyResolution(msg)
		return n, nil
	case ntGitHubMsg:
		return n, n.applyGitHub(msg)
	case ntPullPrefillMsg:
		n.applyPullPrefill(msg)
		return n, nil
	case ntIssuesMsg:
		n.applyIssues(msg)
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
	n.workflow, n.workflowPicked = "", false
	n.projectID = 0
	n.cursor = ntProject
	n.mode = ntNavigating
	n.pick, n.fieldsEd = nil, nil
	n.rowErr = map[ntRow]string{}
	n.err, n.submitting, n.touched = "", false, false
	n.loaded, n.loadErr = false, nil
	n.github, n.githubProject = apiclient.GitHubStatus{}, 0
	n.issues, n.issuesFor, n.issuesErr, n.issue = nil, issuesKey{}, "", nil
	n.handoff, n.pull, n.pullPrefilled = nil, nil, false
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
		return tea.Batch(n.workflowsCmd(p.ID), n.githubCmd(p.ID))
	}
	return n.resolveCmd()
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
	if n.workflowPicked && n.workflow != "" && n.workflowEntry(n.workflow) != nil {
		n.syncWorkflowFields()
		return
	}
	p, ok := n.project()
	want := apiclient.AdhocWorkflow
	if ok {
		want = p.Workflow()
	}
	if e := n.workflowEntry(want); e != nil {
		n.setWorkflow(e.Name)
		return
	}
	for _, e := range n.workflows {
		if e.Valid() && e.RunsHere() {
			n.setWorkflow(e.Name)
			return
		}
	}
	n.setWorkflow("")
}

func (n *newTask) setWorkflow(name string) {
	n.workflow = name
	n.syncWorkflowFields()
}

// syncWorkflowFields rebuilds the declared prefix from the selected workflow
// while preserving every value already typed. Non-empty rows no longer
// declared become ordinary custom fields instead of being discarded; untouched
// declaration chrome disappears. A custom row whose name the new workflow
// declares becomes that locked declared row (task 022).
func (n *newTask) syncWorkflowFields() {
	values := make(map[string]string, len(n.fields))
	for _, field := range n.fields {
		values[field.key] = field.value
	}

	entry := n.workflowEntry(n.workflow)
	declared := map[string]bool{}
	out := make([]kv, 0, len(n.fields))
	if entry != nil && entry.Valid() {
		out = make([]kv, 0, len(entry.Fields)+len(n.fields))
		for _, definition := range entry.Fields {
			declared[definition.Name] = true
			// A declared `default:` seeds the row, but only where the human
			// has not already put something there: switching workflow to
			// compare two must not silently overwrite a value that was
			// typed. Optional defaults are seeded here and nowhere else —
			// the daemon never invents one (§8.1.2, task 058 decision 3).
			value, typed := values[definition.Name]
			if !typed || value == "" {
				value = definition.Default
			}
			out = append(out, kv{
				key: definition.Name, value: value,
				declared: true, definition: definition,
			})
		}
	}
	for _, field := range n.fields {
		if field.key == "" || declared[field.key] || (field.declared && field.value == "") {
			continue
		}
		out = append(out, kv{key: field.key, value: field.value})
	}
	n.fields = out
}

func (n *newTask) project() (apiclient.Project, bool) {
	for _, p := range n.projects {
		if p.ID == n.projectID {
			return p, true
		}
	}
	return apiclient.Project{}, false
}

// workflowNeedsInputAgent reports whether the selected workflow constrains the
// agent choice: some step declares `on_input: require` and leaves its agent to
// the task (§7.4, task 013). The daemon derives it; this only reads the flag.
func (n *newTask) workflowNeedsInputAgent() bool {
	e := n.workflowEntry(n.workflow)
	return e != nil && e.Valid() && e.RequiresInput
}

func (n *newTask) workflowEntry(name string) *apiclient.WorkflowEntry {
	for i := range n.workflows {
		if n.workflows[i].Name == name {
			return &n.workflows[i]
		}
	}
	return nil
}

func (n *newTask) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	switch n.mode {
	case ntEditing:
		return n, n.updateEditing(msg)
	case ntPicking:
		return n, n.updatePicking(msg)
	case ntFieldsOpen:
		return n, n.updateFields(msg)
	case ntFieldPicking:
		return n, n.updateFieldPicking(msg)
	case ntConfirming:
		return n.updateConfirm(msg)
	case ntNavigating:
	}
	return n.updateNavigating(msg)
}

func (n *newTask) updateNavigating(msg tea.KeyPressMsg) (panel, tea.Cmd) {
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

// moveCursor walks to the next *visible* row. A hidden row is stepped over
// rather than landed on: ntIssue is absent whenever the daemon says this
// project's issues cannot be read, and a cursor parked on a row nothing draws
// is a form that appears to swallow keystrokes.
func (n *newTask) moveCursor(delta int) {
	if delta == 0 {
		return
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	next := int(n.cursor)
	for range abs(delta) {
		candidate := next + step
		for candidate >= 0 && candidate < int(ntRowCount) && !n.rowVisible(ntRow(candidate)) {
			candidate += step
		}
		if candidate < 0 || candidate >= int(ntRowCount) {
			break
		}
		next = candidate
	}
	n.cursor = ntRow(next)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// rowVisible reports whether a row is part of this draft's form.
//
// Only ntIssue is ever hidden, and only on the daemon's word: the integration
// is disabled, the project's origin is not a github.com repository, or GitHub
// cannot be reached. In all three the row is simply absent — the form does not
// offer a control that would fail (task 035).
func (n *newTask) rowVisible(row ntRow) bool {
	// A handoff has a source already — the chat — so the issue row would be a
	// second one competing to prefill the same title and description.
	if n.handoff != nil && row == ntIssue {
		return false
	}
	if row != ntIssue {
		return true
	}
	return n.githubAvailable()
}

// githubAvailable reports the probe's verdict for the project on screen. A
// probe for a different project is not an answer about this one.
func (n *newTask) githubAvailable() bool {
	return n.projectID != 0 && n.githubProject == n.projectID && n.github.Available
}

// visibleRows lists the rows this draft draws, in order.
func (n *newTask) visibleRows() []ntRow {
	out := make([]ntRow, 0, int(ntRowCount))
	for row := ntProject; row < ntRowCount; row++ {
		if n.rowVisible(row) {
			out = append(out, row)
		}
	}
	return out
}

// activate opens whatever the focused row edits.
func (n *newTask) activate() tea.Cmd {
	if n.inherited(n.cursor) {
		return nil
	}
	switch n.cursor {
	case ntProject, ntWorkflow, ntIssue, ntAgent, ntModel, ntEffort:
		n.openPicker(n.cursor)
	case ntTitle, ntBranch, ntBranchName, ntPriority, ntDescription:
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
	case ntProject, ntWorkflow, ntIssue, ntFields, ntAgent, ntModel, ntEffort, ntCreate, ntRowCount:
	}
}

func (n *newTask) stopEditing() {
	n.mode = ntNavigating
	n.titleIn.Blur()
	n.branch.Blur()
	n.branchName.Blur()
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
	case ntBranchName:
		n.branchName, cmd = n.branchName.Update(msg)
	case ntPriority:
		n.priority, cmd = n.priority.Update(msg)
	case ntDescription:
		n.desc, cmd = n.desc.Update(msg)
	case ntProject, ntWorkflow, ntIssue, ntFields, ntAgent, ntModel, ntEffort, ntCreate, ntRowCount:
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
func (n *newTask) abandon() (panel, tea.Cmd) {
	if !n.touched {
		return n, func() tea.Msg { return selectViewMsg{id: viewHome} }
	}
	n.mode = ntConfirming
	return n, nil
}

func (n *newTask) updateConfirm(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		n.reset()
		return n, func() tea.Msg { return selectViewMsg{id: viewHome} }
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
	if n.projectID == 0 && n.handoff == nil {
		n.rowErr[ntProject] = "pick a project"
	}
	if n.titleText() == "" {
		n.rowErr[ntTitle] = "a title is required"
	}
	if message := n.firstFieldError(); message != "" {
		n.rowErr[ntFields] = message
	}
	if e := n.workflowEntry(n.workflow); n.workflow != "" && e != nil {
		switch {
		case !e.Valid():
			n.rowErr[ntWorkflow] = "this workflow does not validate: " + e.FirstError()
		case !e.RunsHere():
			n.rowErr[ntWorkflow] = "this workflow does not run on this platform — it " + e.PlatformNote()
		}
	}
	// The §7.4 `require` gate, checked locally so the message lands on the
	// agent row rather than arriving as a create error with nowhere to point
	// (task 013). The daemon re-checks it: this is a courtesy, not the gate.
	if n.workflowNeedsInputAgent() && n.agent != "" {
		if a, ok := n.agents.Find(n.agent); ok && a.CannotTakeInput() {
			n.rowErr[ntAgent] = "this workflow needs an agent that can answer questions mid-run; " +
				n.agent + " cannot"
		}
	}
	if len(n.rowErr) > 0 {
		// In the guided layout only the cursor's stage is visible. Move to the
		// first invalid row so a required workflow field cannot fail submit on
		// a different, hidden stage.
		for row := ntProject; row < ntRowCount; row++ {
			if _, invalid := n.rowErr[row]; invalid {
				n.cursor = row
				break
			}
		}
		return nil
	}
	if n.client == nil {
		n.err = "not connected"
		return nil
	}
	req := n.request()
	client := n.client
	n.submitting = true
	if n.handoff != nil {
		// The chat's route, not POST /v1/tasks (task 074 decision 1). The
		// body is the same one; the daemon supplies the project, the base
		// branch and the branch from the chat and ignores whatever is here.
		chatID := n.handoff.ID
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
			defer cancel()
			task, _, err := client.HandoffChat(ctx, chatID, req)
			if err != nil {
				return ntFailedMsg{err: err}
			}
			return taskCreatedMsg{task: task}
		}
	}
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
	if n.pull != nil {
		number := n.pull.Number
		req.GitHubPull = &number
		// Same explicit-wins reasoning as a linked issue below: what is on
		// screen is sent, empties included, or the daemon would put its own
		// prefill back into a row the human deliberately cleared.
		req.Description = ptr(n.desc.Value())
		req.Fields = n.issueFieldMap()
	}
	if n.issue != nil {
		number := n.issue.Number
		req.GitHubIssue = &number
		// A linked draft sends what is on screen *explicitly*, empties
		// included, because the daemon fills in anything the request leaves
		// unset (task 035 decision 2). Omitting a description or a declared
		// field the human deliberately cleared would have the daemon put the
		// prefill straight back — which is the one way the form's "nothing is
		// locked" promise could quietly become false.
		req.Description = ptr(n.desc.Value())
		req.Fields = n.issueFieldMap()
	}
	if b := strings.TrimSpace(n.branch.Value()); b != "" {
		req.BaseBranch = ptr(b)
	}
	if b := strings.TrimSpace(n.branchName.Value()); b != "" {
		req.BranchName = ptr(b)
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

// issueFieldMap is fieldMap for a draft linked to an issue: every named row
// is sent, including the ones whose value is empty. See request() — an omitted
// key is a key the daemon prefills.
func (n *newTask) issueFieldMap() map[string]string {
	out := make(map[string]string, len(n.fields))
	for _, f := range n.fields {
		if f.key != "" {
			out[f.key] = f.value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fieldMap flattens the ordered rows. Untouched declarations are form chrome,
// not task data, so they stay absent; an explicitly added custom field keeps
// its empty value for compatibility. A later duplicate key wins, which is also
// what the editor shows.
func (n *newTask) fieldMap() map[string]string {
	if len(n.fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(n.fields))
	for _, f := range n.fields {
		if f.key != "" && (!f.declared || f.value != "") {
			out[f.key] = f.value
		}
	}
	if len(out) == 0 {
		return nil
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
		{"fields.", ntFields},
		{"base_branch", ntBranch},
		{"branch", ntBranchName},
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
// is a known adapter that is not usable.
//
// With a resolution to hand, a step naming no agent is checked too: §8.6
// level 4 still runs an adapter, and "the default one is missing" is exactly
// the warning that used to be impossible to give. Without one, such a step
// is reported, never accused — the registry cannot say what it resolves to.
func (n *newTask) unavailableSteps(e apiclient.WorkflowEntry) []string {
	res, resolved := n.resolved()
	resolved = resolved && e.Name == n.workflow
	var out []string
	for i, s := range e.Steps {
		name := s.Agent
		if resolved && i < len(res.Steps) && res.Steps[i].Agent != nil {
			name = res.Steps[i].Agent.Value
		}
		if n.agents.Unavailable(name) {
			out = append(out, fmt.Sprintf("%s → %s", firstNonEmpty(s.Name, s.ID), name))
		}
	}
	return out
}

func ptr[T any](v T) *T { return &v }
