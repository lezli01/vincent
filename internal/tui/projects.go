package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// forceHint is the phrase handleProjectDelete uses when force is the remedy.
// The daemon answers 409 for two different situations and only one of them
// is a question: "pass ?force" is the server naming its own way out, so the
// confirmation keys on that rather than on the status code, which cannot
// tell "archive these and continue?" from "cancel the running task first".
const forceHint = "?force"

// Projects messages.
type (
	projectsRefreshMsg struct{}
	projectsLoadedMsg  struct {
		stamp    loadStamp
		projects []apiclient.Project
		tasks    []apiclient.Task
		info     apiclient.Info
		infoOK   bool
		err      error
	}
	// projectSavedMsg reports a completed create or patch.
	projectSavedMsg struct {
		project apiclient.Project
		err     error
	}
	// overviewPickMsg is enter on one of the overview's rows (task 132
	// decision 42): select project and go back to the last project-scoped
	// view, or — when task is set, from the "needs you" list — select the
	// task's project and open the task. The root does both, because the
	// selection and the view stack are its.
	overviewPickMsg struct {
		project apiclient.Project
		task    *apiclient.Task
	}
	// projectDeletedMsg reports a completed delete attempt. forced records
	// which of the two requests came back, so a 409 on the forced one is
	// never re-offered as a confirmation.
	projectDeletedMsg struct {
		id     int64
		forced bool
		err    error
	}
)

// deletePrompt is an inline confirmation. force is what the *next* request
// carries, so the first prompt asks about the project and the second — when
// the daemon says tasks are in the way — asks about the tasks.
type deletePrompt struct {
	id    int64
	name  string
	text  string
	force bool
}

// projectsView is §15's view 4, the project overview (task 132.15): one row
// of figures per registered project, a totals row, the tasks that need a
// human across every project, and the add/edit/remove flows. It is the only
// multi-project view; every other one shows the selected project.
type projectsView struct {
	client *apiclient.Client
	now    func() time.Time

	// projects carry their `stats` (GET /v1/projects?stats=true, task
	// 132.1). Every figure on a row is the daemon's; nothing is counted
	// here, so a row never disagrees with the picker or `vincent project
	// list --stats`.
	projects []apiclient.Project
	// tasks is the full root listing, read only for the "needs you" list.
	tasks []apiclient.Task
	// globalCap is the daemon-wide limit. It is shown beside a project with
	// no cap of its own because that is the ceiling actually holding tasks
	// back — the two caps are independent, not a fallback chain.
	globalCap int
	// slotsUsed is /v1/info's installation-wide slot count, the totals row's
	// numerator. It is never a sum of the rows (#324's rule, §13.2).
	slotsUsed int
	infoOK    bool
	// github is the root's §13.2 probe per project, kept from the
	// githubProbeMsg it broadcasts (task 132 decision 44).
	github map[int64]apiclient.GitHubStatus

	// active is whether the overview is the visible view: it fetches on
	// activation and on events only while it is (task 132.15).
	active bool

	// inAttention is the cursor being in the "needs you" list rather than
	// the table; attnCursor is its row there.
	inAttention bool
	attnCursor  int

	loaded   bool
	loadErr  error
	lastLoad time.Time

	tbl        table.Model
	selectedID int64
	// overviewCols and overviewName are the column set and name width the
	// table was last fitted to, so the totals row lines up under it.
	overviewCols []ovCol
	overviewName int

	filter    textField
	filtering bool

	form    *projectForm
	confirm *deletePrompt
	// err is a failure that belongs to the view rather than a form row —
	// including the 409 no confirmation can resolve.
	err string

	// stamps orders the view's loads. It is seq-only: every stamp carries
	// project 0 (task 132.5).
	stamps loadStamps

	refreshPending bool
	width, height  int
}

func newProjectsView() *projectsView {
	fi := newTextField()
	fi.SetPlaceholder("filter by name or path")
	fi.SetPrompt("/")
	p := &projectsView{
		now:    time.Now,
		filter: fi,
		tbl:    table.New(table.WithFocused(true)),
	}
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true)
	s.Selected = tableSelectedStyle()
	p.tbl.SetStyles(s)
	return p
}

func (p *projectsView) title() string { return "Projects" }

// setClient fetches only while the overview is on screen: its stats are
// worth a request only while someone is reading them, and activation
// refetches anyway.
func (p *projectsView) setClient(c *apiclient.Client) tea.Cmd {
	p.client = c
	if !p.active {
		return nil
	}
	return p.loadCmd()
}

// capturesInput is true only while a text field owns the keyboard: the
// filter, a form row being typed into, or a picker's free-text entry. The
// confirmations are single-key, so they leave the global bindings alone.
func (p *projectsView) capturesInput() bool {
	if p.filtering {
		return true
	}
	return p.form != nil && p.form.capturesInput()
}

// paste hands pasted text to the field that owns the keyboard: a form row
// being typed into — pasting the repository path is how a project gets
// registered without leaving the TUI (§19 M3) — or the filter.
func (p *projectsView) paste(text string) tea.Cmd {
	if p.form != nil {
		return p.form.paste(text)
	}
	if !p.filtering {
		return nil
	}
	var cmd tea.Cmd
	p.filter, cmd = p.filter.Update(tea.PasteMsg{Content: text})
	return cmd
}

// hintedProject lets `n` open the new-task form on the row under the cursor.
func (p *projectsView) hintedProject() int64 {
	id, ok := p.selected()
	if !ok {
		return 0
	}
	return id
}

func (p *projectsView) loadCmd() tea.Cmd {
	client := p.client
	if client == nil {
		return nil
	}
	// Project 0 always: the overview is not project-bearing (task 132
	// decision 5), so its stamp only orders its loads (decision 26).
	stamp := p.stamps.next(0)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		projects, err := client.ListProjects(ctx, apiclient.WithStats())
		if err != nil {
			return projectsLoadedMsg{stamp: stamp, err: err}
		}
		// The figures ride on the project rows themselves. The task list
		// feeds the "needs you" list and the slots come from info; neither
		// failing is a reason to hide the projects, so only the project
		// fetch can fail the load.
		tasks, taskErr := client.ListTasks(ctx, apiclient.ListTasksOptions{})
		if taskErr != nil {
			tasks = nil
		}
		info, infoErr := client.Info(ctx)
		return projectsLoadedMsg{
			stamp:    stamp,
			projects: projects, tasks: tasks,
			info: info, infoOK: infoErr == nil,
		}
	}
}

func (p *projectsView) scheduleRefresh() tea.Cmd {
	if p.refreshPending {
		return nil
	}
	p.refreshPending = true
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return projectsRefreshMsg{} })
}

func (p *projectsView) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
		return p, nil
	case viewActivatedMsg:
		if msg.id == viewProjects {
			// The overview does not fetch while hidden, so activation is
			// what keeps "off-screen" from meaning "stale".
			p.active = true
			return p, p.loadCmd()
		}
		return p, nil
	case viewDeactivatedMsg:
		if msg.id == viewProjects {
			p.active = false
		}
		return p, nil
	case projectsRefreshMsg:
		p.refreshPending = false
		if !p.active {
			return p, nil
		}
		return p, p.loadCmd()
	case githubProbeMsg:
		if msg.err == nil {
			p.github = make(map[int64]apiclient.GitHubStatus, len(msg.projects))
			for _, gp := range msg.projects {
				p.github[gp.project.ID] = gp.status
			}
		}
		return p, nil
	case projectsLoadedMsg:
		p.applyLoaded(msg)
		return p, nil
	case noteMsg:
		return p, p.updateNote(msg.note)
	case projectSavedMsg:
		return p, p.applySaved(msg)
	case projectDeletedMsg:
		return p, p.applyDeleted(msg)
	case projectFormClosedMsg:
		p.form = nil
		return p, nil
	case pfWorkflowsMsg:
		if p.form != nil && msg.err == nil {
			p.form.workflows = msg.entries
		}
		return p, nil
	case tea.KeyPressMsg:
		return p.updateKey(msg)
	}
	return p, nil
}

func (p *projectsView) applyLoaded(msg projectsLoadedMsg) {
	if !p.stamps.accepts(msg.stamp) {
		return // a slower, older load landing after a newer one
	}
	p.stamps.apply(msg.stamp)
	if msg.err != nil {
		// Keep the rows on screen: a failed refresh is not a lost list.
		p.loadErr = msg.err
		return
	}
	p.loadErr = nil
	p.loaded = true
	p.lastLoad = p.now()
	p.projects = msg.projects
	p.tasks = msg.tasks
	if msg.infoOK {
		p.globalCap, p.slotsUsed, p.infoOK = msg.info.MaxParallelTasks, msg.info.Slots.Used, true
	}
	p.clampAttention()
	if p.form != nil {
		p.form.refreshFrom(msg.projects)
	}
}

// updateNote refetches, debounced and only while the overview is visible, on
// the events that move what it shows: task.*, issue.* and chat.* move the
// figures, project.* the rows, and the registry changing moves the
// default-workflow picker's options.
func (p *projectsView) updateNote(n apiclient.Note) tea.Cmd {
	ev, ok := n.(apiclient.EventNote)
	if !ok || !p.active {
		return nil
	}
	if projectPickerEvent(ev.Event.Type) || ev.Event.Type == eventWorkflowRegistryChanged {
		return p.scheduleRefresh()
	}
	return nil
}

func (p *projectsView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	if p.form != nil {
		return p, p.form.updateKey(msg)
	}
	if p.confirm != nil {
		return p.updateConfirm(msg)
	}
	if p.filtering {
		switch msg.String() {
		case "esc":
			p.filtering = false
			p.filter.SetValue("")
			p.filter.Blur()
			return p, nil
		case "enter":
			p.filtering = false
			p.filter.Blur()
			return p, nil
		}
		var cmd tea.Cmd
		p.filter, cmd = p.filter.Update(msg)
		return p, cmd
	}

	switch msg.String() {
	case opKey(keymap.Filter):
		p.filtering = true
		p.filter.Focus()
		return p, nil
	case "esc":
		// One layer per press (§15): a committed filter or an error note
		// clears first; with nothing left, the takeover closes.
		if p.filter.Value() != "" || p.err != "" {
			p.filter.SetValue("")
			p.err = ""
			return p, nil
		}
		return p, func() tea.Msg { return selectViewMsg{id: viewHome} }
	case opKey(keymap.Add):
		p.err = ""
		p.form = newProjectForm(p.client, nil)
		return p, p.form.loadCmd()
	case "tab", "shift+tab":
		// Two sections, one cursor: tab moves it between the table and the
		// "needs you" list, the way the daemon and triggers takeovers move
		// between their two panes. An empty list is not somewhere to go.
		p.inAttention = !p.inAttention && len(p.attention()) > 0
		return p, nil
	case "enter":
		return p, p.pick()
	case "e":
		if pr, ok := p.current(); ok {
			p.err = ""
			p.form = newProjectForm(p.client, &pr)
			return p, p.form.loadCmd()
		}
		return p, nil
	case opKey(keymap.Delete):
		// `D` destroys a persisted record after a confirmation, which is what
		// it means on both archived boards too (task 093).
		p.askDelete()
		return p, nil
	}

	if p.inAttention {
		switch msg.String() {
		case "up", "k":
			p.attnCursor = max(p.attnCursor-1, 0)
		case "down", "j":
			p.attnCursor = min(p.attnCursor+1, max(len(p.attention())-1, 0))
		}
		return p, nil
	}
	var cmd tea.Cmd
	p.tbl, cmd = p.tbl.Update(msg)
	p.rememberSelection()
	return p, cmd
}

// pick is enter (task 132 decision 42): on a project row, that project; on
// a "needs you" row, the task and its project. Either way the root does the
// selecting, through selectProject.
func (p *projectsView) pick() tea.Cmd {
	if p.inAttention {
		attn := p.attention()
		if len(attn) == 0 {
			return nil
		}
		t := attn[min(p.attnCursor, len(attn)-1)]
		msg := overviewPickMsg{project: p.projectOf(t), task: &t}
		return func() tea.Msg { return msg }
	}
	pr, ok := p.current()
	if !ok {
		return nil
	}
	return func() tea.Msg { return overviewPickMsg{project: pr} }
}

// projectOf is a task's project as selectProject wants it. A task whose
// project is not (yet) in the listing still carries its own id and name.
func (p *projectsView) projectOf(t apiclient.Task) apiclient.Project {
	for _, pr := range p.projects {
		if pr.ID == t.ProjectID {
			return pr
		}
	}
	return apiclient.Project{ID: t.ProjectID, Name: t.ProjectName}
}

// attention is "needs you, across projects": the tasks the board's `!`
// filter keeps (keepsAttention), in the board's order — oldest wait first —
// over the full listing. It is the one cross-project task list the TUI
// draws (task 132.15).
func (p *projectsView) attention() []apiclient.Task {
	sorted := append([]apiclient.Task(nil), p.tasks...)
	sortTasks(sorted)
	return attentionTasks(sorted)
}

// clampAttention keeps the list's cursor on a row after a refresh shrank it,
// and leaves the list when it emptied.
func (p *projectsView) clampAttention() {
	n := len(p.attention())
	if n == 0 {
		p.inAttention, p.attnCursor = false, 0
		return
	}
	p.attnCursor = min(p.attnCursor, n-1)
}

// askDelete opens the first confirmation. Deleting cascades to task rows and
// removes worktrees, so it always asks — even for a project with nothing in
// it, which is the case where the mistake is easiest to make.
func (p *projectsView) askDelete() {
	pr, ok := p.current()
	if !ok {
		return
	}
	p.err = ""
	p.confirm = &deletePrompt{
		id:   pr.ID,
		name: pr.Name,
		text: fmt.Sprintf("delete %q and its task history? this cannot be undone", pr.Name),
	}
}

func (p *projectsView) updateConfirm(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	c := p.confirm
	switch msg.String() {
	case "y", "Y":
		p.confirm = nil
		return p, p.deleteCmd(c.id, c.force)
	default:
		p.confirm = nil
		return p, nil
	}
}

func (p *projectsView) deleteCmd(id int64, force bool) tea.Cmd {
	client := p.client
	if client == nil {
		p.err = "not connected"
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		err := client.DeleteProject(ctx, id, force)
		return projectDeletedMsg{id: id, forced: force, err: err}
	}
}

// applyDeleted turns the daemon's answer into either a second question or an
// error. A 409 that names ?force is the server offering a way through, and
// only when the request that got it was unforced; anything else — a running
// task, most importantly — is a refusal, and re-asking would be offering a
// "yes" that cannot work.
func (p *projectsView) applyDeleted(msg projectDeletedMsg) tea.Cmd {
	if msg.err == nil {
		p.err = ""
		return p.loadCmd()
	}
	text := errString(msg.err)
	if !msg.forced && strings.Contains(text, forceHint) {
		p.confirm = &deletePrompt{
			id:    msg.id,
			name:  p.nameOf(msg.id),
			text:  strings.TrimSuffix(text, "; pass ?force to archive them and delete anyway") + " — archive them and delete anyway?",
			force: true,
		}
		return nil
	}
	p.err = text
	return nil
}

func (p *projectsView) applySaved(msg projectSavedMsg) tea.Cmd {
	if msg.err != nil {
		// The form stays open with the daemon's message on the row it named:
		// a rejected save that discarded the draft would be its own bug.
		if p.form != nil {
			p.form.applyFailure(msg.err)
		}
		return nil
	}
	p.form = nil
	p.selectedID = msg.project.ID
	return p.loadCmd()
}

func (p *projectsView) nameOf(id int64) string {
	for _, pr := range p.projects {
		if pr.ID == id {
			return pr.Name
		}
	}
	return strconv.FormatInt(id, 10)
}

// visible is the filtered list currently on screen.
func (p *projectsView) visible() []apiclient.Project {
	q := strings.ToLower(strings.TrimSpace(p.filter.Value()))
	if q == "" {
		return p.projects
	}
	out := make([]apiclient.Project, 0, len(p.projects))
	for _, pr := range p.projects {
		if strings.Contains(strings.ToLower(pr.Name), q) ||
			strings.Contains(strings.ToLower(pr.Path), q) {
			out = append(out, pr)
		}
	}
	return out
}

func (p *projectsView) selected() (int64, bool) {
	rows := p.visible()
	i := p.tbl.Cursor()
	if i < 0 || i >= len(rows) {
		return 0, false
	}
	return rows[i].ID, true
}

func (p *projectsView) current() (apiclient.Project, bool) {
	id, ok := p.selected()
	if !ok {
		return apiclient.Project{}, false
	}
	for _, pr := range p.visible() {
		if pr.ID == id {
			return pr, true
		}
	}
	return apiclient.Project{}, false
}

func (p *projectsView) rememberSelection() {
	if id, ok := p.selected(); ok {
		p.selectedID = id
	}
}

func (p *projectsView) restoreSelection(rows []apiclient.Project) {
	if p.selectedID == 0 {
		return
	}
	for i := range rows {
		if rows[i].ID == p.selectedID {
			p.tbl.SetCursor(i)
			return
		}
	}
	p.tbl.SetCursor(0)
}

// capCell renders the cap the way the scheduler actually applies it. A
// project with no cap of its own is not capped at the global figure — it
// competes for the whole pool — so the two read differently.
//
// The numerator is the daemon's own figure (`slots_used`, §11), never a walk
// of p.tasks: the cap counts every task in a slot-holding state —
// `awaiting_input` as well as `running`, lanes as well as roots
// (store.CountSlotHoldersByProject) — while /v1/tasks omits fan-out lanes by
// default (task 014 decision 13, §13.2). Counting here undercounted the cap
// by exactly the tasks holding it up (issue #324).
func (p *projectsView) capCell(pr apiclient.Project) string {
	if pr.MaxParallelTasks != nil {
		return fmt.Sprintf("%d / %d", pr.SlotsUsed, *pr.MaxParallelTasks)
	}
	if p.infoOK {
		return fmt.Sprintf("%d / — (global %d)", pr.SlotsUsed, p.globalCap)
	}
	return fmt.Sprintf("%d / —", pr.SlotsUsed)
}
