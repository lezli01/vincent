package tui

import (
	"context"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// scopeProject is the scope string the registry gives an entry that lives in
// a project's own .vincent/workflows (§5.2).
const scopeProject = "project"

// Workflows messages.
type (
	workflowsRefreshMsg struct{}
	// workflowsLoadedMsg carries the two listings the view resolves (task 132
	// decision 7 and decision 39): global is builtin + global, own is the
	// selected project's own entries. err is the global fetch failing, which
	// is the only failure that costs the view its contents; ownErr is the
	// project fetch failing, which degrades the view to the global rows and
	// an error line.
	workflowsLoadedMsg struct {
		stamp  loadStamp
		global []apiclient.WorkflowEntry
		own    []apiclient.WorkflowEntry
		ownErr error
		err    error
	}
	// workflowEditedMsg reports that $EDITOR exited. It carries no content:
	// what the file now says reaches the view through the registry reload,
	// which is the same path an external editor takes.
	workflowEditedMsg struct{ err error }
	// workflowResolvedMsg carries POST /v1/resolve for one expanded entry —
	// which adapter each agent step would actually run (§8.6, T4.7).
	workflowResolvedMsg struct {
		key        wfResolveKey
		resolution apiclient.Resolution
		err        error
	}
)

// wfResolveKey identifies an entry by the scope that owns its file: global
// and builtin entries by 0, the selected project's own by its id. The same
// name means different files in the two, and an overridden global row must
// still resolve, open and fork as the global file it is.
type wfResolveKey struct {
	projectID int64
	name      string
}

// wfLine is one rendered line of the resolved list. Only lines with an entry
// are selectable, so the cursor skips the error line.
type wfLine struct {
	// note is a non-entry line: the project fetch failing.
	note  string
	entry *apiclient.WorkflowEntry
	// projectID is the scope that owns entry's file (see wfResolveKey).
	projectID int64
	// shadows is set on a project entry that overrides a global or builtin
	// one of the same name: the overridden entry's scope.
	shadows string
	// shadowedBy is set on a global or builtin entry the selected project
	// overrides: the project's name. The row stays listed, dimmed, so the
	// global file is still reachable from here (decision 40).
	shadowedBy string
}

// key is the line's resolve/definition key.
func (l wfLine) key() wfResolveKey {
	return wfResolveKey{projectID: l.projectID, name: l.entry.Name}
}

// workflowsView is §15's view 5: the merged registry, live.
type workflowsView struct {
	// projectScope is the root's selected project (task 132.2). A switch
	// reloads, and stamps drops a load issued for the previous project or
	// overtaken by a newer one (task 132.5).
	projectScope
	stamps loadStamps

	client *apiclient.Client
	exec   execFunc
	now    func() time.Time

	// global is the builtin + global listing, own the selected project's
	// own entries; lines resolves the two into one list.
	global   []apiclient.WorkflowEntry
	own      []apiclient.WorkflowEntry
	ownErr   error
	loaded   bool
	loadErr  error
	lastLoad time.Time

	cursor   int
	expanded bool
	err      string

	// resolutions caches what the daemon says each expanded entry resolves
	// to, keyed by scope + name. A registry reload drops the cache: the file
	// that just changed is exactly the one whose resolution may have moved.
	resolutions map[wfResolveKey]apiclient.Resolution

	// editor is the open structured editor, nil when the list has the
	// keyboard. It is the same nullable-sub-model shape as graph (task 065).
	editor *wfEditorLayer
	// create is the open create/fork prompt, nil otherwise.
	create *wfCreateForm

	// graph is the open graph sub-layer, nil when the list has the keyboard.
	// It follows the shape projectsView uses for its form: a nullable
	// sub-model that takes the keys and renders in place (decision 13).
	graph *graphLayer

	// vp scrolls the list. Without it a registry taller than the terminal is
	// simply cut off — a truncation that predates the graph and that the
	// graph makes worse, because Escape returns you to a list that may not
	// show the row you came from (017.9).
	vp viewport.Model

	refreshPending bool
	width, height  int
}

func newWorkflowsView() *workflowsView {
	w := &workflowsView{exec: tea.ExecProcess, now: time.Now, vp: viewport.New()}
	w.reload = w.loadCmd
	return w
}

func (w *workflowsView) title() string { return "Workflows" }

func (w *workflowsView) setClient(c *apiclient.Client) tea.Cmd {
	w.client = c
	return w.loadCmd()
}

// hintedProject is the selected project: the list shows that project's
// resolved registry, so no row names another one to hint (task 132.12).
func (w *workflowsView) hintedProject() int64 { return w.project.id }

// loadCmd fetches the selected project's resolved registry with two calls
// (task 132 decision 39). GET /v1/workflows with a project_id merges by name,
// so a global or builtin entry the project overrides is missing from it;
// the unscoped listing supplies those, and lines compares the two. Nothing
// is fetched for any other project. With no project selected only the
// global listing is fetched.
func (w *workflowsView) loadCmd() tea.Cmd {
	client := w.client
	if client == nil {
		return nil
	}
	project := w.project.id
	stamp := w.stamps.next(project)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		global, err := client.ListWorkflows(ctx, 0)
		if err != nil {
			return workflowsLoadedMsg{stamp: stamp, err: err}
		}
		msg := workflowsLoadedMsg{stamp: stamp, global: global}
		if project == 0 {
			return msg
		}
		entries, err := client.ListWorkflows(ctx, project)
		if err != nil {
			// The global rows are real and worth showing; the project's own
			// are simply unknown until its listing comes back.
			msg.ownErr = err
			return msg
		}
		msg.own = ownEntries(entries)
		return msg
	}
}

// ownEntries keeps the entries a project owns. The rest of the response
// is the global registry as that project sees it, already in the global
// listing.
func ownEntries(entries []apiclient.WorkflowEntry) []apiclient.WorkflowEntry {
	out := make([]apiclient.WorkflowEntry, 0, len(entries))
	for _, e := range entries {
		if e.Scope == scopeProject {
			out = append(out, e)
		}
	}
	return out
}

// sortLines orders the resolved list alphabetically, a project entry ahead
// of the global or builtin one it shadows. Invalid entries are deliberately
// not floated to the top: a workflow that moves when you break it is harder
// to find, not easier.
func sortLines(lines []wfLine) {
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].entry.Name != lines[j].entry.Name {
			return lines[i].entry.Name < lines[j].entry.Name
		}
		return lines[i].projectID != 0 && lines[j].projectID == 0
	})
}

func (w *workflowsView) scheduleRefresh() tea.Cmd {
	if w.refreshPending {
		return nil
	}
	w.refreshPending = true
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return workflowsRefreshMsg{} })
}

func (w *workflowsView) update(msg tea.Msg) (panel, tea.Cmd) {
	if p, cmd, handled := w.updateEditorMsg(msg); handled {
		return p, cmd
	}
	if p, cmd, handled := w.updateCreateMsg(msg); handled {
		return p, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height = msg.Width, msg.Height
		w.sizeGraph()
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
		return w, w.resolveCmd()
	case workflowResolvedMsg:
		if msg.err == nil {
			if w.resolutions == nil {
				w.resolutions = map[wfResolveKey]apiclient.Resolution{}
			}
			w.resolutions[msg.key] = msg.resolution
		}
		return w, nil
	case workflowDefinitionMsg:
		w.applyDefinition(msg)
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
	case tea.MouseClickMsg, tea.MouseWheelMsg:
		w.updateGraphMouse(msg)
		return w, nil
	}
	return w, nil
}

func (w *workflowsView) applyLoaded(msg workflowsLoadedMsg) {
	if !w.stamps.accepts(msg.stamp) {
		return // an older load landing late, or one for the previous project
	}
	w.stamps.apply(msg.stamp)
	if msg.err != nil {
		// Keep the last-good registry behind the warning: a failed refresh is
		// not an empty registry.
		w.loadErr = msg.err
		return
	}
	w.loadErr = nil
	w.loaded = true
	w.lastLoad = w.now()
	w.global, w.own, w.ownErr = msg.global, msg.own, msg.ownErr
	// A reload is the one event that can change what a step resolves to, so
	// nothing cached against the old registry survives it.
	w.resolutions = nil
	w.snapCursor()
}

// resolveCmd fetches the resolution for the entry under the cursor, once.
// It is called when the cursor moves and when the registry reloads — the
// step list only shows an expanded entry, but resolving on cursor movement
// means the names are already there when it opens.
func (w *workflowsView) resolveCmd() tea.Cmd {
	client := w.client
	line, ok := w.currentLine()
	if client == nil || !ok {
		return nil
	}
	key := line.key()
	if _, cached := w.resolutions[key]; cached {
		return nil
	}
	req := apiclient.ResolveRequest{Workflow: key.name}
	if key.projectID != 0 {
		req.ProjectID = ptr(key.projectID)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		res, err := client.Resolve(ctx, req)
		return workflowResolvedMsg{key: key, resolution: res, err: err}
	}
}

// snapCursor puts the cursor on a selectable line. Line 0 may be the project
// fetch's error line, so a freshly loaded view would otherwise open with `e`
// and `enter` pointing at nothing.
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

// updateNote refetches on a registry reload and on project changes, which can
// rename the project whose entries are listed. The registry event carries an empty payload — it
// names no scope — so the only honest reaction is to refetch everything.
func (w *workflowsView) updateNote(n apiclient.Note) tea.Cmd {
	ev, ok := n.(apiclient.EventNote)
	if !ok || !forProject(ev.Event, w.project.id) {
		return nil
	}
	if ev.Event.Type == eventWorkflowRegistryChanged ||
		strings.HasPrefix(ev.Event.Type, "project.") {
		cmds := []tea.Cmd{w.scheduleRefresh()}
		// An open graph refetches too, and re-lays-out in place. Live reload
		// reflecting saves immediately is this view's stated §15 behaviour,
		// and a graph that went stale while you edited the file under it
		// would be the wrong half of that promise (decision 19).
		if w.graph != nil {
			w.graph.loading = true
			cmds = append(cmds, w.definitionCmd(w.graph.key))
		}
		return tea.Batch(cmds...)
	}
	return nil
}

func (w *workflowsView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	switch {
	case w.create != nil:
		return w.updateCreateKey(msg)
	case w.editor != nil:
		return w.updateEditorKey(msg)
	case w.graph != nil:
		return w.updateGraphKey(msg)
	}
	switch msg.String() {
	case "up", "k":
		w.moveCursor(-1)
		return w, w.resolveCmd()
	case "down", "j":
		w.moveCursor(1)
		return w, w.resolveCmd()
	case "enter":
		w.expanded = !w.expanded
		return w, w.resolveCmd()
	case "g":
		return w, w.openGraph()
	case opKey(keymap.Editor):
		// Unchanged: `e` means $EDITOR here and in the six other contexts
		// bindings.go gives it, so the structured editor takes its own keys
		// rather than giving one key two meanings (task 065 decision 6).
		return w, w.editCmd()
	case "i":
		return w, w.openEditor()
	case opKey(keymap.Add):
		w.openCreate(false)
		return w, nil
	case "f":
		w.openCreate(true)
		return w, nil
	case opKey(keymap.Refresh):
		w.err = ""
		return w, w.loadCmd()
	case "esc":
		// One layer per press (§15): an error note clears first; with
		// nothing left, the takeover closes.
		if w.err != "" {
			w.err = ""
			return w, nil
		}
		return w, func() tea.Msg { return selectViewMsg{id: viewHome} }
	}
	return w, nil
}

// moveCursor walks the selectable lines, skipping the error line.
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

// lines resolves the two listings into one list sorted by name (task 132
// decision 7): every global and builtin entry, and the selected project's
// own entries beside them. A project entry whose name a global or builtin
// one also has shadows it (§5.2); both stay listed, the overridden one
// dimmed, so the global file stays reachable (decision 40). Duplicate-name
// losers arrive as extra invalid entries and are listed like any other.
func (w *workflowsView) lines() []wfLine {
	overridden := map[string]bool{}
	for _, e := range w.own {
		overridden[e.Name] = true
	}
	globalScope := map[string]string{}
	var out []wfLine
	if w.ownErr != nil {
		out = append(out, wfLine{note: w.project.name + ": registry unavailable: " + errString(w.ownErr)})
	}
	entries := make([]wfLine, 0, len(w.global)+len(w.own))
	for i := range w.global {
		e := &w.global[i]
		if _, seen := globalScope[e.Name]; !seen {
			globalScope[e.Name] = e.Scope
		}
		line := wfLine{entry: e}
		if overridden[e.Name] {
			line.shadowedBy = w.project.name
		}
		entries = append(entries, line)
	}
	for i := range w.own {
		e := &w.own[i]
		entries = append(entries, wfLine{entry: e, projectID: w.project.id, shadows: globalScope[e.Name]})
	}
	sortLines(entries)
	return append(out, entries...)
}

// capturesInput is true only while a form row or the create prompt has a
// focused text field: typing "q" into a workflow's description must not quit.
func (w *workflowsView) capturesInput() bool {
	if w.create != nil {
		return w.create.capturing()
	}
	return w.editor != nil && w.editor.capturing()
}
