package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/pathx"
)

// The TUI's startup project (task 132.3, spec §15). One chain, run once per
// process when the first project list and the first config answer have both
// arrived; the first rule that names a registered project wins:
//
//  1. `vincent --project <name|id>` — an exact name first, then an all-digit
//     value as an id (decision 29), so a project named `3` stays reachable;
//  2. the working directory — the deepest registered project path, or task or
//     chat worktree, that contains it (decision 4);
//  3. `tui.default_project`, by name;
//  4. the last-used project from tui.json — id first, then name, so a
//     restored database that renumbered ids still resolves;
//  5. the first project by name.
//
// A rule that names no registered project falls through, and the notice says
// so (decision 30). It is a pure function over its inputs so the whole table
// is tested without a model.

// Rule names, as the notice and selWhy spell them.
const (
	whyFlag      = "--project"
	whyCwd       = "from the working directory"
	whyConfig    = "tui.default_project"
	whyLastUsed  = "last used"
	whyFirstName = "the first project by name"
	// whyFollowed is a switch made by opening another project's task, chat
	// or issue (task 132.6).
	whyFollowed = "followed an opened task, chat or issue"
)

// selectedProjectState is tui.json's `selected_project` (decision 31): the
// last selection, written on every change.
type selectedProjectState struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// startupInputs is everything the chain reads.
type startupInputs struct {
	flag           string
	cwd            string
	defaultProject string
	lastUsed       *selectedProjectState
	projects       []apiclient.Project
	tasks          []apiclient.Task
	chats          []apiclient.Chat
}

// startupPick is the chain's answer. ok is false only with no projects at
// all. notice is the one line the root raises, empty when the pick is silent.
type startupPick struct {
	project apiclient.Project
	ok      bool
	why     string
	notice  string
}

func resolveStartupProject(in startupInputs) startupPick {
	var failed []string
	finish := func(p apiclient.Project, why string) startupPick {
		pick := startupPick{project: p, ok: true, why: why}
		switch {
		case len(failed) > 0:
			pick.notice = fmt.Sprintf("%s — showing `%s` (%s)", strings.Join(failed, "; "), p.Name, why)
		case why == whyCwd:
			pick.notice = headerProjectGlyph + " " + p.Name + " — " + whyCwd
		}
		return pick
	}
	if in.flag != "" {
		if p, ok := projectByName(in.projects, in.flag); ok {
			return finish(p, whyFlag)
		}
		if id, err := strconv.ParseInt(in.flag, 10, 64); isDigits(in.flag) && err == nil {
			if p, ok := projectByID(in.projects, id); ok {
				return finish(p, whyFlag)
			}
		}
		failed = append(failed, fmt.Sprintf("--project `%s` is not registered", in.flag))
	}
	if p, ok := projectByDir(in); ok {
		return finish(p, whyCwd)
	}
	if in.defaultProject != "" {
		if p, ok := projectByName(in.projects, in.defaultProject); ok {
			return finish(p, whyConfig)
		}
		failed = append(failed, fmt.Sprintf("tui.default_project `%s` is not registered", in.defaultProject))
	}
	if last := in.lastUsed; last != nil && (last.ID != 0 || last.Name != "") {
		if p, ok := projectByID(in.projects, last.ID); ok && last.ID != 0 {
			return finish(p, whyLastUsed)
		}
		if p, ok := projectByName(in.projects, last.Name); ok && last.Name != "" {
			return finish(p, whyLastUsed)
		}
		failed = append(failed, fmt.Sprintf("the last-used project `%s` is not registered", last.Name))
	}
	if p, ok := firstProjectByName(in.projects); ok {
		return finish(p, whyFirstName)
	}
	return startupPick{}
}

// reselectAfterDelete picks what replaces a deleted selection (task 132
// decision 46): the chain's tail, rule 3 then rule 5. The flag and the working
// directory are launch facts that do not apply mid-session, and the last-used
// project is the one just deleted. notice is the line the root raises.
func reselectAfterDelete(projects []apiclient.Project, defaultProject, deleted string) startupPick {
	pick := resolveStartupProject(startupInputs{defaultProject: defaultProject, projects: projects})
	switch {
	case !pick.ok:
		pick.notice = fmt.Sprintf("project `%s` was deleted — no projects remain", deleted)
	case pick.why == whyConfig:
		pick.notice = fmt.Sprintf("project `%s` was deleted — showing `%s` (default project)", deleted, pick.project.Name)
	default:
		pick.notice = fmt.Sprintf("project `%s` was deleted — showing `%s` (first by name)", deleted, pick.project.Name)
	}
	return pick
}

// projectByDir is rule 2: every registered project's path and every listed
// task's and chat's worktree is a candidate, and the deepest one containing
// the working directory wins — so a cwd inside a task worktree under the data
// dir resolves to the task's project even when another project's path is a
// parent of the data dir. A worktree whose owner is not a registered project
// is no candidate.
func projectByDir(in startupInputs) (apiclient.Project, bool) {
	if in.cwd == "" {
		return apiclient.Project{}, false
	}
	type candidate struct {
		path      string
		projectID int64
	}
	var cands []candidate
	for _, p := range in.projects {
		cands = append(cands, candidate{p.Path, p.ID})
	}
	for _, t := range in.tasks {
		if t.WorktreePath != nil && *t.WorktreePath != "" {
			cands = append(cands, candidate{*t.WorktreePath, t.ProjectID})
		}
	}
	for _, c := range in.chats {
		if c.WorktreePath != "" {
			cands = append(cands, candidate{c.WorktreePath, c.ProjectID})
		}
	}
	var best *candidate
	for i := range cands {
		c := &cands[i]
		if c.path == "" || !pathx.Contains(c.path, in.cwd) {
			continue
		}
		if _, ok := projectByID(in.projects, c.projectID); !ok {
			continue
		}
		// Every match contains the cwd, so the matches lie on one chain;
		// a deeper one is contained by the shallower. Comparing by
		// containment rather than by string length keeps a symlinked
		// spelling from out-ranking a resolved one.
		if best == nil || (pathx.Contains(best.path, c.path) && !pathx.SameDir(best.path, c.path)) {
			best = c
		}
	}
	if best == nil {
		return apiclient.Project{}, false
	}
	return projectByID(in.projects, best.projectID)
}

func projectByName(projects []apiclient.Project, name string) (apiclient.Project, bool) {
	for _, p := range projects {
		if p.Name == name {
			return p, true
		}
	}
	return apiclient.Project{}, false
}

func projectByID(projects []apiclient.Project, id int64) (apiclient.Project, bool) {
	for _, p := range projects {
		if p.ID == id {
			return p, true
		}
	}
	return apiclient.Project{}, false
}

// firstProjectByName is rule 5, ties broken by id.
func firstProjectByName(projects []apiclient.Project) (apiclient.Project, bool) {
	if len(projects) == 0 {
		return apiclient.Project{}, false
	}
	first := projects[0]
	for _, p := range projects[1:] {
		if p.Name < first.Name || (p.Name == first.Name && p.ID < first.ID) {
			first = p
		}
	}
	return first, true
}

// isDigits is decision 29's "all-digit": a sign or a space is a name.
func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// startupState is the root's half of the chain: the launch's own inputs and
// the two arrivals it waits for.
type startupState struct {
	flag string
	cwd  string
	// cfgSeen and defaultProject are the first config answer. A failed
	// fetch counts as an answer with nothing configured: startup must not
	// wait forever on a transient error, and falling through to the last
	// used project is the fail-open direction.
	cfgSeen        bool
	defaultProject string
	listed         bool
	fetching       bool
	done           bool
}

// startupWorktreesMsg carries the task and chat listings rule 2 reads. A
// failed listing is an empty one: the chain still runs on project paths.
type startupWorktreesMsg struct {
	tasks []apiclient.Task
	chats []apiclient.Chat
}

// noteStartupConfig adopts the first config answer for the chain and ignores
// every later one: tui.default_project is read at startup only, so a hot
// reload never moves the selection mid-session (task 132.3).
func (m *root) noteStartupConfig(msg tea.Msg) tea.Cmd {
	if m.startup.cfgSeen {
		return nil
	}
	switch msg := msg.(type) {
	case boardConfigMsg:
		m.startup.defaultProject = msg.defaultProject
	case daemonConfigMsg:
		if msg.err == nil {
			m.startup.defaultProject = msg.config.TUI.DefaultProject
		}
	case configSavedMsg:
		if msg.err == nil {
			m.startup.defaultProject = msg.cfg.TUI.DefaultProject
		}
	}
	m.startup.cfgSeen = true
	return m.maybeResolveStartup()
}

// noteDefaultProject keeps the latest `tui.default_project`, for a deleted
// selection's replacement and the project picker's `★` (task 132.18). A
// failed fetch leaves the last answer standing.
func (m *root) noteDefaultProject(msg tea.Msg) {
	defer func() {
		if m.projPick != nil {
			m.projPick.defaultProject = m.defaultProject
		}
	}()
	switch msg := msg.(type) {
	case boardConfigMsg:
		if msg.err == nil {
			m.defaultProject = msg.defaultProject
		}
	case daemonConfigMsg:
		if msg.err == nil {
			m.defaultProject = msg.config.TUI.DefaultProject
		}
	case configSavedMsg:
		if msg.err == nil {
			m.defaultProject = msg.cfg.TUI.DefaultProject
		}
	}
}

// maybeResolveStartup starts the chain once both arrivals are in, fetching
// the worktree listings only when there is a working directory to match.
func (m *root) maybeResolveStartup() tea.Cmd {
	s := &m.startup
	if s.done || s.fetching || !s.cfgSeen || !s.listed {
		return nil
	}
	s.fetching = true
	client := m.client
	if s.cwd == "" || client == nil {
		return func() tea.Msg { return startupWorktreesMsg{} }
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		var out startupWorktreesMsg
		if tasks, err := client.ListTasks(ctx, apiclient.ListTasksOptions{IncludeChildren: true}); err == nil {
			out.tasks = tasks
		}
		if chats, err := client.ListChats(ctx, apiclient.ListChatsOptions{}); err == nil {
			out.chats = chats
		}
		return out
	}
}

// resolveStartup runs the chain and selects its answer.
func (m *root) resolveStartup(msg startupWorktreesMsg) tea.Cmd {
	if m.startup.done {
		return nil
	}
	m.startup.done = true
	pick := resolveStartupProject(startupInputs{
		flag:           m.startup.flag,
		cwd:            m.startup.cwd,
		defaultProject: m.startup.defaultProject,
		lastUsed:       readTUIState(m.dataDir).SelectedProject,
		projects:       m.projects,
		tasks:          msg.tasks,
		chats:          msg.chats,
	})
	if !pick.ok {
		return nil
	}
	m.selNotice = pick.notice
	m.selNoticeWarn = strings.Contains(pick.notice, "is not registered")
	return m.selectProject(pick.project, pick.why)
}

// saveSelection persists the selection off the update loop (decision 31). A
// failed write is not reported, for saveFolds' reason: the selection holds on
// screen either way, and the only consequence is that the next launch falls
// through to a rule below last-used.
func (m *root) saveSelection() tea.Cmd {
	dir, sel := m.dataDir, selectedProjectState{ID: m.sel.id, Name: m.sel.name}
	if dir == "" || sel.ID == 0 {
		return nil
	}
	return func() tea.Msg {
		_ = mergeTUIState(dir, "selected_project", sel)
		return nil
	}
}
