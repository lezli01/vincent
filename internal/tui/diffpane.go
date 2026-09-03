package tui

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

const (
	// diffTimeout bounds the fetch: the endpoint shells out to git, which is
	// fast on a worktree and not instant on a large one.
	diffTimeout = 20 * time.Second
	// maxDiffLines caps what the pane renders. §18 allows an agent to touch a
	// vendored tree; the endpoint still served the whole diff and T4.3 owns
	// real limits, so this bounds the terminal, not the truth.
	maxDiffLines = 5000
)

var (
	styleDiffFile = lipgloss.NewStyle().Bold(true)
	styleDiffHunk = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleDiffAdd  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleDiffDel  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

// The fold glyphs, matching the board's group headers (boardgroup.go): a glyph
// rather than colour alone, so the state of a fold survives NO_COLOR and a
// 16-colour terminal (§15 Colour).
const (
	diffFoldClosed = "▸"
	diffFoldOpen   = "▾"
)

// diffKeySep joins the parts of a fold key. NUL cannot occur in a path or in a
// lane id, so a composed key can never collide with a plain one.
const diffKeySep = "\x00"

// diffLoadedMsg carries a fetched diff.
//
// sections is the attributed form (apiclient.DiffByLane) and is what the pane
// fetches. text is the flat diff, kept for a caller that only has one — the
// pane treats it as a single remainder section, which is the same thing the
// daemon returns for a task that fanned nothing out.
type diffLoadedMsg struct {
	taskID   int64
	text     string
	sections []apiclient.DiffSection
	err      error
}

// diffPane is the §15 diff tab: the worktree against merge-base with the base
// branch. It fetches when the tab opens and on an explicit refresh, never
// from the event stream — the endpoint runs git per call, so following events
// would mean a subprocess per event.
//
// The diff is grouped and folded shut (task 012, diffgroup.go): the pane opens
// on the list of files with their ± counts, and the reader expands the ones
// worth reading. For a fan-out parent there is an outer level above that
// (§7.6): the daemon attributes each hunk to the lane whose merge brought it
// in, and the tab reads lane › file. A file two lanes touched is listed under
// both, because two lanes really did change it and a single row would have to
// pick one of them to lie about.
type diffPane struct {
	taskID    int64
	lines     []string
	sections  []diffSection
	loaded    bool
	fetching  bool
	err       error
	truncated bool

	// grouped is whether the outer lane level renders at all. A task with no
	// lanes comes back as a lone remainder section and is drawn exactly as it
	// was before lanes existed — one flat list of files.
	grouped bool

	// open is the fold state, keyed by identity rather than by position: a
	// refresh re-parses the diff, and a file that grew a hunk in between must
	// not fold shut under the reader. A missing entry is closed, which is what
	// makes "everything starts collapsed" the default rather than a state to
	// initialise. Ungrouped, a key is just the file's path.
	open map[string]bool
	// nodes is the fold tree as it currently reads: every lane header, and the
	// files of the lanes that are open. It is what the cursor walks.
	nodes []diffNode
	// cursor is the node the fold keys act on. cursorPath is the same thing by
	// key, so a refresh restores the cursor onto the row it was on even when
	// the diff grew a file above it.
	cursor     int
	cursorPath string

	vp viewport.Model
	// rows is the built content, one entry per rendered line, keeping the
	// node each line belongs to — which is what lets a click land on the row
	// it points at (clickRow).
	rows []diffRow
	// headLines is how many lines render drew above the viewport (the summary
	// line), so a click's y can be turned back into a row index.
	headLines int
	built     bool
}

// diffSection is one lane's share of the change, or the remainder — the
// parent's own commits and its uncommitted work, which belong to no lane.
type diffSection struct {
	laneID      string
	childTaskID int64
	remainder   bool

	lead  []string
	files []diffFile
	// styledLead and styledBody are the coloured lines, done once per fetch.
	// Colouring is by far the most expensive part of a rebuild and it depends
	// on nothing but the line itself, so folding a file — or walking the
	// cursor down a fully expanded 5000-line diff — assembles cached strings
	// instead of re-styling every line under a held key.
	styledLead     []string
	styledBody     [][]string
	added, removed int
}

// diffNode is one foldable row of the tree: a lane, or a file inside one.
type diffNode struct {
	section int
	// file indexes the section's files; -1 for the lane's own header row.
	file int
	key  string
}

// diffRow is one rendered line: a foldable header, or a line belonging to one.
type diffRow struct {
	header bool
	// node indexes nodes; -1 for a line that belongs to no node (a section's
	// lead, the truncation notice).
	node int
	text string
}

func newDiffPane() diffPane {
	return diffPane{vp: viewport.New(), open: map[string]bool{}}
}

// open points the pane at a task, discarding another task's diff.
func (p *diffPane) openTask(taskID int64) {
	if p.taskID == taskID {
		return
	}
	p.taskID = taskID
	p.reset()
}

func (p *diffPane) reset() {
	p.lines = nil
	p.sections = nil
	p.nodes = nil
	p.grouped = false
	p.loaded = false
	p.fetching = false
	p.err = nil
	p.truncated = false
	p.built = false
	p.rows = nil
	// The folds belong to the diff, not to the pane: another task's files are
	// not these files.
	p.open = map[string]bool{}
	p.cursor, p.cursorPath = 0, ""
	p.vp.SetContent("")
	p.vp.SetYOffset(0)
}

// fetch reloads the diff. stale=false means "only if we have nothing", which
// is what opening the tab asks for; a refresh keypress passes true.
//
// The grouped form is asked for unconditionally. It costs the daemon nothing
// extra for a task with no lanes — one `git log` that comes back empty — and
// it means the pane never has to know in advance whether the task fanned out,
// which is a fact that changes while the tab is open.
func (p *diffPane) fetch(client *apiclient.Client, force bool) tea.Cmd {
	if client == nil || p.taskID == 0 || p.fetching {
		return nil
	}
	if p.loaded && !force {
		return nil
	}
	p.fetching = true
	id := p.taskID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
		defer cancel()
		sections, err := client.DiffByLane(ctx, id)
		return diffLoadedMsg{taskID: id, sections: sections, err: err}
	}
}

func (p *diffPane) apply(msg diffLoadedMsg) {
	if msg.taskID != p.taskID {
		return
	}
	p.fetching = false
	if msg.err != nil {
		p.err = msg.err
		return
	}
	p.err = nil
	p.loaded = true
	sections := msg.sections
	if sections == nil {
		sections = []apiclient.DiffSection{{Remainder: true, Diff: msg.text}}
	}
	p.setSections(sections)
	p.keepFolds()
}

// setSections parses each section and colours it, spending one shared line
// budget across all of them in order: the cap exists to bound the terminal,
// and a per-section cap would let a fan-out of twenty lanes render twenty
// times the intended maximum.
func (p *diffPane) setSections(in []apiclient.DiffSection) {
	p.sections = make([]diffSection, 0, len(in))
	p.lines = nil
	p.truncated = false
	p.grouped = false
	budget := maxDiffLines
	for _, s := range in {
		if !s.Remainder {
			p.grouped = true
		}
		lines := splitDiff(s.Diff)
		if len(lines) > budget {
			lines = lines[:budget]
			p.truncated = true
		}
		budget -= len(lines)
		p.lines = append(p.lines, lines...)

		sec := diffSection{laneID: s.LaneID, childTaskID: s.ChildTaskID, remainder: s.Remainder}
		sec.lead, sec.files = parseDiffFiles(lines)
		sec.styledLead = colorDiffLines(sec.lead)
		sec.styledBody = make([][]string, len(sec.files))
		for i, f := range sec.files {
			sec.styledBody[i] = colorDiffLines(f.body)
			sec.added += f.added
			sec.removed += f.removed
		}
		p.sections = append(p.sections, sec)
	}
}

// sectionKey identifies a lane across refreshes. The lane id alone would not:
// a parent may fan out twice, and two fan_out steps are free to name a lane
// the same thing. The child task the lane ran as is unique.
func (p *diffPane) sectionKey(i int) string {
	s := p.sections[i]
	if s.remainder {
		return diffKeySep + "remainder"
	}
	return diffKeySep + "lane" + diffKeySep + s.laneID + diffKeySep +
		strconv.FormatInt(s.childTaskID, 10)
}

// fileKey identifies one file *within* a section. Ungrouped it is the bare
// path, which is what keeps the fold state of a task that never fanned out
// exactly what it was.
func (p *diffPane) fileKey(i, f int) string {
	path := p.sections[i].files[f].path
	if !p.grouped {
		return path
	}
	return p.sectionKey(i) + diffKeySep + path
}

// rebuildNodes recomputes the visible tree. A closed lane contributes its
// header and nothing else, so the cursor cannot walk into a fold that is shut.
func (p *diffPane) rebuildNodes() {
	nodes := make([]diffNode, 0, len(p.nodes))
	for i := range p.sections {
		if p.grouped {
			key := p.sectionKey(i)
			nodes = append(nodes, diffNode{section: i, file: -1, key: key})
			if !p.open[key] {
				continue
			}
		}
		for f := range p.sections[i].files {
			nodes = append(nodes, diffNode{section: i, file: f, key: p.fileKey(i, f)})
		}
	}
	p.nodes = nodes
	p.built = false
}

// keepFolds carries the fold state and the cursor across a refresh, by key.
// Entries for rows the new diff no longer has are dropped: a session left on
// one task for a day would otherwise accumulate the fold state of every file
// the agent touched and reverted.
func (p *diffPane) keepFolds() {
	open := make(map[string]bool, len(p.open))
	for i := range p.sections {
		if p.grouped {
			if key := p.sectionKey(i); p.open[key] {
				open[key] = true
			}
		}
		for f := range p.sections[i].files {
			if key := p.fileKey(i, f); p.open[key] {
				open[key] = true
			}
		}
	}
	p.open = open
	p.rebuildNodes()
	p.cursor = 0
	for i, n := range p.nodes {
		if n.key == p.cursorPath {
			p.cursor = i
			break
		}
	}
	p.syncCursorPath()
}

func (p *diffPane) syncCursorPath() {
	if len(p.nodes) == 0 {
		p.cursor, p.cursorPath = 0, ""
		return
	}
	p.cursor = min(max(p.cursor, 0), len(p.nodes)-1)
	p.cursorPath = p.nodes[p.cursor].key
}

func splitDiff(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

// updateKey is the pane's own keyboard: the fold tree is what ↑/↓ walk, and
// the fold keys act on the row under the cursor — a lane or a file, whichever
// it is on. Line-level scrolling stays on the viewport's pager keys (pgup/pgdn,
// u/f, b) and the wheel — with everything collapsed there is usually nothing to
// scroll, and a reader who has opened a long file wants a page at a time anyway.
func (p *diffPane) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "down", "j":
		p.moveCursor(1)
		return nil
	case "up", "k":
		p.moveCursor(-1)
		return nil
	case "enter", " ", "space":
		p.toggle()
		return nil
	case "right", "l":
		p.setFold(true)
		return nil
	case "left", "h":
		p.setFold(false)
		return nil
	case "O":
		p.foldAll(true)
		return nil
	case "C":
		p.foldAll(false)
		return nil
	}
	var cmd tea.Cmd
	p.vp, cmd = p.vp.Update(msg)
	return cmd
}

func (p *diffPane) moveCursor(delta int) {
	if len(p.nodes) == 0 {
		return
	}
	p.cursor = min(max(p.cursor+delta, 0), len(p.nodes)-1)
	p.syncCursorPath()
	p.built = false
}

func (p *diffPane) toggle() {
	if len(p.nodes) == 0 {
		return
	}
	p.setFold(!p.open[p.nodes[p.cursor].key])
}

// setFold opens or closes the row under the cursor. Closing a lane hides its
// files, so the cursor is re-pinned by key afterwards rather than by index —
// otherwise closing a lane low in the list would leave the cursor pointing
// past the end.
func (p *diffPane) setFold(want bool) {
	if len(p.nodes) == 0 {
		return
	}
	key := p.nodes[p.cursor].key
	if p.open[key] == want {
		return
	}
	if want {
		p.open[key] = true
	} else {
		delete(p.open, key)
	}
	p.rebuildNodes()
	p.pinCursor(key)
}

// pinCursor puts the cursor back on the row with the given key, which is where
// it was before the tree changed shape under it.
func (p *diffPane) pinCursor(key string) {
	for i, n := range p.nodes {
		if n.key == key {
			p.cursor = i
			break
		}
	}
	p.syncCursorPath()
}

// foldAll is the expand-all / collapse-all pair — every level of the tree, so
// `O` on a fan-out opens the lanes *and* the files under them rather than
// stopping at the lanes and needing a second press per lane. Collapsing empties
// the map rather than writing false into it, so "closed" has one representation.
func (p *diffPane) foldAll(want bool) {
	key := ""
	if len(p.nodes) > 0 {
		key = p.nodes[p.cursor].key
	}
	if !want {
		if len(p.open) == 0 {
			return
		}
		p.open = map[string]bool{}
		p.rebuildNodes()
		p.pinCursor(key)
		return
	}
	changed := false
	for i := range p.sections {
		if p.grouped {
			if k := p.sectionKey(i); !p.open[k] {
				p.open[k] = true
				changed = true
			}
		}
		for f := range p.sections[i].files {
			if k := p.fileKey(i, f); !p.open[k] {
				p.open[k] = true
				changed = true
			}
		}
	}
	if !changed {
		return
	}
	p.rebuildNodes()
	p.pinCursor(key)
}

// scroll moves the pane by lines — the mouse wheel's path.
func (p *diffPane) scroll(delta int) {
	if delta > 0 {
		p.vp.ScrollDown(1)
		return
	}
	p.vp.ScrollUp(1)
}

// clickRow is a click inside the pane's body (§15: click a row). row is
// 0-based from the top of the pane, summary line included. Clicking a header
// folds it; clicking a line of a file moves the cursor onto that file without
// folding anything, because a click on a line of code is not a request to make
// it disappear.
func (p *diffPane) clickRow(row int) {
	i := row - p.headLines + p.vp.YOffset()
	if i < 0 || i >= len(p.rows) {
		return
	}
	r := p.rows[i]
	if r.node < 0 || r.node >= len(p.nodes) {
		return
	}
	p.cursor = r.node
	p.syncCursorPath()
	p.built = false
	if r.header {
		p.toggle()
	}
}

// render draws the diff, or the reason there is none. The endpoint's two
// conflicts are different situations: a task that has not started has no
// worktree yet, while an archived one had its worktree removed on purpose —
// and only the first is worth waiting on.
func (p *diffPane) render(width, height int) string {
	if body, ok := p.emptyState(); ok {
		return styleDim.Render("  " + body)
	}
	head, rest := "", max(height, 1)
	// The summary is pinned above the viewport rather than scrolled with it:
	// the totals are what a reader checks a diff's size against, and they are
	// worth a line only while they are still on screen. A pane too short to
	// hold both keeps the diff.
	if len(p.nodes) > 0 && rest > 2 {
		head = p.summary() + "\n"
		rest--
	}
	p.headLines = len(strings.Split(head, "\n")) - 1
	p.vp.SetWidth(max(width, 1))
	p.vp.SetHeight(rest)
	if !p.built {
		p.rows = p.buildRows()
		lines := make([]string, len(p.rows))
		for i, r := range p.rows {
			lines[i] = r.text
		}
		p.vp.SetContent(strings.Join(lines, "\n"))
		p.built = true
		p.revealCursor()
	}
	return head + p.vp.View()
}

// revealCursor scrolls the selected row's header into view. It runs on every
// rebuild, which is also every cursor move and every fold: expanding a file
// near the bottom of a long list is pointless if what you opened is off
// screen.
func (p *diffPane) revealCursor() {
	for i, r := range p.rows {
		if r.header && r.node == p.cursor {
			p.vp.EnsureVisible(i, 0, 0)
			return
		}
	}
}

func (p *diffPane) emptyState() (string, bool) {
	switch {
	case p.err != nil:
		return p.errorText(), true
	case p.fetching && len(p.lines) == 0:
		return "loading diff…", true
	case !p.loaded:
		return "press d to load the diff", true
	case len(p.lines) == 0:
		return "no changes against the base branch yet", true
	default:
		return "", false
	}
}

// errorText explains a refusal in the terms of the task's own life cycle.
func (p *diffPane) errorText() string {
	var apiErr *apiclient.Error
	if errors.As(p.err, &apiErr) && apiErr.Status == http.StatusConflict {
		switch {
		case strings.Contains(apiErr.Message, "no worktree"):
			return "no worktree yet — this task has not started running"
		case strings.Contains(apiErr.Message, "no longer exists"):
			return "the worktree was removed (archived) — the branch still holds the commits"
		default:
			return "diff unavailable: " + apiErr.Message
		}
	}
	return "diff failed: " + errString(p.err)
}

// fileCount is how many file rows the tree holds. On a fan-out it counts a
// file once per lane that touched it, which is what the rows themselves do.
func (p *diffPane) fileCount() int {
	n := 0
	for i := range p.sections {
		n += len(p.sections[i].files)
	}
	return n
}

// laneCount is how many of the sections are lanes; the remainder is not one.
func (p *diffPane) laneCount() int {
	n := 0
	for i := range p.sections {
		if !p.sections[i].remainder {
			n++
		}
	}
	return n
}

// summary is the pane's first line: how many lanes and files, and the totals
// across all of them. With everything folded shut, this is the only place the
// size of the change is stated.
func (p *diffPane) summary() string {
	var added, removed int
	files := p.fileCount()
	for i := range p.sections {
		added += p.sections[i].added
		removed += p.sections[i].removed
	}
	out := "  "
	if p.grouped {
		out += styleDim.Render(countNoun(p.laneCount(), "lane")) + "  "
	}
	out += styleDim.Render(countNoun(files, "file"))
	if added+removed == 0 {
		// A change of only renames, modes or binaries has no ± lines at all,
		// and `+0 -0` above a list of files would read as a rendering fault.
		return out
	}
	return out + "  " + diffCounts(added, removed)
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// buildRows renders the pane's lines: each section's header (when the lanes
// render at all), then whatever preceded its first file, then each file's
// header with its body under it when open, then the truncation notice.
func (p *diffPane) buildRows() []diffRow {
	rows := make([]diffRow, 0, len(p.lines)+len(p.nodes)+1)
	if !p.grouped {
		// Ungrouped there is no header to hang it under, so whatever preceded
		// the first file opens the pane — including for a diff that has no
		// file section at all, which is the case the lead exists for.
		for i := range p.sections {
			for _, line := range p.sections[i].styledLead {
				rows = append(rows, diffRow{node: -1, text: line})
			}
		}
	}
	for i, n := range p.nodes {
		if n.file < 0 {
			rows = append(rows, diffRow{header: true, node: i, text: p.sectionRow(i)})
			// A section's lead belongs to the section, not to any file in it,
			// so it hangs directly under the header once the lane is open.
			if p.open[n.key] {
				for _, line := range p.sections[n.section].styledLead {
					rows = append(rows, diffRow{node: -1, text: line})
				}
			}
			continue
		}
		rows = append(rows, diffRow{header: true, node: i, text: p.fileRow(i)})
		if !p.open[n.key] {
			continue
		}
		for _, line := range p.sections[n.section].styledBody[n.file] {
			rows = append(rows, diffRow{node: i, text: line})
		}
	}
	if p.truncated {
		// The cap cuts the stream, so the last file here is a partial section
		// and any file past it is missing outright — which is what the notice
		// has always been for.
		rows = append(rows, diffRow{
			node: -1,
			text: styleDim.Render("  … diff truncated; the whole change is on the branch"),
		})
	}
	return rows
}

// sectionRow is one lane's line in the outer list. The lane id is what the
// workflow calls it; the child task id is how a reader gets to the lane's own
// transcript, and is the only handle on it that is unambiguous.
func (p *diffPane) sectionRow(i int) string {
	n := p.nodes[i]
	s := p.sections[n.section]
	glyph := diffFoldClosed
	if p.open[n.key] {
		glyph = diffFoldOpen
	}
	label := "the task's own commits"
	if !s.remainder {
		label = "lane " + s.laneID
	}
	style := styleDiffFile
	if i == p.cursor {
		style = styleSelected
	}
	out := style.Render(glyph + " " + label)
	if !s.remainder && s.childTaskID > 0 {
		out += "  " + styleDim.Render("task "+strconv.FormatInt(s.childTaskID, 10))
	}
	if len(s.files) == 0 {
		// A lane that merged cleanly but changed nothing, or a parent that
		// committed nothing of its own. `+0 -0` over an empty list would read
		// as a rendering fault rather than as the fact it is.
		return out + "  " + styleDim.Render("no changes")
	}
	return out + "  " + diffCounts(s.added, s.removed)
}

// fileRow is one file's line in the list: the fold glyph, the path, and what
// it did to the file. The counts live here because "which file was rewritten"
// is the question a folded list has to answer without being opened.
func (p *diffPane) fileRow(i int) string {
	n := p.nodes[i]
	f := p.sections[n.section].files[n.file]
	glyph := diffFoldClosed
	if p.open[n.key] {
		glyph = diffFoldOpen
	}
	style := styleDiffFile
	if i == p.cursor {
		style = styleSelected
	}
	indent := ""
	if p.grouped {
		indent = "  "
	}
	out := style.Render(indent + glyph + " " + f.path)
	if f.binary {
		// A binary file has no ± lines at all, and `+0 -0` would read as
		// "nothing changed here".
		return out + "  " + styleDim.Render("binary")
	}
	return out + "  " + diffCounts(f.added, f.removed)
}

func diffCounts(added, removed int) string {
	return styleDiffAdd.Render("+"+strconv.Itoa(added)) + " " +
		styleDiffDel.Render("-"+strconv.Itoa(removed))
}

// diffClass is what one line of a unified diff is.
type diffClass int

const (
	diffContext diffClass = iota
	// diffHeader is a line of a file's own header — the `diff --git`, the blob
	// ids, a mode change, the ± path markers.
	diffHeader
	diffHunk
	diffAdd
	diffDel
)

// classifyDiffLine reads the structure. The ± file markers are checked before
// the ± content prefixes: they carry the same first character, and colouring
// them as changes makes every file header read as one added and one removed
// line.
func classifyDiffLine(line string) diffClass {
	switch {
	case strings.HasPrefix(line, "diff --git"), strings.HasPrefix(line, "index "),
		strings.HasPrefix(line, "new file"), strings.HasPrefix(line, "deleted file"),
		strings.HasPrefix(line, "similarity index"), strings.HasPrefix(line, "rename "),
		strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		return diffHeader
	case strings.HasPrefix(line, "@@"):
		return diffHunk
	case strings.HasPrefix(line, "+"):
		return diffAdd
	case strings.HasPrefix(line, "-"):
		return diffDel
	default:
		return diffContext
	}
}

func colorDiffLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = colorDiffLine(line)
	}
	return out
}

// colorDiffLine colours the structure of one line — hunks, additions,
// removals. Per-language highlighting inside a diff would mean stripping the
// gutter and detecting a lexer per file, which is a different feature.
func colorDiffLine(line string) string {
	switch classifyDiffLine(line) {
	case diffHeader:
		return styleDiffFile.Render(line)
	case diffHunk:
		return styleDiffHunk.Render(line)
	case diffAdd:
		return styleDiffAdd.Render(line)
	case diffDel:
		return styleDiffDel.Render(line)
	case diffContext:
		return line
	}
	return line
}
