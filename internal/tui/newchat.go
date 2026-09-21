package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The new-chat form (§15, task 067), a layer over the chats board rather than
// a seventh view: it is opened from there, it returns there, and it means
// nothing anywhere else.
//
// It is deliberately smaller than the new-task form. A chat has no workflow,
// no fields, no github issue and no scheduling, so the whole form is project,
// title, agent, model, effort, base branch and branch — and only the first two
// are required.
//
// Five of those seven rows are lists, drawn by the same `picker` the new-task,
// follow-up and repair forms use (issue #281): one component means one set of
// idioms for "choose one of a list" — incremental filtering, a bounded
// window, the `cli`/`curated` provenance note, and free text where a catalog
// is only a suggestion.

// ncRow is one row of the new-chat form, in tab order.
type ncRow int

const (
	ncProject ncRow = iota
	ncTitle
	ncAgent
	ncModel
	ncEffort
	ncBase
	// ncBranch adopts a branch that already exists (§10, task 125), and that
	// is the only thing it can mean: `branch_name` without `existing_branch`
	// is a 400 on POST /v1/chats, because a chat that cuts its own branch is
	// named from its id. Left empty, vincent cuts `vincent/{id}-{slug}` as it
	// always has. It sits next to ncBase so the two branch rows are adjacent,
	// which leaves the tab order of the five rows above untouched.
	ncBranch
	ncRowCount
)

// chatCreatedMsg reports the outcome of POST /v1/chats.
type chatCreatedMsg struct {
	chat *apiclient.Chat
	err  error
}

// newChatFieldsMsg carries what the form needs to offer choices: the
// registered projects and the adapters that can hold a conversation.
type newChatFieldsMsg struct {
	projects []apiclient.Project
	agents   []apiclient.Agent
	err      error
}

// newChatBranchesMsg carries a project's local branch listing, which is what
// the branch row offers. The project travels with it so a reply for one the
// user has since left is dropped rather than applied — the new-task form's
// ntBranchesMsg for the same reason.
type newChatBranchesMsg struct {
	projectID int64
	branches  []apiclient.Branch
	err       error
}

// newChatForm is the create form.
type newChatForm struct {
	client *apiclient.Client

	projects []apiclient.Project
	agents   apiclient.Agents

	projectID int64
	agentIdx  int

	model  string
	effort string

	title textField
	base  textField
	// branch holds the adopted branch name. It is storage for the picker the
	// row opens, not a text row: a chat's branch row has exactly one meaning,
	// and typing one is done in the picker's free-text row.
	branch textField

	// branches is the project's local branch listing, branchesFor the project
	// it describes and branchesErr why there is none — drawn above the
	// picker's free row, so a repository the daemon cannot read still leaves
	// a branch name typeable.
	branches    []apiclient.Branch
	branchesFor int64
	branchesErr string

	// pick is the list the focused row expands into, nil while navigating.
	pick *picker

	// focus is the row the cursor is on.
	focus ncRow

	err string
	// branchErr is the daemon's complaint about the branch row, parked there
	// rather than on the form-wide line: the 409 for a working directory
	// another task or chat already owns names a branch, and the row that
	// chose it is where it can be changed (task 125 decision 2).
	branchErr  string
	submitting bool
}

func newNewChatForm(client *apiclient.Client, hintProject int64) *newChatForm {
	f := &newChatForm{client: client, projectID: hintProject}
	f.title = newTextField()
	f.title.SetPlaceholder("what is this conversation about")
	f.base = newTextField()
	f.base.SetPlaceholder(f.baseHint())
	f.branch = newTextField()
	f.focus = ncTitle
	f.title.Focus()
	return f
}

// init fetches the projects and adapters the pickers offer.
func (f *newChatForm) init() tea.Cmd {
	client := f.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		projects, err := client.ListProjects(ctx)
		if err != nil {
			return newChatFieldsMsg{err: err}
		}
		agents, _ := client.ListAgents(ctx, false)
		return newChatFieldsMsg{projects: projects, agents: agents}
	}
}

// branchesCmd fetches the project's local branches for the branch row.
func (f *newChatForm) branchesCmd(projectID int64) tea.Cmd {
	client := f.client
	if client == nil || projectID == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		branches, err := client.ListBranches(ctx, projectID)
		return newChatBranchesMsg{projectID: projectID, branches: branches, err: err}
	}
}

// applyBranches takes a listing only when it describes the project on screen.
func (f *newChatForm) applyBranches(msg newChatBranchesMsg) {
	if msg.projectID == 0 || msg.projectID != f.projectID {
		return
	}
	f.branchesFor = msg.projectID
	if msg.err != nil {
		f.branches, f.branchesErr = nil, "could not list branches: "+errString(msg.err)
		return
	}
	f.branches, f.branchesErr = msg.branches, ""
}

// capturesInput is true for every row of an open draft, not only the two text
// ones — a recorded exception to PR L's rule that a form captures input "only
// while a text row is in edit mode" (task 067 decision 8, §15 amended
// 2026-08-31).
//
// The rule is not changed anywhere else: `newTask.capturesInput` still
// answers false while navigating, and widening it globally would make `?`,
// `:`, `!` and `M` unreachable from every form in the TUI. The exception is
// scoped to this form because a live draft sits behind every row: only title
// and base are text fields, so `q` on the project row quit the TUI with the
// draft (issue #279). An open picker adds a filter row and a free-text row on
// top of that. `esc` stays the way out, one layer per press.
func (f *newChatForm) capturesInput() bool { return true }

// paste types into whichever text entry is open: the picker's free-text row
// when a list is up, else the focused text field.
func (f *newChatForm) paste(text string) tea.Cmd {
	if f.pick != nil {
		return f.pick.paste(text)
	}
	var cmd tea.Cmd
	switch f.focus {
	case ncTitle:
		f.title, cmd = f.title.Update(tea.PasteMsg{Content: text})
	case ncBase:
		f.base, cmd = f.base.Update(tea.PasteMsg{Content: text})
	case ncProject, ncAgent, ncModel, ncEffort, ncBranch, ncRowCount:
	}
	return cmd
}

// applyFields records the pickers' contents, defaulting the project to the
// hint and the agent to the first adapter that can resume — which is the
// daemon's own default, so the form and the API agree before anyone types.
func (f *newChatForm) applyFields(msg newChatFieldsMsg) tea.Cmd {
	if msg.err != nil {
		f.err = errString(msg.err)
		return nil
	}
	f.projects, f.agents = msg.projects, resumableAgents(msg.agents)
	if f.projectID == 0 && len(f.projects) > 0 {
		f.projectID = f.projects[0].ID
	}
	f.base.SetPlaceholder(f.baseHint())
	// The branch listing is project-scoped, so it is asked for once the
	// project row has settled rather than alongside the two catalogs above.
	return f.branchesCmd(f.projectID)
}

// resumableAgents is the agent picker's contents: only adapters that can hold
// a conversation (decision row 29). The daemon's `agent_cannot_resume` refusal
// stays the authority — applyFailure still renders it — this only keeps the
// picker from offering a choice that is certain to be refused.
//
// It follows the `input_verdict` precedent: a daemon too old to send
// `supports_resume` says nothing about any adapter, and nothing is dropped on
// the strength of a field that was never sent.
//
// A row disabled with its reason — the new-task precedent from tasks 010 and
// 013 — was considered and declined (task 067 decision 10): an adapter that
// cannot hold a conversation is out of reach for every chat, not for this one
// draft, which is the case that precedent covers.
func resumableAgents(agents []apiclient.Agent) apiclient.Agents {
	var out apiclient.Agents
	for _, a := range agents {
		if !a.CannotResume() {
			out = append(out, a)
		}
	}
	return out
}

// applyFailure puts the daemon's refusal on the form rather than closing it.
//
// `agent_cannot_resume` is rendered as the typed refusal it is: an adapter
// that cannot resume its own session cannot hold a conversation, and telling
// the human to pick another adapter is a different sentence from "creation
// failed" (task 063 decision 3).
func (f *newChatForm) applyFailure(err error) {
	f.submitting = false
	var apiErr *apiclient.Error
	if errors.As(err, &apiErr) && apiErr.Code == "agent_cannot_resume" {
		f.err = "that agent cannot resume its own session, so it cannot hold a " +
			"conversation — pick one that can"
		return
	}
	msg := errString(err)
	// Everything the daemon refuses about an adopted branch names the branch:
	// the 400s for a name it cannot find and the 409 for a working directory
	// that already has an owner. Both belong on the row that chose it — the
	// new-task form's applyFailure reads the daemon's words the same way.
	if strings.Contains(msg, "branch") {
		f.branchErr = msg
		f.moveFocusTo(ncBranch)
		return
	}
	f.err = msg
}

// update runs the form's keyboard. done reports that the layer should close.
func (f *newChatForm) update(msg tea.KeyPressMsg, client *apiclient.Client) (cmd tea.Cmd, done bool) {
	f.client = client
	// An open list is a layer above the form in §15's esc stack: every key
	// goes to it, `ctrl+s` included, so `esc` closes the list and leaves the
	// draft — one esc, one layer.
	if f.pick != nil {
		res := f.pick.update(msg)
		cmds := []tea.Cmd{res.cmd}
		if res.chosen {
			cmds = append(cmds, f.setRow(ncRow(f.pick.row), res.value))
		}
		if res.closed {
			f.pick = nil
		}
		return tea.Batch(cmds...), false
	}
	switch msg.String() {
	case "esc":
		return nil, true
	case "tab", "down":
		f.moveFocus(1)
		return nil, false
	case "shift+tab", "up":
		f.moveFocus(-1)
		return nil, false
	case "left":
		return f.cycle(-1), false
	case "right":
		return f.cycle(1), false
	case "ctrl+s":
		return f.submit(), false
	case "enter":
		// The same key on every row: open the focused row's list, or move on
		// from a text one. `ctrl+s` is the sole create key, which is what §15
		// documents and what the form's own hint line says.
		f.openRow()
		return nil, false
	}
	switch f.focus {
	case ncTitle:
		f.title, cmd = f.title.Update(msg)
	case ncBase:
		f.base, cmd = f.base.Update(msg)
	case ncProject, ncAgent, ncModel, ncEffort, ncBranch, ncRowCount:
	}
	return cmd, false
}

// openRow expands the focused row into its list. The two text rows have no
// list, so enter advances from them.
func (f *newChatForm) openRow() {
	f.err = ""
	switch f.focus {
	case ncProject:
		f.pick = newPicker(int(ncProject), "project", f.projectOptions(), false,
			strconv.FormatInt(f.projectID, 10))
	case ncAgent:
		f.pick = newPicker(int(ncAgent), "agent", f.agentOptions(), false, f.agentName())
	case ncModel:
		f.pick = newPicker(int(ncModel), "model", f.catalogOptions(f.models(), f.defaultModel()),
			true, f.model)
	case ncEffort:
		f.pick = newPicker(int(ncEffort), "effort", f.catalogOptions(f.efforts(), f.defaultEffort()),
			true, f.effort)
	case ncBranch:
		f.branchErr = ""
		f.pick = newPicker(int(ncBranch), "existing branch", f.branchOptions(), true,
			strings.TrimSpace(f.branch.Value()))
		f.pick.err = f.branchesErr
	case ncTitle, ncBase, ncRowCount:
		f.moveFocus(1)
	}
}

// branchOptions is the project's local branches, led by the row that clears
// the field. It is a listing and not a validator, so no row is disabled: the
// worktree holding a branch can be removed between choosing it and creating
// the chat, and the free-text row can name such a branch anyway (task 125.9
// decision 3).
func (f *newChatForm) branchOptions() []pickerOption {
	out := make([]pickerOption, 0, len(f.branches)+1)
	out = append(out, pickerOption{value: "", label: "(vincent cuts one)"})
	for _, b := range f.branches {
		out = append(out, pickerOption{value: b.Name, label: b.Name, note: branchNote(b)})
	}
	return out
}

// setRow commits a chosen value.
func (f *newChatForm) setRow(row ncRow, value string) tea.Cmd {
	switch row {
	case ncProject:
		if id, err := strconv.ParseInt(value, 10, 64); err == nil {
			return f.setProject(id)
		}
	case ncAgent:
		f.setAgent(value)
	case ncModel:
		f.model = value
	case ncEffort:
		f.effort = value
	case ncBranch:
		f.branch.SetValue(value)
		f.branchErr = ""
	case ncTitle, ncBase, ncRowCount:
	}
	return nil
}

// setProject re-derives whatever depends on the project: the base row's hint,
// which names that project's real default branch, and the branch listing,
// which is another repository's now. A branch already chosen goes with it — a
// name from the old project is not a branch this one can adopt.
func (f *newChatForm) setProject(id int64) tea.Cmd {
	if id == f.projectID {
		return nil
	}
	f.projectID = id
	f.base.SetPlaceholder(f.baseHint())
	f.branch.SetValue("")
	f.branchErr = ""
	f.branches, f.branchesFor, f.branchesErr = nil, 0, ""
	return f.branchesCmd(id)
}

// setAgent selects an adapter by name and drops the model and effort chosen
// under the previous one. The two catalogs belong to an adapter; keeping
// values chosen from another one would submit a pair that never existed.
func (f *newChatForm) setAgent(name string) {
	for i, a := range f.agents {
		if a.Name == name {
			f.setAgentIdx(i)
			return
		}
	}
}

func (f *newChatForm) setAgentIdx(i int) {
	if i == f.agentIdx {
		return
	}
	f.agentIdx = i
	f.model, f.effort = "", ""
}

func (f *newChatForm) moveFocus(delta int) {
	f.moveFocusTo((f.focus + ncRow(delta) + ncRowCount) % ncRowCount)
}

func (f *newChatForm) moveFocusTo(row ncRow) {
	f.focus = row
	f.title.Blur()
	f.base.Blur()
	switch f.focus {
	case ncTitle:
		f.title.Focus()
	case ncBase:
		f.base.Focus()
	case ncProject, ncAgent, ncModel, ncEffort, ncBranch, ncRowCount:
	}
}

// cycle steps the project and agent rows in place, without opening their
// list. This is the enum-row idiom from the new-task fields editor verbatim:
// left/right step through the members the way a boolean cycles, so two
// adapters stay a single keypress; enter opens the list, which is the only
// workable control for a long one.
//
// The model and effort rows are not stepped: they are catalogs of a hundred
// and more (§9.7), where "next" is not a cheap answer to anything. The text
// rows ignore it — left and right are cursor movement there.
func (f *newChatForm) cycle(delta int) tea.Cmd {
	switch f.focus {
	case ncProject:
		if len(f.projects) == 0 {
			return nil
		}
		i := 0
		for j, p := range f.projects {
			if p.ID == f.projectID {
				i = j
				break
			}
		}
		return f.setProject(f.projects[(i+delta+len(f.projects))%len(f.projects)].ID)
	case ncAgent:
		if len(f.agents) == 0 {
			return nil
		}
		f.setAgentIdx((f.agentIdx + delta + len(f.agents)) % len(f.agents))
	case ncTitle, ncModel, ncEffort, ncBase, ncBranch, ncRowCount:
	}
	return nil
}

func (f *newChatForm) agentName() string {
	if f.agentIdx < 0 || f.agentIdx >= len(f.agents) {
		return ""
	}
	return f.agents[f.agentIdx].Name
}

// projectOptions is one row per registered project, with its path as the
// note — the same pair the new-task project picker offers.
func (f *newChatForm) projectOptions() []pickerOption {
	out := make([]pickerOption, 0, len(f.projects))
	for _, p := range f.projects {
		out = append(out, pickerOption{
			value: strconv.FormatInt(p.ID, 10),
			label: p.Name,
			note:  p.Path,
		})
	}
	return out
}

// agentOptions is the adapter list, already narrowed to those that can hold a
// conversation, with the availability notes the other pickers carry: an
// adapter that is installed but not logged in fails every turn, and saying so
// here saves a chat that was never going to start (§9.5).
func (f *newChatForm) agentOptions() []pickerOption {
	out := make([]pickerOption, 0, len(f.agents))
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

// catalogOptions renders a catalog with its provenance tags, the way the
// new-task, follow-up and repair pickers do: "cli" came from the adapter
// itself and cannot be stale, "curated" is vincent's own list and may be.
//
// The leading row is "(agent default)" rather than "(workflow default)": a
// chat has no workflow, so the only thing behind an unset override is the
// adapter's own default, which the row names.
func (f *newChatForm) catalogOptions(opts []apiclient.AgentOption, def string) []pickerOption {
	out := make([]pickerOption, 0, len(opts)+1)
	out = append(out, pickerOption{value: "", label: "(agent default)", note: def})
	for _, o := range opts {
		out = append(out, pickerOption{value: o.Value, label: o.Value, note: o.Source})
	}
	return out
}

// models, efforts, defaultModel and defaultEffort scope to the selected
// adapter: its catalog is the only one that means anything for this chat.
func (f *newChatForm) models() []apiclient.AgentOption {
	a, ok := f.agents.Find(f.agentName())
	if !ok {
		return nil
	}
	return a.Models
}

func (f *newChatForm) efforts() []apiclient.AgentOption {
	a, ok := f.agents.Find(f.agentName())
	if !ok {
		return nil
	}
	return a.Efforts
}

func (f *newChatForm) defaultModel() string {
	a, ok := f.agents.Find(f.agentName())
	if !ok {
		return ""
	}
	return a.DefaultModel
}

func (f *newChatForm) defaultEffort() string {
	a, ok := f.agents.Find(f.agentName())
	if !ok {
		return ""
	}
	return a.DefaultEffort
}

// baseHint is the base row's placeholder: the selected project's real default
// branch, re-derived whenever the project row changes.
//
// It is a placeholder and not a value on purpose. Seeding the input would pin
// a branch at draft time and need a rule for re-seeding over text a human may
// already have typed; leaving the row empty keeps `CreateChatRequest.BaseBranch`
// empty, so `handleChatCreate` resolves the project's default at creation, as
// it does today.
func (f *newChatForm) baseHint() string {
	for _, p := range f.projects {
		if p.ID == f.projectID && p.DefaultBranch != "" {
			return p.DefaultBranch + " (the project's default)"
		}
	}
	return "base branch (optional — the project's default)"
}

// request is what the form would submit. BaseBranch is whatever the base row
// holds and nothing else: an untouched row submits empty, and the daemon
// resolves the project's default branch — the placeholder only says which one
// that is.
func (f *newChatForm) request() apiclient.CreateChatRequest {
	req := apiclient.CreateChatRequest{
		ProjectID:  f.projectID,
		Title:      strings.TrimSpace(f.title.Value()),
		Agent:      f.agentName(),
		Model:      f.model,
		Effort:     f.effort,
		BaseBranch: strings.TrimSpace(f.base.Value()),
	}
	// The pair travels together or not at all: either half alone is a 400,
	// and the row has no second meaning that would send one without the
	// other (§13.2, task 125).
	if b := strings.TrimSpace(f.branch.Value()); b != "" {
		req.BranchName, req.ExistingBranch = b, true
	}
	return req
}

func (f *newChatForm) submit() tea.Cmd {
	if strings.TrimSpace(f.title.Value()) == "" {
		f.err = "a chat needs a title"
		return nil
	}
	if f.projectID == 0 {
		f.err = "pick a project"
		return nil
	}
	client := f.client
	if client == nil {
		f.err = "not connected"
		return nil
	}
	f.err, f.branchErr = "", ""
	f.submitting = true
	req := f.request()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		chat, err := client.CreateChat(ctx, req)
		return chatCreatedMsg{chat: chat, err: err}
	}
}

func (f *newChatForm) projectName() string {
	for _, p := range f.projects {
		if p.ID == f.projectID {
			return p.Name
		}
	}
	if f.projectID == 0 {
		return "—"
	}
	return strconv.FormatInt(f.projectID, 10)
}

func (f *newChatForm) render(width, height int) string {
	// ncIndent is the marker plus the padded label every row carries, and so
	// what the two text fields have left of the pane (issue #299).
	const ncIndent = 2 + 9
	f.title.SetWidth(max(width-ncIndent, 10))
	f.base.SetWidth(max(width-ncIndent, 10))
	rows := []struct {
		row   ncRow
		label string
		value []string
	}{
		{ncProject, "project", []string{f.projectName() + stepHint()}},
		{ncTitle, "title", f.title.rows()},
		{ncAgent, "agent", []string{f.agentLabel()}},
		{ncModel, "model", []string{f.overrideValue(f.model, f.defaultModel())}},
		{ncEffort, "effort", []string{f.overrideValue(f.effort, f.defaultEffort())}},
		{ncBase, "base", f.base.rows()},
		{ncBranch, "branch", []string{f.branchValue()}},
	}
	lines := []string{" " + styleTitle.Render("new chat"), ""}
	for _, r := range rows {
		marker := "  "
		if r.row == f.focus && f.pick == nil {
			marker = "▸ "
		}
		lines = append(lines, indentRows(marker+padRight(r.label, 9), r.value)...)
		if r.row == ncBranch && f.branchErr != "" {
			lines = append(lines, "  "+styleBad.Render("⚠ "+f.branchErr))
		}
	}
	if f.pick != nil {
		f.pick.setWidth(width)
		lines = append(lines, f.pick.renderBody()...)
	}
	lines = append(lines, "")
	switch {
	case f.submitting:
		lines = append(lines, " "+styleDim.Render("creating…"))
	case f.err != "":
		lines = append(lines, " "+styleBad.Render(f.err))
	case f.pick != nil:
		lines = append(lines, " "+styleDim.Render(
			"↑/↓ choose · / filter · enter pick · esc close the list"))
	default:
		lines = append(lines, " "+styleDim.Render("enter choose · ctrl+s create · esc cancel"))
	}
	// A form that grew past its pane keeps its cursor row on screen rather
	// than drawing off the bottom.
	if height > 0 && len(lines) > height {
		lines = window(lines, int(f.focus)+2, height)
	}
	return strings.Join(lines, "\n")
}

// branchValue is the branch row's summary. Empty is the ordinary chat: vincent
// cuts `vincent/{id}-{slug}` from the base. A name is §10's adopt mode, which
// is the row's only other meaning, so the row says so rather than leaving the
// consequence to the block reason (§18).
func (f *newChatForm) branchValue() string {
	name := strings.TrimSpace(f.branch.Value())
	if name == "" {
		return styleDim.Render("(vincent cuts one)   enter list")
	}
	out := name + "  " + styleDim.Render("(runs on this existing branch)")
	for _, b := range f.branches {
		if b.Name == name && b.MainCheckout {
			out += "  " + styleWarn.Render("⚠ runs in "+
				firstNonEmpty(b.CheckedOutIn, "your own checkout")+", not a worktree")
		}
	}
	return out
}

// stepHint is the dim suffix on the two rows `←`/`→` still step in place.
func stepHint() string { return styleDim.Render("   ← → step · enter list") }

// overrideValue renders a §8.6-style override row: what was chosen, or what
// the adapter would use when nothing was.
func (f *newChatForm) overrideValue(value, def string) string {
	if value != "" {
		return value + styleDim.Render("   enter list")
	}
	label := "(agent default)"
	if def != "" {
		label += " " + def
	}
	return styleDim.Render(label + "   enter list")
}

func (f *newChatForm) agentLabel() string {
	name := f.agentName()
	if name == "" {
		name = styleDim.Render("the daemon's default")
	}
	return name + stepHint()
}
