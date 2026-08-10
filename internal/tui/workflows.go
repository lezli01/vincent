package tui

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// scopeProject is the scope string the registry gives an entry that lives in
// a project's own .vincent/workflows (§5.2).
const scopeProject = "project"

// Workflows messages.
type (
	workflowsRefreshMsg struct{}
	// workflowsLoadedMsg carries the whole assembled registry. err is the
	// global fetch failing, which is the only failure that costs the view its
	// contents; a project whose fetch failed carries its error in its block.
	workflowsLoadedMsg struct {
		blocks []wfBlock
		err    error
	}
	// workflowEditedMsg reports that $EDITOR exited. It carries no content:
	// what the file now says reaches the view through the registry reload,
	// which is the same path an external editor takes.
	workflowEditedMsg struct{ err error }
)

// wfBlock is one scope's entries. The global block comes first and each
// project follows in project-list order, because shadowing is a relationship
// between scopes: a row saying "shadows global X" needs the global block on
// screen to point at.
type wfBlock struct {
	name      string
	projectID int64
	entries   []apiclient.WorkflowEntry
	// err is this scope's fetch failing. It degrades the block alone — one
	// unreadable project must not blank the registry.
	err error
}

// wfLine is one rendered line. Only lines with an entry are selectable, so
// the cursor skips headers and per-block errors.
type wfLine struct {
	header string
	block  *wfBlock
	entry  *apiclient.WorkflowEntry
	// shadows names the global entry this project entry hides.
	shadows string
}

// workflowsView is §15's view 5: the merged registry, live.
type workflowsView struct {
	client *apiclient.Client
	exec   execFunc
	now    func() time.Time

	blocks   []wfBlock
	loaded   bool
	loadErr  error
	lastLoad time.Time

	cursor   int
	expanded bool
	err      string

	refreshPending bool
	width, height  int
}

func newWorkflowsView() *workflowsView {
	return &workflowsView{exec: tea.ExecProcess, now: time.Now}
}

func (w *workflowsView) title() string { return "Workflows" }

func (w *workflowsView) setClient(c *apiclient.Client) tea.Cmd {
	w.client = c
	return w.loadCmd()
}

// hintedProject lets `n` open the new-task form on the scope under the
// cursor; the global block hints nothing.
func (w *workflowsView) hintedProject() int64 {
	line, ok := w.currentLine()
	if !ok || line.block == nil {
		return 0
	}
	return line.block.projectID
}

// loadCmd assembles the registry. GET /v1/workflows lists global entries
// alone, and with a project_id it lists that project's view of the registry
// with §5.2 shadowing applied — so the merge takes the global list whole and
// keeps only the project-scoped entries from each project's list. Anything
// else double-counts a global entry that a project also sees.
func (w *workflowsView) loadCmd() tea.Cmd {
	client := w.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		global, err := client.ListWorkflows(ctx, 0)
		if err != nil {
			return workflowsLoadedMsg{err: err}
		}
		sortEntries(global)
		blocks := []wfBlock{{name: "global", entries: global}}

		projects, err := client.ListProjects(ctx)
		if err != nil {
			// The global block is real and worth showing; the project blocks
			// are simply unknown until the list comes back.
			return workflowsLoadedMsg{blocks: blocks}
		}
		for _, p := range projects {
			block := wfBlock{name: p.Name, projectID: p.ID}
			entries, err := client.ListWorkflows(ctx, p.ID)
			if err != nil {
				block.err = err
			} else {
				block.entries = projectScoped(entries)
				sortEntries(block.entries)
			}
			blocks = append(blocks, block)
		}
		return workflowsLoadedMsg{blocks: blocks}
	}
}

// projectScoped keeps the entries a project owns. The rest of the response
// is the global registry as that project sees it, already in the global
// block.
func projectScoped(entries []apiclient.WorkflowEntry) []apiclient.WorkflowEntry {
	out := make([]apiclient.WorkflowEntry, 0, len(entries))
	for _, e := range entries {
		if e.Scope == scopeProject {
			out = append(out, e)
		}
	}
	return out
}

// sortEntries orders a block alphabetically. Invalid entries are deliberately
// not floated to the top: a workflow that moves when you break it is harder
// to find, not easier.
func sortEntries(entries []apiclient.WorkflowEntry) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
}

func (w *workflowsView) scheduleRefresh() tea.Cmd {
	if w.refreshPending {
		return nil
	}
	w.refreshPending = true
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return workflowsRefreshMsg{} })
}

func (w *workflowsView) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height = msg.Width, msg.Height
		return w, nil
	case viewActivatedMsg:
		if msg.id == viewWorkflows {
			return w, w.loadCmd()
		}
		return w, nil
	case workflowsRefreshMsg:
		w.refreshPending = false
		return w, w.loadCmd()
	case workflowsLoadedMsg:
		w.applyLoaded(msg)
		return w, nil
	case workflowEditedMsg:
		if msg.err != nil {
			w.err = "editor: " + errString(msg.err)
		}
		// Nothing else: the watcher reloads the registry, the daemon writes
		// workflow.registry_changed, and the refetch that event triggers is
		// what puts the new content on screen — the same path an external
		// editor takes, which is what the acceptance criterion asks for.
		return w, nil
	case noteMsg:
		return w, w.updateNote(msg.note)
	case tea.KeyPressMsg:
		return w.updateKey(msg)
	}
	return w, nil
}

func (w *workflowsView) applyLoaded(msg workflowsLoadedMsg) {
	if msg.err != nil {
		// Keep the last-good registry behind the warning: a failed refresh is
		// not an empty registry.
		w.loadErr = msg.err
		return
	}
	w.loadErr = nil
	w.loaded = true
	w.lastLoad = w.now()
	w.blocks = msg.blocks
	w.snapCursor()
}

// snapCursor puts the cursor on a selectable line. Line 0 is always a scope
// header, so a freshly loaded view would otherwise open with `e` and `enter`
// pointing at nothing.
func (w *workflowsView) snapCursor() {
	lines := w.lines()
	if w.cursor >= 0 && w.cursor < len(lines) && lines[w.cursor].entry != nil {
		return
	}
	for i := range lines {
		if lines[i].entry != nil {
			w.cursor = i
			return
		}
	}
	w.cursor = 0
}

// updateNote refetches on a registry reload and on project changes, which add
// and remove whole scopes. The registry event carries an empty payload — it
// names no scope — so the only honest reaction is to refetch everything.
func (w *workflowsView) updateNote(n apiclient.Note) tea.Cmd {
	ev, ok := n.(apiclient.EventNote)
	if !ok {
		return nil
	}
	if ev.Event.Type == eventWorkflowRegistryChanged ||
		strings.HasPrefix(ev.Event.Type, "project.") {
		return w.scheduleRefresh()
	}
	return nil
}

func (w *workflowsView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		w.moveCursor(-1)
	case "down", "j":
		w.moveCursor(1)
	case "enter":
		w.expanded = !w.expanded
	case "e":
		return w, w.editCmd()
	case "R":
		w.err = ""
		return w, w.loadCmd()
	case "esc":
		w.err = ""
	}
	return w, nil
}

// moveCursor walks the selectable lines, skipping headers and block errors.
func (w *workflowsView) moveCursor(delta int) {
	lines := w.lines()
	i := w.cursor + delta
	for i >= 0 && i < len(lines) {
		if lines[i].entry != nil {
			w.cursor = i
			return
		}
		i += delta
	}
}

func (w *workflowsView) currentLine() (wfLine, bool) {
	lines := w.lines()
	if w.cursor < 0 || w.cursor >= len(lines) {
		return wfLine{}, false
	}
	line := lines[w.cursor]
	if line.entry == nil {
		return wfLine{}, false
	}
	return line, true
}

// editCmd opens the entry's own file. An entry with no file is the built-in
// adhoc: there is nothing on disk to open, so `e` is absent there rather
// than failing — the same rule that keeps edit+retry off a manual step.
func (w *workflowsView) editCmd() tea.Cmd {
	line, ok := w.currentLine()
	if !ok {
		return nil
	}
	if line.entry.File == "" {
		w.err = line.entry.Name + " is built in — there is no file to edit"
		return nil
	}
	w.err = ""
	return openEditorPath(w.exec, line.entry.File, func(err error) tea.Msg {
		return workflowEditedMsg{err: err}
	})
}

// lines flattens the blocks into the rendered order, resolving which project
// entries shadow a global one on the way.
func (w *workflowsView) lines() []wfLine {
	global := map[string]bool{}
	for i := range w.blocks {
		if w.blocks[i].projectID != 0 {
			continue
		}
		for _, e := range w.blocks[i].entries {
			global[e.Name] = true
		}
	}
	var out []wfLine
	for i := range w.blocks {
		b := &w.blocks[i]
		out = append(out, wfLine{header: b.name, block: b})
		for j := range b.entries {
			line := wfLine{block: b, entry: &b.entries[j]}
			if b.projectID != 0 && global[b.entries[j].Name] {
				line.shadows = b.entries[j].Name
			}
			out = append(out, line)
		}
	}
	return out
}

// capturesInput is always false: this view has no text entry, so the global
// single-key bindings keep working throughout.
func (w *workflowsView) capturesInput() bool { return false }
