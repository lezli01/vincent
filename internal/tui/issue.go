package tui

import (
	"context"
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
// The linked tasks are the unsettled ones, for now (130.9 decision 3): the
// detail DTO carries tasks.count and tasks.active_ids and nothing filters
// tasks by issue yet. Every root task, newest first, arrives with `?issue_id=`
// (130.7 / 130.13).

// Issue-detail messages.
type (
	issueRefreshMsg struct{ id int64 }
	issueLoadedMsg  struct {
		id    int64
		issue apiclient.Issue
		// tasks are the active ids, fetched one by one; one that vanished
		// between the two reads is left out rather than failing the screen.
		tasks []apiclient.Task
		err   error
	}
)

// issueView is §15's view 13.
type issueView struct {
	client *apiclient.Client

	id      int64
	issue   apiclient.Issue
	tasks   []apiclient.Task
	loaded  bool
	loadErr error

	// cursor is the selected linked task; scroll is the page's first line.
	cursor int
	scroll int
	// follow asks the next render to scroll the selected task into view;
	// pgup/pgdown leave it unset so the page scrolls past the selection.
	follow bool

	raw   *rawHolder
	links *hyperlinkHolder
	md    mdCache

	note        string
	noteBad     bool
	refreshWait bool

	// w is the form and the close/reopen/delete prompt (task 130.12).
	w issueWrites
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

func (v *issueView) hintedProject() int64 { return v.issue.ProjectID }

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
			id := msg.saved.ID
			return v, func() tea.Msg { return openIssueMsg{id: id} }
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
		tasks := make([]apiclient.Task, 0, len(iss.Tasks.ActiveIDs))
		for _, tid := range iss.Tasks.ActiveIDs {
			if t, err := client.GetTask(ctx, tid); err == nil {
				tasks = append(tasks, t.Task)
			}
		}
		return issueLoadedMsg{id: id, issue: iss, tasks: tasks}
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
	v.cursor = min(v.cursor, max(len(v.tasks)-1, 0))
}

func (v *issueView) scheduleRefresh() tea.Cmd {
	if v.refreshWait || v.client == nil || v.id == 0 {
		return nil
	}
	v.refreshWait = true
	id := v.id
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return issueRefreshMsg{id: id} })
}

// updateNote re-reads on this issue's own events and on events of the tasks
// it lists. Another issue's events leave it alone.
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
	case isTaskEvent(ev.Event.Type) && ev.Event.TaskID != nil:
		if v.linksTask(*ev.Event.TaskID) {
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
		v.cursor = max(v.cursor-1, 0)
		v.follow = true
		return v, nil
	case "down", "j":
		v.cursor = min(v.cursor+1, max(len(v.tasks)-1, 0))
		v.follow = true
		return v, nil
	case "pgup":
		v.scroll = max(v.scroll-pageStep, 0)
		return v, nil
	case "pgdown":
		v.scroll += pageStep
		return v, nil
	case "esc":
		if v.note != "" {
			v.note = ""
			return v, nil
		}
		// Back to the list, which kept its own selection.
		return v, func() tea.Msg { return selectViewMsg{id: viewIssues} }
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
	}
	if !v.loaded {
		return v, nil
	}
	switch msg.String() {
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
	}
	return v, nil
}

// pageStep is how far pgup/pgdown move the page.
const pageStep = 10

// openTask opens the selected linked task's workspace, which returns here on
// esc rather than to the board.
func (v *issueView) openTask() tea.Cmd {
	if v.cursor < 0 || v.cursor >= len(v.tasks) {
		return nil
	}
	t := v.tasks[v.cursor]
	return func() tea.Msg { return selectTaskMsg{id: t.ID, state: t.State, back: viewIssue} }
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
	body, cursorRow := v.pageLines(width)
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
	// The page scrolls freely; moving the task selection brings its row
	// back into view.
	if v.follow && cursorRow >= 0 {
		if cursorRow < v.scroll {
			v.scroll = cursorRow
		} else if cursorRow >= v.scroll+room {
			v.scroll = cursorRow - room + 1
		}
		v.follow = false
	}
	v.scroll = min(max(v.scroll, 0), max(len(body)-room, 0))
	lines := append([]string(nil), body[v.scroll:min(v.scroll+room, len(body))]...)
	lines = append(lines, footer...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

// pageLines is the whole page, and the line the selected linked task is on
// (-1 when there is none).
func (v *issueView) pageLines(width int) (lines []string, cursorRow int) {
	cursorRow = -1
	switch {
	case v.loadErr != nil && !v.loaded:
		return []string{
			" " + styleTitle.Render(v.title()), "",
			styleBad.Render("  ⚠ could not read the issue: " + errString(v.loadErr)),
		}, cursorRow
	case !v.loaded:
		return []string{" " + styleTitle.Render(v.title()), "", styleDim.Render("  reading…")}, cursorRow
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
		{"labels", labels},
		{"kind", kind},
		{"priority", issuePriorityLabel(iss.Priority)},
		{"author", iss.Author},
	} {
		lines = append(lines, "  "+styleDim.Render(padRight(f[0], 10))+f[1])
	}

	lines = append(lines, "", " "+styleTitle.Render("Description"))
	if strings.TrimSpace(iss.Body) == "" {
		lines = append(lines, styleDim.Render("  no description"))
	} else {
		v.md.begin()
		md, _ := v.md.lines(iss.Body, max(width-4, 10), levelNormal, v.raw.get(), v.links.get())
		v.md.sweep()
		for _, l := range md {
			lines = append(lines, "  "+l)
		}
	}

	lines = append(lines, "", " "+styleTitle.Render("Tasks")+
		styleDim.Render("  "+plural(iss.Tasks.Count, "task", "tasks")+", "+
			strconv.Itoa(len(iss.Tasks.ActiveIDs))+" active"))
	if len(v.tasks) == 0 {
		lines = append(lines, styleDim.Render("  no active task"))
	}
	for i, t := range v.tasks {
		marker := "  "
		if i == v.cursor {
			marker = styleFocus.Render("› ")
			cursorRow = len(lines)
		}
		glyph := taskStateGlyph(t.State)
		if glyph == "" {
			glyph = "·"
		}
		lines = append(lines, marker+stateStyles[t.State].Render(glyph)+" "+
			styleKey.Render("#"+strconv.FormatInt(t.ID, 10))+"  "+t.Title+"  "+styleDim.Render(t.State))
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
	return lines, cursorRow
}
