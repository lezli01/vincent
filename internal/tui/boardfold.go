package tui

import (
	"maps"
	"slices"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Collapsible groups on the task board (§15, task 054).
//
// A board carrying six projects and a long tail of finished tasks pushes the
// project you are working in off the bottom of the screen, and grouping does
// nothing about it: `g` changes the shape of the table, never the number of
// rows in it. Folding is the missing half — and it is a *view* over the same
// band-sorted list, applied after groupRows has already laid it out, so 009
// decision 2 (group order and within-group order come from the band sort)
// survives untouched.
//
// Task 009 decision 4 rejected collapsing outright, naming one concrete
// failure: a collapsed group holding an awaiting_input task. That half of the
// decision is deliberately superseded here, and three things answer the
// failure it named — the header keeps its count and its `! n` attention badge
// through a fold, `!` expands whatever group it lands in, and a fold opens by
// itself the moment a task inside it enters awaiting_input (board.expandFor).
// Nothing is ever *refused* a collapse, which is what would make the feature
// unpredictable on exactly the busy board that wants it.

// foldPath names one group by the labels of its header and every header above
// it, outermost first: ["api", "build"] is the `build` workflow inside the
// `api` project. Labels rather than indices, so a refetch that reorders the
// board — or a `g` press that renders a different set of levels — leaves the
// set meaning what it meant.
type foldPath []string

func (p foldPath) equal(other foldPath) bool { return slices.Equal(p, other) }

// covers reports that p names a group containing the group (or task) named by
// child: a fold at ["api"] hides everything under ["api", "build"].
func (p foldPath) covers(child foldPath) bool {
	return len(p) <= len(child) && p.equal(child[:len(p)])
}

// foldSet is the collapsed groups. A slice rather than a map for the reason
// markSet is one: it is tens of entries at most, and a stable order makes what
// it feeds — the JSON written to tui.json, and the tests that pin it —
// deterministic.
type foldSet []foldPath

func (f foldSet) has(p foldPath) bool {
	return slices.ContainsFunc(f, p.equal)
}

// with collapses a group, kept sorted so two boards that folded the same
// groups in a different order write the same file.
func (f foldSet) with(p foldPath) foldSet {
	if len(p) == 0 || f.has(p) {
		return f
	}
	out := append(slices.Clone(f), slices.Clone(p))
	slices.SortFunc(out, slices.Compare)
	return out
}

// without expands one group. It is exactly one level: `→` walks down the tree
// a step at a time, so unfolding a project must leave a workflow inside it
// folded if that is how it was left.
func (f foldSet) without(p foldPath) foldSet {
	i := slices.IndexFunc(f, p.equal)
	if i < 0 {
		return f
	}
	out := slices.Delete(slices.Clone(f), i, i+1)
	if len(out) == 0 {
		return nil
	}
	return out
}

// prune drops paths that name nothing on the board any more.
//
// A path survives while every segment is still a project name or a workflow
// name occurring somewhere in the *unfiltered* task list, independent of which
// levels are currently rendered (task 054 decision 4). Pruning against the
// rendered grouping instead would make `g` destructive: cycling to
// project-only grouping stops every [project, workflow] path from appearing,
// and the issue's other acceptance criterion is that folds survive `g` cycling
// away and back. A filter leaves the set alone for the same reason.
//
// An empty list prunes nothing: a TUI whose daemon went away holds no news
// about which projects exist, and forgetting every fold on a reconnect blip is
// worse than keeping a dead one until the next successful load.
func (f foldSet) prune(tasks []apiclient.Task) foldSet {
	if len(f) == 0 || len(tasks) == 0 {
		return f
	}
	known := make(map[string]struct{}, len(tasks)*2)
	for _, t := range tasks {
		known[groupValue(t, groupProject)] = struct{}{}
		known[groupValue(t, groupWorkflow)] = struct{}{}
	}
	out := make(foldSet, 0, len(f))
	for _, p := range f {
		if len(p) == 0 {
			continue
		}
		live := true
		for _, seg := range p {
			if _, ok := known[seg]; !ok {
				live = false
				break
			}
		}
		if live {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// taskPath is the innermost group a task sits in on screen — the path `←`
// collapses when the cursor is on it. It is the path of the deepest level
// that draws a header (shown, from shownLevels), and so carries the value of
// every configured level above it, skipped ones included, the way the
// header's own path does. Nil when no level draws a header: there is no group
// on screen to fold.
func taskPath(t apiclient.Task, g, shown grouping) foldPath {
	return headerPaths(t, g, shown).innermost()
}

// headerPaths is the path of every header above a task on screen, outermost
// first — one per shown level. A skipped level has no header, so it has no
// entry here, which is what keeps `!` and an awaiting_input transition from
// rewriting a fold they could not see (task 129 decision 4).
func headerPaths(t apiclient.Task, g, shown grouping) foldPaths {
	var out foldPaths
	p := make(foldPath, 0, len(g))
	for _, k := range g {
		p = append(p, groupValue(t, k))
		if shown.has(k) {
			out = append(out, slices.Clone(p))
		}
	}
	return out
}

// foldPaths is the headers above one task, outermost first.
type foldPaths []foldPath

func (ps foldPaths) innermost() foldPath {
	if len(ps) == 0 {
		return nil
	}
	return ps[len(ps)-1]
}

// applyFolds is the fold view: the same rows, with the subtree of every
// collapsed header removed. It runs after groupRows rather than inside it so
// that the ordering groupRows produces is provably untouched — a folded board
// is the unfolded one with rows deleted, never a second sort.
//
// A collapsed header additionally reports how many of the tasks it swallowed
// are marked: the bulk selection is a set of tasks and not a view (task 011),
// so `V` marks inside a fold and the header is what keeps that honest.
func applyFolds(rows []boardRow, folds foldSet, marks markSet) []boardRow {
	if len(folds) == 0 {
		return rows
	}
	out := make([]boardRow, 0, len(rows))
	// depth of the collapsed header currently swallowing rows, and where it
	// landed in out; -1 when nothing is being swallowed.
	hidingAt, header := -1, -1
	for _, r := range rows {
		if hidingAt >= 0 {
			if r.depth > hidingAt {
				if !r.header && marks.has(r.task.ID) {
					out[header].marked++
				}
				continue
			}
			hidingAt, header = -1, -1
		}
		if r.header && folds.has(r.path) {
			r.collapsed = true
			out = append(out, r)
			hidingAt, header = r.depth, len(out)-1
			continue
		}
		out = append(out, r)
	}
	return out
}

// headerIndex is where a path's header sits in a rendered row set, or -1.
func headerIndex(rows []boardRow, p foldPath) int {
	return slices.IndexFunc(rows, func(r boardRow) bool { return r.header && r.path.equal(p) })
}

// landingRow is the first row at or after i the cursor may rest on: a task, or
// a collapsed header standing in for its tasks. An expanded header names rows
// that are present, so it stays a label the cursor steps over (task 054
// decision 2), and a wrapped row's continuations belong to the line above
// them — boardRow.selectable is the one definition of both.
// Returns -1 when there is nothing below.
func landingRow(rows []boardRow, i int) int {
	for ; i < len(rows); i++ {
		if rows[i].selectable() {
			return i
		}
	}
	return -1
}

// projectFolds is the task board's fold sets keyed by project id (task 132
// decision 35): a board's folds are independent per project whatever
// `group_by` says, so a `["build"]` fold under a `[workflow]` grouping
// collapses `build` in the one project it was made in. Keyed by id rather
// than by name, so a rename does not move or lose them.
type projectFolds map[int64]foldSet

// with returns a copy with project's set replaced; an empty set removes the
// entry, so the file does not collect empty lists.
func (pf projectFolds) with(project int64, f foldSet) projectFolds {
	out := make(projectFolds, len(pf)+1)
	for id, set := range pf {
		out[id] = set
	}
	if len(f) == 0 {
		delete(out, project)
	} else {
		out[project] = f
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// prune prunes each project's set against that project's own tasks, taken
// from the global live list. A project with no live tasks keeps its set: an
// empty list prunes nothing (foldSet.prune), which is the same rule one
// project at a time.
func (pf projectFolds) prune(tasks []apiclient.Task) (projectFolds, bool) {
	out, changed := pf, false
	for id, set := range pf {
		pruned := set.prune(tasksInProject(tasks, id))
		if len(pruned) != len(set) {
			out, changed = out.with(id, pruned), true
		}
	}
	return out, changed
}

// keepProjects drops the sets of projects that are no longer registered.
func (pf projectFolds) keepProjects(ids map[string]int64) (projectFolds, bool) {
	live := make(map[int64]bool, len(ids))
	for _, id := range ids {
		live[id] = true
	}
	out, changed := pf, false
	for id := range pf {
		if !live[id] {
			out, changed = out.with(id, nil), true
		}
	}
	return out, changed
}

// migrateLegacyFolds moves a pre-132.8 `board_folds` list under project ids
// (task 132 decision 35). A path whose first segment names a registered
// project moves under that project's id. It keeps that segment: a skipped
// level still contributes its value to every header path under it (task 129
// decision 4, headerPaths), so under the default `[project, workflow]`
// grouping the scoped board's `build` header is still ["api", "build"]. A
// path that is only the project segment named a project header, which a
// one-project board no longer draws, and is dropped. A path whose first
// segment names no project — a `[workflow]` grouping's — is dropped too:
// copying it into every project would recreate the sharing this keying
// removes.
func migrateLegacyFolds(pf projectFolds, legacy foldSet, ids map[string]int64) projectFolds {
	for _, p := range legacy {
		if len(p) < 2 {
			continue
		}
		id, ok := ids[p[0]]
		if !ok {
			continue
		}
		pf = pf.with(id, pf[id].with(p))
	}
	return pf
}

// loadFolds reads the persisted sets and any legacy list still waiting to be
// migrated. Every failure — no file, an unreadable one, a half-written one —
// answers "everything expanded", which is the fail-open direction tui.json's
// existing contract already uses and is exactly the board this feature's
// users had yesterday.
func loadFolds(dataDir string) (projectFolds, foldSet) {
	st := readTUIState(dataDir)
	var pf projectFolds
	for id, set := range st.BoardFoldsByProject {
		if len(set) > 0 {
			pf = pf.with(id, foldSet(set))
		}
	}
	return pf, foldSet(st.BoardFolds)
}

// writeFolds persists the sets, merging into whatever else tui.json holds so
// the first-run acknowledgment — and any field a later build adds —
// survives. dropLegacy removes the pre-132.8 field, which is done only once
// it has been migrated: until the project list is known it is held, not
// rewritten.
func writeFolds(dataDir string, pf projectFolds, dropLegacy bool) error {
	out := map[string][]foldPath{}
	for id, set := range pf {
		out[strconv.FormatInt(id, 10)] = set
	}
	var drop []string
	if dropLegacy {
		drop = append(drop, legacyFoldsKey)
	}
	return rewriteTUIState(dataDir, map[string]any{foldsKey: out}, drop...)
}

// foldsKey is the tui.json field, the JSON tag on
// tuiState.BoardFoldsByProject, and legacyFoldsKey the pre-132.8 one on
// tuiState.BoardFolds; mergeTUIState writes into a map, so the two are held
// together here rather than by the compiler.
const (
	foldsKey       = "board_folds_by_project"
	legacyFoldsKey = "board_folds"
)

// folds is the selected project's fold set: the one the render, the keys
// and `!`'s auto-expand read.
func (b *board) folds() foldSet { return b.foldsBy[b.project.id] }

// setFolds replaces the selected project's fold set.
func (b *board) setFolds(f foldSet) { b.foldsBy = b.foldsBy.with(b.project.id, f) }

// setProjects is the root handing over its cached project list. It runs
// the legacy migration once the list is known, and drops the fold sets of
// projects that have been removed.
func (b *board) setProjects(projects []apiclient.Project) {
	ids := make(map[string]int64, len(projects))
	for _, p := range projects {
		ids[p.Name] = p.ID
	}
	b.projectIDs = ids
	b.migrateFolds()
	if !b.foldsLoaded {
		return
	}
	if kept, changed := b.foldsBy.keepProjects(ids); changed {
		b.foldsBy = kept
		b.persistFolds()
	}
}

// migrateFolds moves the legacy list under project ids once both the file
// and the project list have been read, and writes the result back.
func (b *board) migrateFolds() {
	if b.foldsMigrated || !b.foldsLoaded || b.projectIDs == nil {
		return
	}
	b.foldsMigrated = true
	if b.legacyFolds == nil {
		return
	}
	b.foldsBy = migrateLegacyFolds(b.foldsBy, b.legacyFolds, b.projectIDs)
	b.legacyFolds = nil
	b.persistFolds()
}

// The four keys. `←`/`→` walk the tree one level at a time and `C`/`O` do the
// whole table — the same two letters, in the same meaning, the diff pane
// already teaches (task 012).
//
// None of them touches the cursor by index: each names the row it wants and
// lets the next render place it (restoreSelection), which is the rule `g`
// already follows. Reading a cursor index from the old layout against the new
// one is how a fold would silently select a different task.

// cursorRow is the row under the cursor in the current fold view.
func (b *board) cursorRow() (boardRow, bool) {
	rows := b.rows()
	i := b.tbl.Cursor()
	if i < 0 || i >= len(rows) {
		return boardRow{}, false
	}
	return rows[i], true
}

// cursorPath is the group the fold keys act on: the path of the collapsed
// header under the cursor, or the innermost group of the task under it.
func (b *board) cursorPath() (foldPath, bool) {
	r, ok := b.cursorRow()
	if !ok {
		return nil, false
	}
	if r.header {
		return r.path, true
	}
	shown, _ := b.shownGroup()
	return taskPath(r.task, b.group, shown), true
}

// collapseAtCursor is `←`: fold the innermost group the cursor is in, and
// leave the cursor on the header it just closed. Pressing it again on that
// header folds the parent, so ← walks outwards a level at a time and every
// nesting level is addressable without a "nearest above" heuristic.
func (b *board) collapseAtCursor() tea.Cmd {
	// The task under the cursor is what the detail panels go on showing once
	// the fold swallows its row, and where `→` and `O` come back to.
	b.rememberSelection()
	p, ok := b.cursorPath()
	if !ok || len(p) == 0 {
		return nil
	}
	if b.folds().has(p) {
		parent, ok := b.parentHeader(p)
		if !ok {
			return nil // already at the outermost level
		}
		p = parent
	}
	b.setFolds(b.folds().with(p))
	b.focusPath(p)
	return b.saveFolds()
}

// expandAtCursor is `→`: open the collapsed header under the cursor by
// exactly one level, and move the cursor onto the first thing it now shows —
// a task, or a sub-header that was left folded. An expanded header is a label
// again, so the cursor does not stay on it.
//
// `→` on a row that is not a collapsed header does nothing: it is a request
// to open *this* group, not a search for one somewhere else on the board.
func (b *board) expandAtCursor() tea.Cmd {
	r, ok := b.cursorRow()
	if !ok || !r.header || !r.collapsed {
		return nil
	}
	b.setFolds(b.folds().without(r.path))
	rows := b.rows()
	i := headerIndex(rows, r.path)
	if i < 0 {
		b.selectedPath = nil
		return b.saveFolds()
	}
	b.focusRow(rows, landingRow(rows, i+1))
	return b.saveFolds()
}

// collapseAll is `C`: fold every group at every level, and leave the cursor on
// the outermost header it was under. Every level rather than the top one
// alone, so `→` afterwards is the same one-level walk down it is anywhere else.
func (b *board) collapseAll() tea.Cmd {
	p, _ := b.cursorPath()
	next := b.folds()
	for _, r := range b.allRows() {
		if r.header {
			next = next.with(r.path)
		}
	}
	if len(next) == len(b.folds()) && len(p) == 0 {
		return nil
	}
	b.setFolds(next)
	if len(p) > 0 {
		b.focusPath(p[:1])
	}
	return b.saveFolds()
}

// expandAll is `O`: nothing folded anywhere, which is the board a fresh
// install renders. A cursor parked on a header moves onto the first task that
// header was standing in for.
func (b *board) expandAll() tea.Cmd {
	if len(b.folds()) == 0 {
		return nil
	}
	p, onHeader := b.cursorPath()
	if r, ok := b.cursorRow(); !ok || !r.header {
		onHeader = false
	}
	b.setFolds(nil)
	b.selectedPath = nil
	if onHeader && len(p) > 0 {
		rows := b.rows()
		if i := headerIndex(rows, p); i >= 0 {
			b.focusRow(rows, landingRow(rows, i+1))
		}
	}
	return b.saveFolds()
}

// expandFor opens every fold standing between the board and one task, and
// reports whether anything moved. It is what `!` and an incoming
// awaiting_input transition both call: a fold is never allowed to be the
// reason work waiting on a human cannot be reached (task 054 decision 3).
func (b *board) expandFor(id int64) bool {
	if len(b.folds()) == 0 {
		return false
	}
	// Every task the board can name, an expanded fan-out's lanes included
	// (boardlanes.go): a lane sits in its parent's group, so the fold hiding
	// it is the same one.
	t, ok := b.taskByID(id)
	if !ok {
		return false
	}
	// The path is read against the grouping on screen: what has to open is
	// what is hiding the task now.
	shown, _ := b.shownGroup()
	next := b.folds()
	for _, p := range headerPaths(t, b.group, shown) {
		next = next.without(p)
	}
	if len(next) == len(b.folds()) {
		return false
	}
	b.setFolds(next)
	b.selectedPath = nil
	return true
}

// parentHeader is the path of the header one level out from the header at p,
// skipping any level between them that draws no header (task 129 decision 4).
// It reports false for an outermost header.
func (b *board) parentHeader(p foldPath) (foldPath, bool) {
	shown, _ := b.shownGroup()
	for n := len(p) - 1; n > 0; n-- {
		if n <= len(b.group) && shown.has(b.group[n-1]) {
			return p[:n], true
		}
	}
	return nil, false
}

// focusPath parks the cursor on one collapsed header. The path is recorded
// whether or not the header is on screen yet, because the render that places
// the cursor is the one that will build the rows.
func (b *board) focusPath(p foldPath) {
	b.selectedPath = slices.Clone(p)
	rows := b.rows()
	if i := headerIndex(rows, p); i >= 0 {
		b.tbl.SetCursor(i)
	}
}

// focusRow parks the cursor on row i of a freshly computed row set.
func (b *board) focusRow(rows []boardRow, i int) {
	if i < 0 || i >= len(rows) {
		b.selectedPath = nil
		return
	}
	if rows[i].header {
		b.focusPath(rows[i].path)
		return
	}
	b.selectedPath = nil
	b.selectedID = rows[i].task.ID
	b.tbl.SetCursor(i)
}

// saveFolds persists the set off the update loop. A failed write is not
// reported: the fold is applied on screen either way, and the only
// consequence is that the next launch opens the group again — which is the
// same fail-open direction loadFolds takes.
func (b *board) saveFolds() tea.Cmd {
	dir, folds, drop := b.dataDir, maps.Clone(b.foldsBy), b.foldsMigrated
	if dir == "" {
		return nil
	}
	return func() tea.Msg {
		_ = writeFolds(dir, folds, drop)
		return nil
	}
}

// persistFolds is saveFolds for the callers that have no command to return —
// a prune inside a load handler. The write is small and on a file only this
// process owns; doing it inline costs one syscall on a path that already
// rebuilt the whole task list.
func (b *board) persistFolds() {
	if b.dataDir == "" {
		return
	}
	_ = writeFolds(b.dataDir, b.foldsBy, b.foldsMigrated)
}

// foldedHome is the collapsed header standing in for the remembered task,
// when a fold is what took its row away — a refetch or a `g` press landing on
// a board where the task's group is closed. The cursor belongs there rather
// than at the top of the list.
func (b *board) foldedHome(rows []boardRow) int {
	t, ok := b.taskByID(b.selectedID)
	if !ok {
		return -1
	}
	shown, _ := b.shownGroup()
	p := taskPath(t, b.group, shown)
	if len(p) == 0 {
		return -1
	}
	return slices.IndexFunc(rows, func(r boardRow) bool {
		return r.header && r.collapsed && r.path.covers(p)
	})
}

// chatFoldsKey is the chats board's field in tui.json, the JSON tag on
// tuiState.ChatFolds.
const chatFoldsKey = "chat_folds"

// loadChatFolds and writeChatFolds are loadFolds/writeFolds for the chats
// board, against its own key. Same file, same fail-open contract, same merge
// — a different list, because the two boards fold the same project names.
func loadChatFolds(dataDir string) foldSet { return foldSet(readTUIState(dataDir).ChatFolds) }

func writeChatFolds(dataDir string, f foldSet) error {
	if f == nil {
		f = foldSet{}
	}
	return mergeTUIState(dataDir, chatFoldsKey, f)
}
