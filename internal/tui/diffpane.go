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

// diffLoadedMsg carries a fetched diff.
type diffLoadedMsg struct {
	taskID int64
	text   string
	err    error
}

// diffPane is the §15 diff tab: the worktree against merge-base with the base
// branch. It fetches when the tab opens and on an explicit refresh, never
// from the event stream — the endpoint runs git per call, so following events
// would mean a subprocess per event.
//
// The diff is grouped by file and folded shut (task 012, diffgroup.go): the
// pane opens on the list of files with their ± counts, and the reader expands
// the ones worth reading.
type diffPane struct {
	taskID    int64
	lines     []string
	lead      []string
	files     []diffFile
	loaded    bool
	fetching  bool
	err       error
	truncated bool

	// open is the fold state, keyed by the path the header row shows rather
	// than by position: a refresh re-parses the diff, and a file that grew a
	// hunk in between must not fold shut under the reader. A missing entry is
	// closed, which is what makes "everything starts collapsed" the default
	// rather than a state to initialise.
	open map[string]bool
	// cursor is the file the fold keys act on. cursorPath is the same thing by
	// name, so a refresh restores the cursor onto the file it was on even when
	// the diff grew a file above it.
	cursor     int
	cursorPath string

	// styledLead and styledBody are the coloured lines, done once per fetch.
	// Colouring is by far the most expensive part of a rebuild and it depends
	// on nothing but the line itself, so folding a file — or walking the cursor
	// down a fully expanded 5000-line diff — assembles cached strings instead
	// of re-styling every line under a held key.
	styledLead []string
	styledBody [][]string

	vp viewport.Model
	// rows is the built content, one entry per rendered line, keeping the
	// file each line belongs to — which is what lets a click land on the file
	// it points at (clickRow).
	rows []diffRow
	// headLines is how many lines render drew above the viewport (the summary
	// line), so a click's y can be turned back into a row index.
	headLines int
	built     bool
}

// diffRow is one rendered line: a file's header, or a line belonging to one.
type diffRow struct {
	header bool
	// file indexes files; -1 for a line that belongs to no file (the lead, the
	// truncation notice).
	file int
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
	p.lead = nil
	p.files = nil
	p.styledLead = nil
	p.styledBody = nil
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
		text, err := client.Diff(ctx, id)
		return diffLoadedMsg{taskID: id, text: text, err: err}
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
	p.lines = splitDiff(msg.text)
	p.truncated = len(p.lines) > maxDiffLines
	if p.truncated {
		p.lines = p.lines[:maxDiffLines]
	}
	p.lead, p.files = parseDiffFiles(p.lines)
	p.styledLead = colorDiffLines(p.lead)
	p.styledBody = make([][]string, len(p.files))
	for i, f := range p.files {
		p.styledBody[i] = colorDiffLines(f.body)
	}
	p.keepFolds()
	p.built = false
}

// keepFolds carries the fold state and the cursor across a refresh, by path.
// Entries for files the new diff no longer has are dropped: a session left on
// one task for a day would otherwise accumulate the fold state of every file
// the agent touched and reverted.
func (p *diffPane) keepFolds() {
	open := make(map[string]bool, len(p.files))
	p.cursor = 0
	for i, f := range p.files {
		if p.open[f.path] {
			open[f.path] = true
		}
		if f.path == p.cursorPath {
			p.cursor = i
		}
	}
	p.open = open
	p.syncCursorPath()
}

func (p *diffPane) syncCursorPath() {
	if len(p.files) == 0 {
		p.cursor, p.cursorPath = 0, ""
		return
	}
	p.cursor = min(max(p.cursor, 0), len(p.files)-1)
	p.cursorPath = p.files[p.cursor].path
}

func splitDiff(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

// updateKey is the pane's own keyboard: the file list is what ↑/↓ walk, and
// the fold keys act on the file under the cursor. Line-level scrolling stays
// on the viewport's pager keys (pgup/pgdn, u/f, b) and the wheel — with every
// file collapsed there is usually nothing to scroll, and a reader who has
// opened a long file wants a page at a time anyway.
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
	if len(p.files) == 0 {
		return
	}
	p.cursor = min(max(p.cursor+delta, 0), len(p.files)-1)
	p.syncCursorPath()
	p.built = false
}

func (p *diffPane) toggle() {
	if len(p.files) == 0 {
		return
	}
	p.setFold(!p.open[p.files[p.cursor].path])
}

func (p *diffPane) setFold(want bool) {
	if len(p.files) == 0 {
		return
	}
	path := p.files[p.cursor].path
	if p.open[path] == want {
		return
	}
	if want {
		p.open[path] = true
	} else {
		delete(p.open, path)
	}
	p.built = false
}

// foldAll is the expand-all / collapse-all pair. Collapsing empties the map
// rather than writing false into it, so "closed" has one representation.
func (p *diffPane) foldAll(want bool) {
	if !want {
		if len(p.open) == 0 {
			return
		}
		p.open = map[string]bool{}
		p.built = false
		return
	}
	for _, f := range p.files {
		if !p.open[f.path] {
			p.open[f.path] = true
			p.built = false
		}
	}
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
	if r.file < 0 {
		return
	}
	p.cursor = r.file
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
	if len(p.files) > 0 && rest > 2 {
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

// revealCursor scrolls the selected file's header into view. It runs on every
// rebuild, which is also every cursor move and every fold: expanding a file
// near the bottom of a long list is pointless if what you opened is off
// screen.
func (p *diffPane) revealCursor() {
	for i, r := range p.rows {
		if r.header && r.file == p.cursor {
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

// summary is the pane's first line: how many files, and the totals across all
// of them. With every file folded shut, this is the only place the size of the
// change is stated.
func (p *diffPane) summary() string {
	var added, removed int
	for _, f := range p.files {
		added += f.added
		removed += f.removed
	}
	noun := " files"
	if len(p.files) == 1 {
		noun = " file"
	}
	out := "  " + styleDim.Render(strconv.Itoa(len(p.files))+noun)
	if added+removed == 0 {
		// A change of only renames, modes or binaries has no ± lines at all,
		// and `+0 -0` above a list of files would read as a rendering fault.
		return out
	}
	return out + "  " + diffCounts(added, removed)
}

// buildRows renders the pane's lines: whatever preceded the first file, then
// each file's header with its body under it when open, then the truncation
// notice.
func (p *diffPane) buildRows() []diffRow {
	rows := make([]diffRow, 0, len(p.lines)+len(p.files)+1)
	for _, line := range p.styledLead {
		rows = append(rows, diffRow{file: -1, text: line})
	}
	for i, f := range p.files {
		rows = append(rows, diffRow{header: true, file: i, text: p.headerRow(i)})
		if !p.open[f.path] {
			continue
		}
		for _, line := range p.styledBody[i] {
			rows = append(rows, diffRow{file: i, text: line})
		}
	}
	if p.truncated {
		// The cap cuts the stream, so the last file here is a partial section
		// and any file past it is missing outright — which is what the notice
		// has always been for.
		rows = append(rows, diffRow{
			file: -1,
			text: styleDim.Render("  … diff truncated; the whole change is on the branch"),
		})
	}
	return rows
}

// headerRow is one file's line in the list: the fold glyph, the path, and what
// it did to the file. The counts live here because "which file was rewritten"
// is the question a folded list has to answer without being opened.
func (p *diffPane) headerRow(i int) string {
	f := p.files[i]
	glyph := diffFoldClosed
	if p.open[f.path] {
		glyph = diffFoldOpen
	}
	style := styleDiffFile
	if i == p.cursor {
		style = styleSelected
	}
	out := style.Render(glyph + " " + f.path)
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
