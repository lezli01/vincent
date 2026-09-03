package tui

import (
	"slices"

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

// taskPath is the group a task sits in under one grouping — the path `←`
// collapses when the cursor is on it.
func taskPath(t apiclient.Task, g grouping) foldPath {
	p := make(foldPath, 0, len(g))
	for _, k := range g {
		p = append(p, groupValue(t, k))
	}
	return p
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

// loadFolds reads the persisted set. Every failure — no file, an unreadable
// one, a half-written one — answers "everything expanded", which is the
// fail-open direction tui.json's existing contract already uses and is
// exactly the board this feature's users had yesterday.
func loadFolds(dataDir string) foldSet {
	return foldSet(readTUIState(dataDir).BoardFolds)
}

// writeFolds persists the set, merging into whatever else tui.json holds so
// the first-run acknowledgment — and any field a later build adds — survives.
func writeFolds(dataDir string, f foldSet) error {
	if f == nil {
		f = foldSet{}
	}
	return mergeTUIState(dataDir, foldsKey, f)
}

// foldsKey is the tui.json field. It is the JSON tag on tuiState.BoardFolds;
// mergeTUIState writes into a map, so the two are held together here rather
// than by the compiler.
const foldsKey = "board_folds"

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
	return taskPath(r.task, b.group), true
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
	if b.folds.has(p) {
		if len(p) == 1 {
			return nil // already at the outermost level
		}
		p = p[:len(p)-1]
	}
	b.folds = b.folds.with(p)
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
	b.folds = b.folds.without(r.path)
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
	next := b.folds
	for _, r := range b.allRows() {
		if r.header {
			next = next.with(r.path)
		}
	}
	if len(next) == len(b.folds) && len(p) == 0 {
		return nil
	}
	b.folds = next
	if len(p) > 0 {
		b.focusPath(p[:1])
	}
	return b.saveFolds()
}

// expandAll is `O`: nothing folded anywhere, which is the board a fresh
// install renders. A cursor parked on a header moves onto the first task that
// header was standing in for.
func (b *board) expandAll() tea.Cmd {
	if len(b.folds) == 0 {
		return nil
	}
	p, onHeader := b.cursorPath()
	if r, ok := b.cursorRow(); !ok || !r.header {
		onHeader = false
	}
	b.folds = nil
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
	if len(b.folds) == 0 {
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
	p := taskPath(t, b.group)
	next := b.folds
	for n := 1; n <= len(p); n++ {
		next = next.without(p[:n])
	}
	if len(next) == len(b.folds) {
		return false
	}
	b.folds = next
	b.selectedPath = nil
	return true
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
	dir, folds := b.dataDir, slices.Clone(b.folds)
	if dir == "" {
		return nil
	}
	return func() tea.Msg {
		_ = writeFolds(dir, folds)
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
	_ = writeFolds(b.dataDir, b.folds)
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
	p := taskPath(t, b.group)
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
