package tui

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The issue detail (§15 view 13, task 130.9): one issue in full — its
// state, its body as Markdown, its labels, kind, priority and author, the
// tasks it started, and, for an imported issue, where it came from.
//
// The body is the one place outside assistant prose that markdown.go renders
// (task 130 decision 12, widening task 073 decision 5): it is prose a human
// wrote as Markdown. It shares the session's rendered/raw choice (task 076
// decision 2) and the link picker (task 112) with the panes that already
// render Markdown, rather than growing a second toggle.
//
// The linked tasks are every root task the issue started, newest first,
// finished and archived ones included, from GET /v1/tasks?issue_id= (task 130
// decision 16.3, widened by 130.13 from 130.9's active ids). `a` starts
// another from here (decision 19.1).
//
// Task 134.16 adds the issue's worktree roles: a `lane` fact, a Main
// worktree section naming the branch and its occupant (which `enter` opens),
// each linked task's role, and a merge-back folded under the side task it
// merges. All of it is read from the issue DTO and the task rows already
// fetched — merge_source_task_id names a merge-back's side task, never the
// workflow name or the title.
//
// The thread sits under the description, oldest first, each comment's body
// through the same renderer the description uses (task 130 decision 24,
// 130.16). `W` writes a local comment in $EDITOR; an issue whose GitHub remote
// is live takes none, by the rule that refuses its title and body (spec §5.6),
// so the key is withheld there rather than offered to fail.

// Issue-detail messages.
type (
	issueRefreshMsg struct{ id int64 }
	issueLoadedMsg  struct {
		id    int64
		issue apiclient.Issue
		// tasks are every root task the issue started, newest first. A
		// listing that failed leaves them out rather than failing the screen:
		// the issue itself is still worth showing.
		tasks []apiclient.Task
		// comments is the thread, oldest first. A read that failed is
		// commentsErr, shown in the thread's place for the same reason.
		comments    []apiclient.IssueComment
		commentsErr error
		err         error
	}
	// issueCommentEditedMsg is $EDITOR closing on a comment draft.
	issueCommentEditedMsg struct {
		id   int64
		text string
		err  error
	}
	// issueCommentedMsg is a finished AddIssueComment.
	issueCommentedMsg struct {
		id  int64
		err error
	}
)

// issueView is §15's view 13.
type issueView struct {
	// projectScope is the root's selected project (task 132.2), stored for
	// the item that scopes this view to it.
	projectScope

	client *apiclient.Client

	id      int64
	issue   apiclient.Issue
	tasks   []apiclient.Task
	loaded  bool
	loadErr error

	// comments and commentsErr are the thread as last read; posting is a
	// comment on its way to the daemon.
	comments    []apiclient.IssueComment
	commentsErr error
	posting     bool

	// cursor is the selected row of selectRows — the Main worktree section,
	// when the issue has a main branch, then the linked tasks in display
	// order; scroll is the page's first line.
	cursor int
	scroll int
	// The page is scroll-first (#792): the arrows scroll it, and move the
	// selection only among rows on screen, so selecting never jumps the page
	// away from the description. rowLines is each selectRows row's page
	// line, view the page lines last shown and viewTop the screen line the
	// first of them is on (1 under a ▲ cue) — what the last render drew,
	// which is what a key or a click acts on.
	rowLines []int
	view     int
	viewTop  int

	raw   *rawHolder
	links *hyperlinkHolder
	md    mdCache

	note        string
	noteBad     bool
	refreshWait bool

	// w is the form and the close/reopen/delete prompt (task 130.12).
	w issueWrites

	// back is where esc lands: the issue list, or the task workspace the
	// issue was opened from (task 130.13). Zero is the list.
	back viewID
}

func newIssueView(raw *rawHolder, links *hyperlinkHolder) *issueView {
	if raw == nil {
		raw = newRawHolder()
	}
	if links == nil {
		links = newHyperlinkHolder()
	}
	return &issueView{raw: raw, links: links, w: newIssueWrites()}
}

func (v *issueView) title() string {
	if v.id == 0 {
		return "Issue"
	}
	return "Issue #" + strconv.FormatInt(v.id, 10)
}

func (v *issueView) setClient(c *apiclient.Client) tea.Cmd {
	v.client = c
	return v.loadCmd()
}

// capturesInput holds the global keys back while the form or the prompt is
// up, for the list's reason.
func (v *issueView) capturesInput() bool { return v.w.open() }

func (v *issueView) bindingContext() bindingContext { return v.w.context(ctxIssue) }

func (v *issueView) paste(text string) tea.Cmd { return v.w.paste(text) }

// open points the screen at an issue. Anything shown belonged to another one,
// so it is dropped rather than flashed under the new title.
func (v *issueView) open(id int64) tea.Cmd {
	if id != v.id {
		v.issue, v.tasks, v.loaded, v.loadErr = apiclient.Issue{}, nil, false, nil
		v.comments, v.commentsErr, v.posting = nil, nil, false
		// A re-read still pending is the other issue's: left armed, it would
		// swallow this one's events — a comment landing in the debounce
		// window would never show.
		v.refreshWait = false
		v.cursor, v.scroll = 0, 0
		v.w.form, v.w.act = nil, nil
	}
	v.id = id
	v.note = ""
	return v.loadCmd()
}

func (v *issueView) update(msg tea.Msg) (panel, tea.Cmd) {
	if cmd, ok := v.w.update(msg); ok {
		return v, cmd
	}
	switch msg := msg.(type) {
	case issueFormClosedMsg:
		v.w.form = nil
		switch {
		case msg.saved == nil:
			return v, nil
		case msg.created:
			id, pid := msg.saved.ID, msg.saved.ProjectID
			return v, func() tea.Msg { return openIssueMsg{id: id, projectID: pid} }
		}
		if msg.saved.ID == v.id {
			v.issue = *msg.saved
		}
		v.setNote("saved issue #"+strconv.FormatInt(msg.saved.ID, 10), false)
		return v, v.loadCmd()
	case issueActedMsg:
		v.w.act = nil
		if msg.err == nil && msg.action == issueActDelete {
			// The issue is gone; its screen has nothing left to show.
			return v, func() tea.Msg { return selectViewMsg{id: viewIssues} }
		}
		v.setNote(actedNote(msg), msg.err != nil)
		if msg.err == nil && msg.id == v.id {
			v.issue = msg.issue
		}
		return v, v.loadCmd()
	case viewActivatedMsg:
		if msg.id == viewIssue {
			// An inactive view receives no tick, so one that fired while the
			// screen was away is lost; this read covers it, and the debounce
			// re-arms rather than staying shut on every later event.
			v.refreshWait = false
			return v, v.loadCmd()
		}
		return v, nil
	case issueRefreshMsg:
		v.refreshWait = false
		if msg.id != v.id {
			return v, nil
		}
		return v, v.loadCmd()
	case issueLoadedMsg:
		v.applyLoaded(msg)
		return v, nil
	case issueCommentEditedMsg:
		return v, v.postComment(msg)
	case issueCommentedMsg:
		if msg.id != v.id {
			return v, nil
		}
		v.posting = false
		if msg.err != nil {
			// A 409 here is the remote going live between the read that
			// offered the key and the post (decision 24's mirrored rule).
			v.setNote("comment on issue #"+strconv.FormatInt(msg.id, 10)+": "+errString(msg.err), true)
			return v, v.loadCmd()
		}
		v.setNote("commented on issue #"+strconv.FormatInt(msg.id, 10), false)
		return v, v.loadCmd()
	case openedURLMsg:
		if msg.err != nil {
			v.setNote(openFailure(msg), true)
		} else {
			v.note = ""
		}
		return v, nil
	case linkOpenedMsg:
		v.setNote(msg.notice())
		return v, nil
	case noteMsg:
		return v, v.updateNote(msg.note)
	case tea.KeyPressMsg:
		return v.updateKey(msg)
	case tea.MouseWheelMsg:
		v.updateWheel(msg)
		return v, nil
	case tea.MouseClickMsg:
		v.updateClick(msg)
		return v, nil
	}
	return v, nil
}

func (v *issueView) loadCmd() tea.Cmd {
	client, id := v.client, v.id
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		iss, err := client.GetIssue(ctx, id, "")
		if err != nil {
			return issueLoadedMsg{id: id, err: err}
		}
		tasks, err := client.ListTasks(ctx, apiclient.ListTasksOptions{
			IssueID: id, Archived: apiclient.ArchivedAll,
		})
		if err != nil {
			tasks = nil
		}
		comments, commentsErr := client.ListIssueComments(ctx, id)
		return issueLoadedMsg{id: id, issue: iss, tasks: tasks, comments: comments, commentsErr: commentsErr}
	}
}

func (v *issueView) applyLoaded(msg issueLoadedMsg) {
	if msg.id != v.id {
		return // an answer for the issue the screen was on before
	}
	if msg.err != nil {
		v.loadErr = msg.err
		return
	}
	v.issue, v.tasks, v.loaded, v.loadErr = msg.issue, msg.tasks, true, nil
	v.comments, v.commentsErr = msg.comments, msg.commentsErr
	v.cursor = min(v.cursor, max(len(v.selectRows())-1, 0))
}

func (v *issueView) scheduleRefresh() tea.Cmd {
	if v.refreshWait || v.client == nil || v.id == 0 {
		return nil
	}
	v.refreshWait = true
	id := v.id
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return issueRefreshMsg{id: id} })
}

// updateNote re-reads on this issue's own events, on events of the tasks
// it lists, and on task events naming this issue. Another issue's events leave it alone. The thread's events,
// issue.comment_added and issue.comment_updated, name the issue in `id` like
// every issue.* event, so they re-read the thread with the rest.
func (v *issueView) updateNote(n apiclient.Note) tea.Cmd {
	ev, ok := n.(apiclient.EventNote)
	if !ok || v.id == 0 {
		return nil
	}
	switch {
	case isIssueEvent(ev.Event.Type):
		if issueEventID(ev.Event) == v.id {
			return v.scheduleRefresh()
		}
	case isTaskEvent(ev.Event.Type):
		// A task of this issue's that the screen does not list yet — a new
		// side task, or a merge-back a side task's `→ done` inserted — names
		// the issue in its payload (task 134.6), not in the tasks shown.
		if ev.Event.TaskID != nil && v.linksTask(*ev.Event.TaskID) || taskEventIssueID(ev.Event) == v.id {
			return v.scheduleRefresh()
		}
	}
	return nil
}

func (v *issueView) linksTask(id int64) bool {
	if slices.Contains(v.issue.Tasks.ActiveIDs, id) {
		return true
	}
	for _, t := range v.tasks {
		if t.ID == id {
			return true
		}
	}
	return false
}

func (v *issueView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		v.step(-1)
		return v, nil
	case "down", "j":
		v.step(1)
		return v, nil
	case "pgup":
		v.scrollBy(-v.page())
		return v, nil
	case "pgdown", "space":
		v.scrollBy(v.page())
		return v, nil
	case "ctrl+u":
		v.scrollBy(-max(v.page()/2, 1))
		return v, nil
	case "ctrl+d":
		v.scrollBy(max(v.page()/2, 1))
		return v, nil
	case "esc":
		if v.note != "" {
			v.note = ""
			return v, nil
		}
		// Back to where the issue was opened from: the list, which kept its
		// own selection, or the task workspace.
		back := v.back
		if back == 0 {
			back = viewIssues
		}
		return v, func() tea.Msg { return selectViewMsg{id: back} }
	case opKey(keymap.Refresh):
		return v, v.loadCmd()
	case opKey(keymap.OpenRow):
		return v, v.openTask()
	case opKey(keymap.Browser):
		return v, v.openSource()
	case rawToggleKey:
		v.raw.toggle()
		return v, nil
	case linkPickKey:
		items := linkItemsFrom("description", copyDoc{text: v.issue.Body})
		return v, func() tea.Msg { return openLinkPickerMsg{items: items} }
	case opKey(keymap.New):
		return v, v.w.openForm(v.client, nil, v.issue.ProjectID)
	// Last, so an operation rebound to g or G keeps it (task 118).
	case "home", "g":
		v.scroll = 0
		return v, nil
	case "end", "G":
		v.scroll = math.MaxInt // render clamps it to the last page
		return v, nil
	}
	if !v.loaded {
		return v, nil
	}
	switch msg.String() {
	case opKey(keymap.Add):
		return v, newTaskFromIssueCmd(v.issue)
	case issueEditKey:
		iss := v.issue
		return v, v.w.openForm(v.client, &iss, iss.ProjectID)
	case issueStateKey:
		act, cmd, note := newIssueStateAction(v.client, v.issue)
		if note != "" {
			v.setNote(note, true)
		}
		v.w.act = act
		return v, cmd
	case opKey(keymap.Delete):
		v.w.act = newIssueDelete(v.client, v.issue)
	case opKey(keymap.Comment):
		return v, v.openComment()
	}
	return v, nil
}

// visible reports whether row i of selectRows was on screen at the last
// render.
func (v *issueView) visible(i int) bool {
	if i < 0 || i >= len(v.rowLines) {
		return false
	}
	at := v.rowLines[i]
	return at >= v.scroll && at < v.scroll+v.view
}

// step is ↑/↓ (#792): the selection moves to the next row on screen, onto
// the first one shown once the selected row has scrolled away, and the page
// scrolls a line when no row on screen is left to move to.
func (v *issueView) step(delta int) {
	if v.visible(v.cursor) {
		if v.visible(v.cursor + delta) {
			v.cursor += delta
			return
		}
	} else {
		shown := -1
		for i := range v.rowLines {
			if v.visible(i) && (shown < 0 || delta < 0) {
				shown = i // the topmost going down, the bottom one going up
			}
		}
		if shown >= 0 {
			v.cursor = shown
			return
		}
	}
	v.scrollBy(delta)
}

// scrollBy moves the page; render clamps it to the content.
func (v *issueView) scrollBy(n int) {
	v.scroll = max(min(v.scroll, math.MaxInt-max(n, 0))+n, 0)
}

// page is a page's worth of lines: the pane as last rendered.
func (v *issueView) page() int { return max(v.view, 1) }

// updateWheel is one line per notch (#300's convention for a page that
// scrolls). The form and the prompt own the wheel as they own the keyboard
// (task 078 decision 3): the prompt names this issue.
func (v *issueView) updateWheel(msg tea.MouseWheelMsg) {
	if v.w.open() {
		return
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		v.scrollBy(-1)
	case tea.MouseWheelDown:
		v.scrollBy(1)
	}
}

// updateClick selects the Main worktree or linked-task row under the
// pointer (§15 Mouse); a click anywhere else does nothing.
func (v *issueView) updateClick(msg tea.MouseClickMsg) {
	if v.w.open() || msg.Button != tea.MouseLeft {
		return
	}
	at := v.scroll + msg.Y - v.viewTop
	if at < v.scroll || at >= v.scroll+v.view {
		return
	}
	for i, line := range v.rowLines {
		if line == at {
			v.cursor = i
			return
		}
	}
}

// commentable reports whether the issue takes a local comment, as the daemon
// says (task 130 decision 24.3): a live GitHub remote refuses one, a local
// issue or a moved or missing remote takes one. It is not whether the body
// is editable — a lost remote keeps the body sync's but gives the thread back.
func (v *issueView) commentable() bool {
	return v.loaded && v.issue.Commentable
}

// liveBindings withholds `W` where the daemon would refuse the comment,
// `enter` where there is no row to open and `o` where there is no page.
func (v *issueView) liveBindings(rows []binding) []binding {
	withheld := map[keymap.Op]bool{
		keymap.Comment: !v.commentable(),
		keymap.OpenRow: len(v.selectRows()) == 0,
		keymap.Browser: issueURL(v.issue) == "",
	}
	out := make([]binding, 0, len(rows))
	for _, b := range rows {
		if b.context != ctxIssue || b.op == "" || !withheld[b.op] {
			out = append(out, b)
		}
	}
	return out
}

// openComment hands an empty draft to $EDITOR, the helper the issue form's
// description uses (130.12).
func (v *issueView) openComment() tea.Cmd {
	if !v.commentable() {
		v.setNote("this issue is mirrored from GitHub — comment there; vincent never posts to GitHub", true)
		return nil
	}
	if v.posting {
		return nil
	}
	id := v.id
	cmd, err := editTextCmd(v.w.exec, "issue"+strconv.FormatInt(id, 10)+"-comment", ".md", "",
		func(text string, err error) tea.Msg { return issueCommentEditedMsg{id: id, text: text, err: err} })
	if err != nil {
		v.setNote("comment: "+errString(err), true)
		return nil
	}
	return cmd
}

// postComment sends a saved draft. An empty one is the way out of $EDITOR
// without posting, the way an empty commit message aborts a commit.
func (v *issueView) postComment(msg issueCommentEditedMsg) tea.Cmd {
	if msg.id != v.id {
		return nil
	}
	if msg.err != nil {
		v.setNote("comment: "+errString(msg.err), true)
		return nil
	}
	body := strings.TrimSpace(msg.text)
	if body == "" {
		v.setNote("empty comment — nothing posted", false)
		return nil
	}
	if v.client == nil {
		v.setNote("not connected", true)
		return nil
	}
	client, id := v.client, msg.id
	v.posting = true
	v.setNote("posting the comment…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		_, err := client.AddIssueComment(ctx, id, body)
		return issueCommentedMsg{id: id, err: err}
	}
}

// newTaskFromIssueCmd is `a` on either issue screen: the new-task form,
// seeded with the issue (task 130 decision 19.1). A closed issue is allowed —
// the form says it is closed and the create call warns.
func newTaskFromIssueCmd(iss apiclient.Issue) tea.Cmd {
	msg := newTaskFromIssueMsg{projectID: iss.ProjectID, issueID: iss.ID}
	return func() tea.Msg { return msg }
}

// issueDetailRow is one selectable row of the detail: the Main worktree section
// (mainWorktree), or a linked task — child when it is a merge-back folded
// under its side task.
type issueDetailRow struct {
	mainWorktree bool
	task         apiclient.Task
	child        bool
}

// selectRows is what the cursor walks, in display order: the Main worktree
// section when the issue has a main branch, then the linked tasks newest
// first with each merge-back folded under the side task it merges. A
// merge-back whose source is not listed stays a top-level row.
func (v *issueView) selectRows() []issueDetailRow {
	var rows []issueDetailRow
	if v.issue.MainWorktree != nil {
		rows = append(rows, issueDetailRow{mainWorktree: true})
	}
	listed := make(map[int64]bool, len(v.tasks))
	for _, t := range v.tasks {
		listed[t.ID] = true
	}
	folded := func(t apiclient.Task) bool {
		return t.MergeSourceTaskID != nil && listed[*t.MergeSourceTaskID]
	}
	for _, t := range v.tasks {
		if folded(t) {
			continue
		}
		rows = append(rows, issueDetailRow{task: t})
		for _, c := range v.tasks {
			if folded(c) && *c.MergeSourceTaskID == t.ID {
				rows = append(rows, issueDetailRow{task: c, child: true})
			}
		}
	}
	return rows
}

// openTask opens the selected row's task — a linked task, or the Main
// worktree section's occupant — in its workspace, which returns here on esc
// rather than to the board.
func (v *issueView) openTask() tea.Cmd {
	rows := v.selectRows()
	if v.cursor < 0 || v.cursor >= len(rows) {
		return nil
	}
	row := rows[v.cursor]
	id, state, pid := row.task.ID, row.task.State, row.task.ProjectID
	if row.mainWorktree {
		mw := v.issue.MainWorktree
		if mw == nil || mw.OccupantTaskID == nil {
			v.setNote("the main worktree is free — no task to open", false)
			return nil
		}
		id, state, pid = *mw.OccupantTaskID, "", v.issue.ProjectID
		if mw.OccupantState != nil {
			state = *mw.OccupantState
		}
	}
	return func() tea.Msg {
		return selectTaskMsg{id: id, state: state, back: viewIssue, projectID: pid}
	}
}

// issueTaskRole is a linked task's role annotation (task 134.16): `main`,
// `side · merge manual|agent`, or `merge-back of #S`. A task with no role —
// one from before roles existed — has none.
func issueTaskRole(t apiclient.Task) string {
	switch {
	case t.MergeSourceTaskID != nil:
		return "merge-back of #" + strconv.FormatInt(*t.MergeSourceTaskID, 10)
	case t.IssueWorktree == nil:
		return ""
	case *t.IssueWorktree == "side":
		mode := "manual"
		if t.MergeBack != nil && t.MergeBack.OnConflict == "agent" {
			mode = "agent"
		}
		return "side · merge " + mode
	}
	return *t.IssueWorktree
}

// issueLaneLabel is the lane fact: the section label, `done` for a closed
// issue whatever the daemon sent, and the attention mark when a root task
// waits on a person.
func issueLaneLabel(iss apiclient.Issue) string {
	lane := iss.Lane
	if iss.State == "closed" {
		lane = issueLaneDone
	}
	label := strings.ReplaceAll(lane, "_", " ")
	for _, sec := range issueSections {
		if sec.lane == lane {
			label = sec.label
		}
	}
	if iss.Attention {
		label += "  " + styleWarn.Render(attentionBadge)
	}
	return label
}

func (v *issueView) openSource() tea.Cmd {
	url := issueURL(v.issue)
	if url == "" {
		v.setNote("this issue is local — there is no page to open", true)
		return nil
	}
	v.setNote("opening "+url+"…", false)
	return openURLCmd(url)
}

func (v *issueView) setNote(text string, bad bool) { v.note, v.noteBad = text, bad }

// --- rendering ---

func (v *issueView) render(width, height int) string {
	if width < 4 || height < 2 {
		return ""
	}
	if v.w.form != nil {
		return v.w.form.render(width, height)
	}
	body := v.pageLines(width)
	var footer []string
	if v.note != "" {
		style := styleDim
		if v.noteBad {
			style = styleBad
		}
		footer = []string{"", style.Render("  " + v.note)}
	}
	if v.w.act != nil {
		footer = v.w.act.lines(width)
	}
	room := max(height-len(footer), 1)
	// The page scrolls freely, and the selection never pulls it (#792). A
	// page taller than the pane gives a line to a ▲ cue while it continues
	// above and to a ▼ cue while it continues below — the picker's idiom.
	cues := len(body) > room && room >= 3
	limit := len(body) - room
	if cues {
		limit = len(body) - (room - 1) // the last page has only the ▲ cue
	}
	v.scroll = min(max(v.scroll, 0), max(limit, 0))
	v.view, v.viewTop = room, 0
	var lines []string
	if cues && v.scroll > 0 {
		lines = append(lines, styleDim.Render(fmt.Sprintf("  ▲ %d more", v.scroll)))
		v.view, v.viewTop = room-1, 1
	}
	below := 0
	if cues && v.scroll+v.view < len(body) {
		v.view--
		below = len(body) - (v.scroll + v.view)
	}
	lines = append(lines, body[v.scroll:min(v.scroll+v.view, len(body))]...)
	if below > 0 {
		lines = append(lines, styleDim.Render(fmt.Sprintf("  ▼ %d more", below)))
	}
	lines = append(lines, footer...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

// pageLines is the whole page. It records each selectRows row's line in
// rowLines, for the arrows and the click.
func (v *issueView) pageLines(width int) (lines []string) {
	v.rowLines = v.rowLines[:0]
	switch {
	case v.loadErr != nil && !v.loaded:
		return []string{
			" " + styleTitle.Render(v.title()), "",
			styleBad.Render("  ⚠ could not read the issue: " + errString(v.loadErr)),
		}
	case !v.loaded:
		return []string{" " + styleTitle.Render(v.title()), "", styleDim.Render("  reading…")}
	}
	iss := v.issue

	head := " " + issueStateStyle(iss).Render(issueStateBadge(iss)) + "  " +
		styleKey.Render("#"+strconv.FormatInt(iss.ID, 10)) + "  " + styleTitle.Render(iss.Title)
	if badge := issueSourceBadge(iss); badge != "" {
		head += "  " + styleDim.Render(badge)
	}
	lines = append(lines, head)
	if v.loadErr != nil {
		lines = append(lines, styleBad.Render("  ⚠ could not re-read the issue: "+errString(v.loadErr)))
	}
	lines = append(lines, "")

	labels := "none"
	if len(iss.Labels) > 0 {
		labels = strings.Join(iss.Labels, ", ")
	}
	kind := iss.Kind
	if kind == "" {
		kind = "none"
	}
	for _, f := range [][2]string{
		{"lane", issueLaneLabel(iss)},
		{"labels", labels},
		{"kind", kind},
		{"priority", issuePriorityLabel(iss.Priority)},
		{"author", iss.Author},
	} {
		lines = append(lines, "  "+styleDim.Render(padRight(f[0], 10))+f[1])
	}

	lines = append(lines, "", " "+styleTitle.Render("Description"))
	v.md.begin()
	if strings.TrimSpace(iss.Body) == "" {
		lines = append(lines, styleDim.Render("  no description"))
	} else {
		md, _ := v.md.lines(iss.Body, max(width-4, 10), levelNormal, v.raw.get(), v.links.get())
		for _, l := range md {
			lines = append(lines, "  "+l)
		}
	}
	lines = append(lines, v.threadLines(width)...)
	v.md.sweep()

	rows := v.selectRows()
	if mw := iss.MainWorktree; mw != nil {
		lines = append(lines, "", " "+styleTitle.Render("Main worktree"))
		lines = append(lines, "  "+styleDim.Render(padRight("branch", 10))+mw.Branch)
		marker := "  "
		if v.cursor == 0 {
			marker = styleFocus.Render("› ")
		}
		v.rowLines = append(v.rowLines, len(lines))
		occupant := styleDim.Render("free")
		if mw.OccupantTaskID != nil {
			state := ""
			if mw.OccupantState != nil {
				state = *mw.OccupantState
			}
			occupant = stateStyles[state].Render(taskStateGlyph(state)) + " " +
				styleKey.Render("#"+strconv.FormatInt(*mw.OccupantTaskID, 10)) + "  "
			for _, t := range v.tasks {
				if t.ID == *mw.OccupantTaskID {
					occupant += t.Title + "  "
				}
			}
			occupant += styleDim.Render(state)
		}
		lines = append(lines, marker+styleDim.Render(padRight("occupant", 10))+occupant)
	}

	lines = append(lines, "", " "+styleTitle.Render("Tasks")+
		styleDim.Render("  "+plural(iss.Tasks.Count, "task", "tasks")+", "+
			strconv.Itoa(len(iss.Tasks.ActiveIDs))+" active"))
	if len(v.tasks) == 0 {
		lines = append(lines, styleDim.Render("  no task yet · "+opKey(keymap.Add)+" starts one"))
	}
	for i, row := range rows {
		if row.mainWorktree {
			continue
		}
		t := row.task
		marker := "  "
		if i == v.cursor {
			marker = styleFocus.Render("› ")
		}
		v.rowLines = append(v.rowLines, len(lines))
		if row.child {
			marker += "  └ "
		}
		glyph := taskStateGlyph(t.State)
		if glyph == "" {
			glyph = "·"
		}
		state := t.State
		if t.ArchivedAt != nil {
			state += " · archived"
		}
		if role := issueTaskRole(t); role != "" {
			state = role + " · " + state
		}
		lines = append(lines, marker+stateStyles[t.State].Render(glyph)+" "+
			styleKey.Render("#"+strconv.FormatInt(t.ID, 10))+"  "+t.Title+"  "+styleDim.Render(state))
	}

	if iss.Source != nil {
		lines = append(lines, "", " "+styleTitle.Render("Source"))
		src := iss.Source
		for _, f := range [][2]string{
			{"provider", src.Provider},
			{"issue", issueSourceBadge(iss)},
			{"url", src.URL},
			{"remote", src.RemoteState},
		} {
			if f[1] != "" {
				lines = append(lines, "  "+styleDim.Render(padRight(f[0], 10))+f[1])
			}
		}
	}
	return lines
}

// threadLines is the Comments section: oldest first, each headed by its
// author and time, with a mirrored comment marked as GitHub's. Called inside
// pageLines' render pass, so the bodies share the description's cache.
func (v *issueView) threadLines(width int) []string {
	lines := []string{"", " " + styleTitle.Render("Comments") + styleDim.Render("  "+plural(len(v.comments), "comment", "comments"))}
	if v.commentsErr != nil {
		return append(lines, styleBad.Render("  ⚠ could not read the comments: "+errString(v.commentsErr)))
	}
	if len(v.comments) == 0 {
		if v.commentable() {
			return append(lines, styleDim.Render("  no comment yet · "+opKey(keymap.Comment)+" writes one"))
		}
		return append(lines, styleDim.Render("  no comment yet"))
	}
	for i, c := range v.comments {
		if i > 0 {
			lines = append(lines, "")
		}
		head := "  " + styleKey.Render(c.Author) + styleDim.Render("  "+c.CreatedAt.Local().Format("2006-01-02 15:04"))
		if c.Remote {
			head += styleDim.Render("  · github")
		}
		if c.UpdatedAt.After(c.CreatedAt) {
			head += styleDim.Render("  · edited")
		}
		lines = append(lines, head)
		md, _ := v.md.lines(c.Body, max(width-6, 10), levelNormal, v.raw.get(), v.links.get())
		for _, l := range md {
			lines = append(lines, "    "+l)
		}
	}
	return lines
}
