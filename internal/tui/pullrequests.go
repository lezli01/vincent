package tui

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The pull-requests takeover (§15 view 7, task 052.6).
//
// The question this screen exists to answer is the cross-project one — what
// is open across everything I run — which one project at a time cannot. So
// it lists every available project's open pull requests, grouped, with the
// task that claims each row.
//
// It makes no GitHub call of its own: every row here came from the daemon,
// which owns every call it makes on a client's behalf. What the TUI adds is
// the two writes to vincent's own column (link and unlink) and a hand-off to
// a browser.

// eventTaskGitHubPullChanged is the durable event the daemon writes when a
// task's pull-request link is applied or removed (§13.3) — the reconciler
// tick this screen re-renders on rather than leaving a stale list.
const eventTaskGitHubPullChanged = "task.github_pull_changed"

// githubProject pairs a registered project with its §13.2 capability probe.
// There is no stored notion of a "GitHub project": it is derived from the
// origin remote plus a credential probe, which is exactly why the probe is
// its own endpoint rather than three more fields on the project DTO.
type githubProject struct {
	project apiclient.Project
	status  apiclient.GitHubStatus
}

// githubProbeMsg carries the whole fan-out of probes. It is one message
// rather than one per project because the nav row is gated on *any* project
// answering yes, and a per-project message would make the row flicker into
// existence as answers trickle in.
type githubProbeMsg struct {
	projects []githubProject
	// err is the project listing failing, which is not the same as every
	// probe saying no — the difference is between "no GitHub projects" and
	// "could not ask".
	err error
}

// available is the projects whose integration answered yes, in listing order.
func (m githubProbeMsg) available() []githubProject {
	out := make([]githubProject, 0, len(m.projects))
	for _, p := range m.projects {
		if p.status.Available {
			out = append(out, p)
		}
	}
	return out
}

// probeGitHubCmd asks every registered project whether its GitHub
// integration is usable, concurrently. The daemon's short cache absorbs the
// repeat cost, which is the reason §13.2 gives for the probe being cheap
// enough to ask per project.
func probeGitHubCmd(client *apiclient.Client) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		projects, err := client.ListProjects(ctx)
		if err != nil {
			return githubProbeMsg{err: err}
		}
		out := make([]githubProject, len(projects))
		var wg sync.WaitGroup
		for i, project := range projects {
			out[i] = githubProject{project: project}
			wg.Add(1)
			go func() {
				defer wg.Done()
				// A probe that fails is "not available": a nav row offered on
				// the strength of a question nobody could answer leads to a
				// screen that then cannot list anything.
				status, err := client.ProjectGitHub(ctx, project.ID)
				if err == nil {
					out[i].status = status
				}
			}()
		}
		wg.Wait()
		return githubProbeMsg{projects: out}
	}
}

// Pull-requests messages.
type (
	prRefreshMsg struct{}
	// prLoadedMsg is one whole screen's worth of listings: every available
	// project's, each carrying its own outcome, plus the task rows the
	// claiming column and the link picker read from.
	prLoadedMsg struct {
		groups []pullGroup
		tasks  []apiclient.Task
	}
	// prLinkedMsg reports a completed link or unlink.
	prLinkedMsg struct {
		taskID int64
		number int
		unlink bool
		err    error
	}
)

// pullGroup is one project's listing. err holds that project's own failure —
// a 409 from an integration that stopped working sinks this group and no
// other, which is the whole reason the groups are fetched separately.
type pullGroup struct {
	project apiclient.Project
	pulls   []apiclient.GitHubPullRequest
	err     string
}

// prRow is one selectable line: a pull request and the project it is in.
// Group headings are drawn by the renderer and are not rows — there is
// nothing to do on one.
type prRow struct {
	project apiclient.Project
	pull    apiclient.GitHubPullRequest
}

// unlinkPrompt is the inline confirmation. It says the refusal is sticky
// because that is what DELETE does: the reconciler reads the suppression and
// will not re-apply the link on its next tick. A prompt that said "clear"
// would be lying.
type unlinkPrompt struct {
	taskID int64
	number int
	text   string
}

// pullRequestsView is §15's view 7.
type pullRequestsView struct {
	client *apiclient.Client
	now    func() time.Time

	// available is what the root's probe fan-out found. The view does not
	// probe: the root does, because the nav row that reaches this screen is
	// gated on the same answer.
	available []githubProject

	groups []pullGroup
	tasks  []apiclient.Task

	loaded   bool
	loading  bool
	lastLoad time.Time

	cursor int

	filter    textField
	filtering bool

	// picker is the link picker, scoped to the row's own project: POST takes
	// a bare number and the daemon resolves the repo from the task's project,
	// so offering a task from another project would link that project's repo
	// to a number that means something else there.
	picker     *picker
	pickerPull int
	pickerRepo string
	// pickerCreate distinguishes the two pickers this screen opens. `l`
	// links the selected pull request to a task; `P` picks a task with no
	// pull request and opens the workspace's form for it (task 069). One
	// picker widget, two intents, because they differ only in which tasks
	// they offer and what choosing one does.
	pickerCreate bool
	confirm      *unlinkPrompt
	// state is the listing's `state=` parameter, cycled by `s` (task 064
	// decision 9). It starts at `open` — 052's default and still the answer
	// to the question this screen usually asks.
	state       string
	note        string
	noteBad     bool
	refreshWait bool

	width, height int
}

func newPullRequestsView() *pullRequestsView {
	fi := newTextField()
	fi.SetPlaceholder("filter by number, title, branch or project")
	fi.SetPrompt("/")
	return &pullRequestsView{now: time.Now, filter: fi, state: pullStates[0]}
}

func (v *pullRequestsView) title() string { return "Pull requests" }

// setClient takes the connection as it comes up and again after a reconnect.
// It re-lists rather than waiting for the next probe: a reconnect is exactly
// when what GitHub said last is most likely to be stale.
func (v *pullRequestsView) setClient(c *apiclient.Client) tea.Cmd {
	v.client = c
	return v.loadCmd()
}

func (v *pullRequestsView) capturesInput() bool {
	if v.filtering {
		return true
	}
	// The picker owns the keyboard only while its filter or free-text entry
	// is being typed into; its list is single-key like the confirmations.
	return v.picker != nil && (v.picker.filtering || v.picker.editing)
}

func (v *pullRequestsView) paste(text string) tea.Cmd {
	if v.picker != nil {
		return v.picker.paste(text)
	}
	if !v.filtering {
		return nil
	}
	var cmd tea.Cmd
	v.filter, cmd = v.filter.Update(tea.PasteMsg{Content: text})
	return cmd
}

// hintedProject lets `n` open the new-task form on the project the cursor is
// standing in.
func (v *pullRequestsView) hintedProject() int64 {
	if row, ok := v.current(); ok {
		return row.project.ID
	}
	return 0
}

func (v *pullRequestsView) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
		return v, nil
	case githubProbeMsg:
		return v, v.applyProbe(msg)
	case viewActivatedMsg:
		if msg.id == viewPullRequests {
			// A view that was off-screen through a burst of events opens on
			// what it last fetched; refetching on activation is what keeps
			// "off-screen" from meaning "stale".
			return v, v.loadCmd()
		}
		return v, nil
	case prRefreshMsg:
		v.refreshWait = false
		return v, v.loadCmd()
	case prLoadedMsg:
		v.applyLoaded(msg)
		return v, nil
	case prLinkedMsg:
		return v, v.applyLinked(msg)
	case openedURLMsg:
		v.applyOpened(msg)
		return v, nil
	case noteMsg:
		return v, v.updateNote(msg.note)
	case tea.KeyPressMsg:
		return v.updateKey(msg)
	}
	return v, nil
}

// applyProbe records what the root found and reloads when the set of
// available projects changed — a project registered mid-session is one this
// screen has never listed.
func (v *pullRequestsView) applyProbe(msg githubProbeMsg) tea.Cmd {
	next := msg.available()
	if sameGitHubProjects(v.available, next) {
		return nil
	}
	v.available = next
	return v.loadCmd()
}

func sameGitHubProjects(a, b []githubProject) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].project.ID != b[i].project.ID {
			return false
		}
	}
	return true
}

// loadCmd fetches every available project's listing concurrently, and the
// task rows the claiming column reads. Each listing keeps its own error:
// this is the endpoint that answers 409 when an integration stops working,
// and one project's credentials expiring must not blank the others.
func (v *pullRequestsView) loadCmd() tea.Cmd {
	client := v.client
	if client == nil || len(v.available) == 0 {
		return nil
	}
	v.loading = true
	projects := append([]githubProject(nil), v.available...)
	state := v.state
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		groups := make([]pullGroup, len(projects))
		var wg sync.WaitGroup
		for i, gp := range projects {
			groups[i] = pullGroup{project: gp.project}
			wg.Add(1)
			go func() {
				defer wg.Done()
				pulls, err := client.ListGitHubPulls(ctx, gp.project.ID,
					apiclient.GitHubPullsOptions{State: state})
				if err != nil {
					groups[i].err = githubReasonMessage(err)
					return
				}
				groups[i].pulls = pulls
			}()
		}
		wg.Wait()
		// The claiming column names a task, and a name is worth more than an
		// id. The listing failing is not a reason to hide the pull requests.
		tasks, err := client.ListTasks(ctx, apiclient.ListTasksOptions{})
		if err != nil {
			tasks = nil
		}
		return prLoadedMsg{groups: groups, tasks: tasks}
	}
}

// githubReasonMessage is the sentence a failed listing puts on its group.
// The daemon's 409 carries its named reason in details and the sentence in
// the message, and the message is what a human reads.
func githubReasonMessage(err error) string {
	var apiErr *apiclient.Error
	if errors.As(err, &apiErr) {
		if apiErr.Message != "" {
			return apiErr.Message
		}
		if reason := apiErr.Details["reason"]; reason != "" {
			return reason
		}
	}
	return errString(err)
}

func (v *pullRequestsView) applyLoaded(msg prLoadedMsg) {
	v.loading = false
	v.loaded = true
	v.lastLoad = v.now()
	v.groups = msg.groups
	if msg.tasks != nil {
		v.tasks = msg.tasks
	}
	v.clampCursor()
}

func (v *pullRequestsView) scheduleRefresh() tea.Cmd {
	if v.refreshWait {
		return nil
	}
	v.refreshWait = true
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return prRefreshMsg{} })
}

// updateNote re-lists on the events that change what is on screen: a
// reconciler tick that linked or unlinked a pull request, and any task event,
// which moves the claiming column's titles and states.
func (v *pullRequestsView) updateNote(n apiclient.Note) tea.Cmd {
	ev, ok := n.(apiclient.EventNote)
	if !ok {
		return nil
	}
	if ev.Event.Type == eventTaskGitHubPullChanged || isTaskEvent(ev.Event.Type) {
		return v.scheduleRefresh()
	}
	return nil
}

func (v *pullRequestsView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	if v.picker != nil {
		return v.updatePickerKey(msg)
	}
	if v.confirm != nil {
		return v.updateConfirmKey(msg)
	}
	if v.filtering {
		switch msg.String() {
		case "esc":
			v.filtering = false
			v.filter.SetValue("")
			v.filter.Blur()
			v.clampCursor()
			return v, nil
		case "enter":
			v.filtering = false
			v.filter.Blur()
			return v, nil
		}
		var cmd tea.Cmd
		v.filter, cmd = v.filter.Update(msg)
		v.clampCursor()
		return v, cmd
	}

	switch msg.String() {
	case "up", "k":
		v.cursor = max(v.cursor-1, 0)
		return v, nil
	case "down", "j":
		v.cursor = min(v.cursor+1, max(len(v.rows())-1, 0))
		return v, nil
	case "/":
		v.filtering = true
		v.filter.Focus()
		return v, nil
	case "esc":
		// One layer per press (§15): a committed filter or a note clears
		// first; with nothing left, the takeover closes.
		if v.filter.Value() != "" || v.note != "" {
			v.filter.SetValue("")
			v.note = ""
			v.clampCursor()
			return v, nil
		}
		return v, func() tea.Msg { return selectViewMsg{id: viewHome} }
	case "R":
		return v, v.loadCmd()
	case "o":
		return v, v.openSelected()
	case "enter":
		return v, v.openTask()
	case "s":
		v.cycleState()
		return v, v.loadCmd()
	case "c":
		return v, v.createTask()
	case "l":
		v.openLinkPicker()
		return v, nil
	case "P":
		v.openCreatePicker()
		return v, nil
	case "u":
		v.askUnlink()
		return v, nil
	}
	return v, nil
}

// pullStates is the cycle `s` walks (task 064 decision 9). `open` stays the
// default, so 052's objection — do not pull a repository's whole pull-request
// history to answer one question — is not reopened, only made a choice: acting
// on a merged pull request and redoing a reverted one are real cases, and this
// screen is now where a task is created from.
var pullStates = []string{"open", "closed", "all"}

func (v *pullRequestsView) cycleState() {
	for i, s := range pullStates {
		if s == v.state {
			v.state = pullStates[(i+1)%len(pullStates)]
			v.setNote("listing "+v.state+" pull requests…", false)
			return
		}
	}
	v.state = pullStates[0]
}

// createTask opens the new-task form seeded with the selected pull request
// (task 064). The TUI computes no prefill and makes no GitHub call: it hands
// the form a project and a number, and the form asks the daemon for the
// prefill against the workflow it ends up on — 035 decision 2, unchanged.
//
// A row another task already claims is refused here rather than at the create
// call. Two live tasks cannot hold one branch (decision 4), and saying so on
// the row that shows the claim is more use than a 400 three screens later.
func (v *pullRequestsView) createTask() tea.Cmd {
	row, ok := v.current()
	if !ok {
		return nil
	}
	if row.pull.TaskID != nil {
		v.setNote("pull request #"+strconv.Itoa(row.pull.Number)+
			" is already claimed by task "+strconv.FormatInt(*row.pull.TaskID, 10)+
			" — press enter to open it", true)
		return nil
	}
	if strings.TrimSpace(row.pull.HeadBranch) == "" {
		v.setNote("pull request #"+strconv.Itoa(row.pull.Number)+
			" names no head branch, so there is no branch to run a task on", true)
		return nil
	}
	pull := row.pull
	return func() tea.Msg {
		return newTaskFromPullMsg{projectID: row.project.ID, pull: &pull}
	}
}

// openSelected hands the row's URL to a browser. The URL is GitHub's own,
// served on the row; nothing is constructed here.
func (v *pullRequestsView) openSelected() tea.Cmd {
	row, ok := v.current()
	if !ok {
		return nil
	}
	v.setNote("opening "+row.pull.URL+"…", false)
	return openURLCmd(row.pull.URL)
}

func (v *pullRequestsView) applyOpened(msg openedURLMsg) {
	if msg.err != nil {
		v.setNote(openFailure(msg), true)
		return
	}
	v.note = ""
}

// openTask jumps to the workspace of the task that claims the row. A row no
// task claims has nowhere to go, and enter is deliberately not overloaded
// into "link this one": the link key is its own, and a key that means two
// unrelated things depending on the row is worse than one that sometimes
// does nothing.
func (v *pullRequestsView) openTask() tea.Cmd {
	row, ok := v.current()
	if !ok || row.pull.TaskID == nil {
		return nil
	}
	id := *row.pull.TaskID
	state := ""
	if t, found := v.taskByID(id); found {
		state = t.State
	}
	return func() tea.Msg { return selectTaskMsg{id: id, state: state} }
}

// openLinkPicker offers the tasks of the row's own project (decision 5).
func (v *pullRequestsView) openLinkPicker() {
	row, ok := v.current()
	if !ok {
		return
	}
	v.note = ""
	v.pickerCreate = false
	v.pickerPull = row.pull.Number
	v.pickerRepo = row.pull.Repo
	v.picker = newPicker(0, "task in "+row.project.Name, v.taskOptions(row.project.ID), false, "")
}

// openCreatePicker is `P`: the tasks that could have a pull request and do
// not (task 069).
//
// This screen has no task rows and is not given any: its question is "what is
// open across everything I run", and a task with no pull request is not an
// open pull request. So the offer arrives as a picker, and choosing a task
// opens that task's workspace with the form up — the workspace is still where
// the offer to create lives (052 decision 6), widened rather than reversed.
//
// Eligibility is branch-and-no-live-link and nothing more (decision 4). A
// task whose push will be refused, or whose branch is somebody else's head,
// is offered and told by the push or the create failing with a named reason,
// rather than filtered out here on a guess.
func (v *pullRequestsView) openCreatePicker() {
	options := v.createOptions()
	if len(options) == 0 {
		v.setNote("no task here has a branch without a pull request", true)
		return
	}
	v.note = ""
	v.pickerCreate = true
	v.picker = newPicker(0, "task to open a pull request for", options, false, "")
}

// createOptions is `P`'s rows: every task on this screen's GitHub projects
// that has a branch and no live link. It is not scoped to one row's project,
// because `P` is not about a row — there may be no row at all.
func (v *pullRequestsView) createOptions() []pickerOption {
	claimed := map[int64]bool{}
	for _, g := range v.groups {
		for _, row := range g.pulls {
			if row.TaskID != nil {
				claimed[*row.TaskID] = true
			}
		}
	}
	onGitHub := map[int64]bool{}
	for _, p := range v.available {
		onGitHub[p.project.ID] = true
	}
	rows := make([]apiclient.Task, 0, len(v.tasks))
	for _, t := range v.tasks {
		if t.BranchName != "" && !claimed[t.ID] && onGitHub[t.ProjectID] {
			rows = append(rows, t)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	out := make([]pickerOption, 0, len(rows))
	for _, t := range rows {
		out = append(out, pickerOption{
			value: strconv.FormatInt(t.ID, 10),
			label: "#" + strconv.FormatInt(t.ID, 10) + "  " + t.Title,
			note:  t.State + " · " + t.BranchName,
		})
	}
	return out
}

// taskOptions is the picker's rows: this project's tasks, newest first, with
// the branch beside each — the branch is what a pull request's head is
// matched against, so it is the field that makes the right task obvious.
func (v *pullRequestsView) taskOptions(projectID int64) []pickerOption {
	rows := make([]apiclient.Task, 0, len(v.tasks))
	for _, t := range v.tasks {
		if t.ProjectID == projectID {
			rows = append(rows, t)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	out := make([]pickerOption, 0, len(rows))
	for _, t := range rows {
		note := t.State
		if t.BranchName != "" {
			note += " · " + t.BranchName
		}
		out = append(out, pickerOption{
			value: strconv.FormatInt(t.ID, 10),
			label: "#" + strconv.FormatInt(t.ID, 10) + "  " + t.Title,
			note:  note,
		})
	}
	return out
}

func (v *pullRequestsView) updatePickerKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	res := v.picker.update(msg)
	var cmd tea.Cmd
	if res.chosen {
		if id, err := strconv.ParseInt(res.value, 10, 64); err == nil {
			if v.pickerCreate {
				cmd = v.openTaskWithPRForm(id)
			} else {
				cmd = v.linkCmd(id, v.pickerPull)
			}
		}
	}
	if res.closed {
		v.picker = nil
	}
	return v, tea.Batch(res.cmd, cmd)
}

// openTaskWithPRForm navigates to a task's workspace and asks it to open the
// pull-request form. The form is not rebuilt here: it needs the daemon's
// prefill for that task, which the workspace fetches on open anyway, and a
// second copy of it on this screen would be a second thing to keep correct.
func (v *pullRequestsView) openTaskWithPRForm(taskID int64) tea.Cmd {
	state := ""
	if t, found := v.taskByID(taskID); found {
		state = t.State
	}
	return func() tea.Msg { return selectTaskMsg{id: taskID, state: state, openPR: true} }
}

func (v *pullRequestsView) linkCmd(taskID int64, number int) tea.Cmd {
	client := v.client
	if client == nil {
		v.setNote("not connected", true)
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		_, err := client.LinkGitHubPull(ctx, taskID, number)
		return prLinkedMsg{taskID: taskID, number: number, err: err}
	}
}

// askUnlink opens the confirmation. It names the stickiness because that is
// the part a human cannot see: the row disappears either way, and only one
// of the two outcomes survives the next reconciler tick.
func (v *pullRequestsView) askUnlink() {
	row, ok := v.current()
	if !ok || row.pull.TaskID == nil {
		v.setNote("no task claims this pull request", true)
		return
	}
	v.note = ""
	v.confirm = &unlinkPrompt{
		taskID: *row.pull.TaskID,
		number: row.pull.Number,
		text: "unlink #" + strconv.Itoa(row.pull.Number) + " from task #" +
			strconv.FormatInt(*row.pull.TaskID, 10) +
			"? the refusal sticks — the reconciler will not link it again",
	}
}

func (v *pullRequestsView) updateConfirmKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	c := v.confirm
	v.confirm = nil
	if msg.String() != "y" && msg.String() != "Y" {
		return v, nil
	}
	client := v.client
	if client == nil {
		v.setNote("not connected", true)
		return v, nil
	}
	return v, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		_, err := client.UnlinkGitHubPull(ctx, c.taskID)
		return prLinkedMsg{taskID: c.taskID, number: c.number, unlink: true, err: err}
	}
}

// applyLinked reports the write and re-lists. The daemon publishes
// task.github_pull_changed too, so the refresh is belt and braces — but a
// human who pressed a key is owed an answer that does not depend on a
// stream being up.
func (v *pullRequestsView) applyLinked(msg prLinkedMsg) tea.Cmd {
	verb := "linked"
	if msg.unlink {
		verb = "unlinked"
	}
	if msg.err != nil {
		v.setNote("could not "+strings.TrimSuffix(verb, "ed")+" #"+
			strconv.Itoa(msg.number)+": "+githubReasonMessage(msg.err), true)
		return nil
	}
	v.setNote("#"+strconv.Itoa(msg.number)+" "+verb+" "+
		linkPreposition(msg.unlink)+" task #"+strconv.FormatInt(msg.taskID, 10), false)
	return v.loadCmd()
}

func linkPreposition(unlink bool) string {
	if unlink {
		return "from"
	}
	return "to"
}

func (v *pullRequestsView) setNote(text string, bad bool) {
	v.note, v.noteBad = text, bad
}

// rows is the flattened, filtered selection order: the groups in listing
// order, each project's pull requests in the order the daemon served them.
func (v *pullRequestsView) rows() []prRow {
	q := strings.ToLower(strings.TrimSpace(v.filter.Value()))
	out := make([]prRow, 0, 16)
	for _, g := range v.groups {
		for _, p := range g.pulls {
			if q != "" && !pullMatches(g.project, p, q) {
				continue
			}
			out = append(out, prRow{project: g.project, pull: p})
		}
	}
	return out
}

func pullMatches(project apiclient.Project, p apiclient.GitHubPullRequest, q string) bool {
	hay := strings.ToLower(strings.Join([]string{
		"#" + strconv.Itoa(p.Number), p.Title, p.HeadBranch, p.Repo,
		p.Status(), p.Author, project.Name,
	}, " "))
	return strings.Contains(hay, q)
}

func (v *pullRequestsView) current() (prRow, bool) {
	rows := v.rows()
	if v.cursor < 0 || v.cursor >= len(rows) {
		return prRow{}, false
	}
	return rows[v.cursor], true
}

func (v *pullRequestsView) clampCursor() {
	v.cursor = min(max(v.cursor, 0), max(len(v.rows())-1, 0))
}

func (v *pullRequestsView) taskByID(id int64) (apiclient.Task, bool) {
	for _, t := range v.tasks {
		if t.ID == id {
			return t, true
		}
	}
	return apiclient.Task{}, false
}
