package tui

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The issues list (§15 view 12, task 130.9).
//
// It lists the selected project's issues, local and imported alike (task
// 132.11, superseding 130.9 decision 1's cross-project, grouped-by-project
// screen): the project is the root (task 132 decision 1), and a switch swaps
// the list rather than scrolling to another group. Within the project the
// list is a board of the daemon's four derived lanes (task 134.8,
// superseding 132.11's flat list) — issuesections.go.
//
// Issues are local rows, so one GET /v1/issues?project_id= fills the whole
// screen, and a load error is screen-wide.
//
// `R` re-reads and nothing more (decision 2). Whether an imported issue is
// current is the reconciler's business and `vincent issue sync`'s, never a
// keypress on a list.

// issueEventPrefix is every issue.* event (§13.3): created, updated,
// state_changed, labels_changed, comment_added and comment_updated (task 130
// decision 24), and lane_changed (task 134.6). Each carries the issue id in its payload, and any of them can
// change a row — or, on the detail, the thread.
const issueEventPrefix = "issue."

func isIssueEvent(t string) bool { return strings.HasPrefix(t, issueEventPrefix) }

// issueEventID reads the issue id an issue.* event's payload names, 0 when it
// names none.
func issueEventID(ev apiclient.Event) int64 {
	var body struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(ev.Payload, &body) != nil {
		return 0
	}
	return body.ID
}

// taskEventIssueID reads the issue_id a task.* event's payload carries — set
// on task.created, task.state_changed, task.deleted and task.restored for a
// task that has an issue (task 134.6) — 0 when it carries none.
func taskEventIssueID(ev apiclient.Event) int64 {
	var body struct {
		IssueID int64 `json:"issue_id"`
	}
	if json.Unmarshal(ev.Payload, &body) != nil {
		return 0
	}
	return body.IssueID
}

// Issues-list messages.
type (
	issuesRefreshMsg struct{}
	issuesLoadedMsg  struct {
		// showDone is the scope the listing was asked for. `s` pressed
		// twice in quick succession has two listings in flight, and only
		// the one for the scope on screen may land.
		showDone bool
		stamp    loadStamp
		issues   []apiclient.Issue
		err      error
	}
	// openIssueMsg asks the root to open one issue's detail screen. The
	// root points the detail at it before switching, the order every
	// takeover that carries an argument uses (task 049 decision 1). back is
	// where esc on the detail lands; zero means the issue list.
	openIssueMsg struct {
		id   int64
		back viewID
		// projectID is the issue's project, for selectTaskMsg's reason.
		projectID int64
	}
)

// issuesView is §15's view 12.
type issuesView struct {
	// projectScope is the root's selected project (task 132.2). A switch
	// reloads, and stamps drops a load issued for the previous project or
	// overtaken by a newer one (task 132.5).
	projectScope
	stamps loadStamps
	// noProjects is a listing that came back empty. A selection of 0 means
	// "no project registered" only then; before the first listing it is a
	// selection not resolved yet (review F9 on the chats board).
	noProjects bool

	client *apiclient.Client
	now    func() time.Time

	issues []apiclient.Issue

	loaded   bool
	loading  bool
	loadErr  error
	lastLoad time.Time

	// cursor indexes lines(). selected is the issue under it and
	// selectedLane the folded section, so a re-list that reorders the rows
	// — every update moves an issue to the top, and a task can move it to
	// another section — keeps the selection on the thing rather than on the
	// index (remember).
	cursor       int
	selected     int64
	selectedLane string
	// folded is the set of collapsed sections, by lane. Session-only, and
	// kept across a project switch, as the filter is.
	folded map[string]bool

	filter    textField
	filtering bool

	// showDone is `s`'s toggle (task 134 decision 5): hidden, the default,
	// lists `state=open` and draws three sections; shown lists every state
	// and draws `done` fourth. Session-only, and kept across a project
	// switch.
	showDone    bool
	note        string
	noteBad     bool
	refreshWait bool

	// w is the form and the close/reopen/delete prompt (task 130.12).
	w issueWrites
}

func newIssuesView() *issuesView {
	fi := newTextField()
	fi.SetPlaceholder("filter by id, title, label or kind")
	fi.SetPrompt("/")
	v := &issuesView{now: time.Now, filter: fi, folded: map[string]bool{}, w: newIssueWrites()}
	v.reload = v.loadCmd
	return v
}

func (v *issuesView) title() string { return "Issues" }

func (v *issuesView) setClient(c *apiclient.Client) tea.Cmd {
	v.client = c
	return v.loadCmd()
}

// setProjects records whether any project is registered (projectListAware).
// The root hands it only a listing that succeeded.
func (v *issuesView) setProjects(ps []apiclient.Project) { v.noProjects = len(ps) == 0 }

// setProject wraps projectScope's: a switch empties the list before the
// reload goes out (task 132.11), so the issues of the project just left are
// never shown under the name of the one just chosen, and the header reads
// "loading ‹name›…" until the stamped load lands. The filter text, the
// `done` toggle and the folds stay: they say what the human wants to see,
// not where.
func (v *issuesView) setProject(p projectSel) tea.Cmd {
	if p.id != v.project.id {
		v.issues, v.loaded, v.loadErr = nil, false, nil
		v.lastLoad = time.Time{}
		v.cursor, v.selected, v.selectedLane = 0, 0, ""
		v.w.act = nil
	}
	return v.projectScope.setProject(p)
}

// capturesInput holds the global keys back while the filter types and while
// the form or the prompt is up: both own the keyboard, and `n` answers the
// prompt's question rather than opening the new-task form.
func (v *issuesView) capturesInput() bool { return v.filtering || v.w.open() }

func (v *issuesView) bindingContext() bindingContext { return v.w.context(ctxIssues) }

func (v *issuesView) paste(text string) tea.Cmd {
	if v.w.open() {
		return v.w.paste(text)
	}
	if !v.filtering {
		return nil
	}
	var cmd tea.Cmd
	v.filter, cmd = v.filter.Update(tea.PasteMsg{Content: text})
	return cmd
}

func (v *issuesView) update(msg tea.Msg) (panel, tea.Cmd) {
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
			// A new issue opens on its own screen, the way a new task opens
			// its workspace.
			id, pid := msg.saved.ID, msg.saved.ProjectID
			return v, func() tea.Msg { return openIssueMsg{id: id, projectID: pid} }
		}
		v.setNote("saved issue #"+strconv.FormatInt(msg.saved.ID, 10), false)
		return v, v.loadCmd()
	case issueEditTargetMsg:
		if msg.err != nil {
			v.setNote("could not read the issue: "+errString(msg.err), true)
			return v, nil
		}
		iss := msg.issue
		return v, v.w.openForm(v.client, &iss, iss.ProjectID)
	case issueActionTargetMsg:
		if msg.err != nil {
			v.setNote("could not read the issue: "+errString(msg.err), true)
			return v, nil
		}
		act, cmd, note := newIssueStateAction(v.client, msg.issue)
		if note != "" {
			v.setNote(note, true)
		}
		v.w.act = act
		return v, cmd
	case issueActedMsg:
		v.w.act = nil
		v.setNote(actedNote(msg), msg.err != nil)
		return v, v.loadCmd()
	case viewActivatedMsg:
		if msg.id == viewIssues {
			return v, v.loadCmd()
		}
		return v, nil
	case issuesRefreshMsg:
		v.refreshWait = false
		return v, v.loadCmd()
	case issuesLoadedMsg:
		v.applyLoaded(msg)
		return v, nil
	case openedURLMsg:
		if msg.err != nil {
			v.setNote(openFailure(msg), true)
		} else {
			v.note = ""
		}
		return v, nil
	case noteMsg:
		return v, v.updateNote(msg.note)
	case tea.KeyPressMsg:
		return v.updateKey(msg)
	}
	return v, nil
}

// loadCmd lists the selected project's issues in one call. With no project
// selected it fetches nothing (task 132.11): GET /v1/issues without
// project_id is every project's list, which a scoped view must never show.
func (v *issuesView) loadCmd() tea.Cmd {
	client := v.client
	if client == nil {
		return nil
	}
	if v.project.id == 0 {
		// A load issued for the project just left must not land here.
		v.stamps.drop(0)
		v.issues, v.loading = nil, false
		v.cursor = 0
		return nil
	}
	v.loading = true
	showDone := v.showDone
	stamp := v.stamps.next(v.project.id)
	opts := v.listOptions()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		issues, err := client.ListIssues(ctx, opts)
		return issuesLoadedMsg{showDone: showDone, stamp: stamp, issues: issues, err: err}
	}
}

// listOptions is the listing's query. `done` is exactly `closed` (task 134
// decision 4), so hiding it is the open listing — the daemon sends nothing
// the list would not draw — and showing it lists every state.
func (v *issuesView) listOptions() apiclient.IssueListOptions {
	opts := apiclient.IssueListOptions{ProjectID: v.project.id}
	if !v.showDone {
		opts.States = []string{"open"}
	}
	return opts
}

func (v *issuesView) applyLoaded(msg issuesLoadedMsg) {
	if msg.showDone != v.showDone || !v.stamps.accepts(msg.stamp) {
		return
	}
	v.stamps.apply(msg.stamp)
	v.noteLoad(msg.err)
	v.loading = false
	if msg.err != nil {
		v.loadErr = msg.err
		return
	}
	v.loaded, v.loadErr = true, nil
	v.lastLoad = v.now()
	v.issues = msg.issues
	v.reselect()
}

func (v *issuesView) scheduleRefresh() tea.Cmd {
	if v.refreshWait || v.client == nil {
		return nil
	}
	v.refreshWait = true
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return issuesRefreshMsg{} })
}

// updateNote re-lists on any issue event — issue.lane_changed among them —
// on project events, and on a task event only when its payload's issue_id
// names a row this list shows (task 134.6 decision 6): such an event moves
// that row's task count or active marker. A step advancing, a status line,
// or a task with no shown issue changes no row, so it no longer costs a
// re-list.
func (v *issuesView) updateNote(n apiclient.Note) tea.Cmd {
	ev, ok := n.(apiclient.EventNote)
	if !ok {
		return nil
	}
	if !forProject(ev.Event, v.project.id) {
		return nil
	}
	switch t := ev.Event.Type; {
	case isIssueEvent(t), strings.HasPrefix(t, "project."):
		return v.scheduleRefresh()
	case strings.HasPrefix(t, "task."):
		if id := taskEventIssueID(ev.Event); id != 0 && v.shows(id) {
			return v.scheduleRefresh()
		}
	}
	return nil
}

// shows reports whether issue id is one of the loaded rows.
func (v *issuesView) shows(id int64) bool {
	return slices.ContainsFunc(v.issues, func(is apiclient.Issue) bool { return is.ID == id })
}

func (v *issuesView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	if v.filtering {
		switch msg.String() {
		case "esc":
			v.filtering = false
			v.filter.SetValue("")
			v.filter.Blur()
			v.clampCursor()
			return v, nil
		case "enter":
			v.filtering = false
			v.filter.Blur()
			return v, nil
		}
		var cmd tea.Cmd
		v.filter, cmd = v.filter.Update(msg)
		v.clampCursor()
		return v, cmd
	}

	switch msg.String() {
	case "up", "k":
		v.moveCursor(-1)
		return v, nil
	case "down", "j":
		v.moveCursor(1)
		return v, nil
	case opKey(keymap.Filter):
		v.filtering = true
		v.filter.Focus()
		return v, nil
	case "esc":
		// One layer per press (§15): a committed filter or a note first.
		if v.filter.Value() != "" || v.note != "" {
			v.filter.SetValue("")
			v.note = ""
			v.clampCursor()
			return v, nil
		}
		return v, func() tea.Msg { return selectViewMsg{id: viewHome} }
	case opKey(keymap.Refresh):
		return v, v.loadCmd()
	case opKey(keymap.Scope):
		v.toggleDone()
		return v, v.loadCmd()
	case "left":
		v.fold(true)
		return v, nil
	case "right":
		v.fold(false)
		return v, nil
	case "C":
		v.foldAll(true)
		return v, nil
	case "O":
		v.foldAll(false)
		return v, nil
	case opKey(keymap.OpenRow):
		row, ok := v.current()
		if !ok {
			return v, nil
		}
		id, pid := row.issue.ID, row.issue.ProjectID
		return v, func() tea.Msg { return openIssueMsg{id: id, projectID: pid} }
	case opKey(keymap.Browser):
		return v, v.openSelected()
	case opKey(keymap.New):
		return v, v.w.openForm(v.client, nil, v.project.id)
	case opKey(keymap.Add):
		// A task from the selected issue (task 130.13): the form opens
		// seeded with it, editable before anything is created.
		if row, ok := v.current(); ok {
			return v, newTaskFromIssueCmd(row.issue)
		}
	case issueEditKey:
		if row, ok := v.current(); ok {
			return v, issueEditFor(v.client, row.issue.ID)
		}
	case issueStateKey:
		if row, ok := v.current(); ok {
			return v, issueActionFor(v.client, row.issue.ID)
		}
	case opKey(keymap.Delete):
		if row, ok := v.current(); ok {
			v.w.act = newIssueDelete(v.client, row.issue)
		}
	}
	return v, nil
}

// toggleDone shows or hides the `done` section (task 134 decision 5). It
// replaces the open → closed → all cycle: a closed issue is `done`, so the
// one question left is whether to see them. Shown, the section opens
// expanded.
func (v *issuesView) toggleDone() {
	v.showDone = !v.showDone
	if v.showDone {
		delete(v.folded, issueLaneDone)
		v.setNote("showing the done lane…", false)
		return
	}
	v.setNote("hiding the done lane…", false)
}

// openSelected hands an imported issue's URL to a browser. A local issue has
// no page anywhere, and says so rather than doing nothing silently.
func (v *issuesView) openSelected() tea.Cmd {
	row, ok := v.current()
	if !ok {
		return nil
	}
	url := issueURL(row.issue)
	if url == "" {
		v.setNote("issue #"+strconv.FormatInt(row.issue.ID, 10)+" is local — there is no page to open", true)
		return nil
	}
	v.setNote("opening "+url+"…", false)
	return openURLCmd(url)
}

// issueURL is an imported issue's page, "" for a local one.
func issueURL(iss apiclient.Issue) string {
	if iss.Source == nil {
		return ""
	}
	return strings.TrimSpace(iss.Source.URL)
}

func (v *issuesView) setNote(text string, bad bool) { v.note, v.noteBad = text, bad }

// issueMatches is `/`'s client-side match: id, title, labels and kind. The
// project's name is no longer a term (task 132.11): every row is in it.
func issueMatches(iss apiclient.Issue, q string) bool {
	id := strconv.FormatInt(iss.ID, 10)
	hay := strings.ToLower(strings.Join(append([]string{
		"#" + id, id, iss.Title, iss.Kind,
	}, iss.Labels...), " "))
	return strings.Contains(hay, q)
}

// --- rendering ---

func (v *issuesView) render(width, height int) string {
	if width < 4 || height < 2 {
		return ""
	}
	if v.w.form != nil {
		return v.w.form.render(width, height)
	}
	lines := make([]string, 0, height)
	lines = append(lines, v.headerLine(width))
	if v.filtering || v.filter.Value() != "" {
		v.filter.SetWidth(max(width-1, 10))
		lines = append(lines, fieldRows(" ", v.filter)...)
	}
	lines = append(lines, "")

	body, cursorRow := v.bodyLines(width)
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
	room := max(height-len(lines)-len(footer), 1)
	lines = append(lines, window(body, cursorRow, room)...)
	lines = append(lines, footer...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

func (v *issuesView) headerLine(width int) string {
	left := " " + styleTitle.Render("issues")
	switch {
	case v.project.id == 0:
		left += styleDim.Render("  ·  no project selected")
	case !v.loaded && v.loading:
		// The project's name, not a bare "listing…": after a switch this is
		// the one line saying which project the empty list is waiting on.
		left += styleDim.Render("  ·  loading " + v.project.name + "…")
	case v.loaded:
		left += styleDim.Render("  ·  " + plural(len(v.issues), "issue", "issues"))
	}
	right := ""
	if !v.lastLoad.IsZero() {
		right = styleDim.Render("updated "+v.lastLoad.Format("15:04:05")) + " "
	}
	return padBetween(left, right, width)
}

// totalsLine counts the listing per section, unfiltered: `N open · N in
// progress · N hand-off`, then the `done` count once `s` shows it, or a dim
// reminder that it is hidden and which key shows it.
func (v *issuesView) totalsLine() string {
	totals := v.laneTotals()
	parts := make([]string, 0, len(issueSections))
	for i := range len(issueSections) - 1 {
		parts = append(parts, strconv.Itoa(totals[i])+" "+issueSections[i].label)
	}
	line := "  " + strings.Join(parts, " · ")
	if v.showDone {
		return line + " · " + strconv.Itoa(totals[len(issueSections)-1]) + " " + issueLaneDone
	}
	return line + styleDim.Render(" · "+issueLaneDone+" hidden ("+opKey(keymap.Scope)+")")
}

func (v *issuesView) bodyLines(width int) (lines []string, cursorRow int) {
	if v.project.id == 0 {
		// Never an unfiltered list in its place (task 132.11). No selection
		// means no project is registered only once a listing has said so;
		// until then it is still resolving.
		if v.noProjects {
			return []string{styleDim.Render("  " + noProjectsEmpty())}, 0
		}
		return []string{styleDim.Render("  Resolving the project…")}, 0
	}
	if v.loadErr != nil {
		// Screen-wide, because the list is one request.
		out := []string{styleBad.Render("  ⚠ could not list issues: " + errString(v.loadErr))}
		if v.loaded {
			out = append(out, styleDim.Render("  showing what was listed at "+v.lastLoad.Format("15:04:05")), "")
		} else {
			return out, 0
		}
		rest, row := v.listLines(width)
		return append(out, rest...), row + len(out)
	}
	if !v.loaded {
		return []string{styleDim.Render("  listing " + v.project.name + "'s issues…")}, 0
	}
	lines, cursorRow = v.listLines(width)
	if len(v.issues) == 0 {
		lines = append(lines, "",
			styleDim.Render("  `vincent issue add` files one; a GitHub project's issues arrive on the reconciler tick."))
	}
	return lines, cursorRow
}

// listLines is the totals line and the sections, filtered, and the cursor's
// line in them.
func (v *issuesView) listLines(width int) (lines []string, cursorRow int) {
	filtering := strings.TrimSpace(v.filter.Value()) != ""
	rows := v.lines()
	lines = make([]string, 0, len(rows)+2)
	lines = append(lines, v.totalsLine(), "")
	inner := max(width-len(groupIndent), 12)
	for i, row := range rows {
		selected := i == v.cursor
		if selected {
			cursorRow = len(lines)
		}
		switch {
		case row.header:
			lines = append(lines, issueSectionHeader(row, selected))
		case row.none:
			none := "none"
			if filtering {
				none = "none match"
			}
			lines = append(lines, groupIndent+"  "+styleDim.Render(none))
		default:
			lines = append(lines, groupIndent+issueLine(row.issue, inner, selected))
		}
	}
	return lines, cursorRow
}

// issueSectionHeader is a section's header: the fold glyph, the bold label,
// the count, and the `! n` badge for the issues whose root task waits on a
// human — kept on the header so folding the section cannot hide it.
func issueSectionHeader(row issueRow, selected bool) string {
	marker := "  "
	if selected {
		marker = styleFocus.Render("› ")
	}
	glyph := groupGlyphOpen
	if row.collapsed {
		glyph = groupGlyphFolded
	}
	out := marker + styleGroup.Render(glyph+" "+issueSections[row.section].label) +
		styleDim.Render("  "+strconv.Itoa(row.count))
	if row.attention > 0 {
		out += "  " + styleWarn.Render(attentionBadge+" "+strconv.Itoa(row.attention))
	}
	return out
}

// issueLine is one row: id, state badge, title, labels and kind, the source
// badge of an imported issue, and the linked-task summary.
func issueLine(iss apiclient.Issue, width int, selected bool) string {
	marker := "  "
	if selected {
		marker = styleFocus.Render("› ")
	}
	id := padRight("#"+strconv.FormatInt(iss.ID, 10), 6)
	state := issueStateBadge(iss)
	tags := issueTags(iss)
	source := issueSourceBadge(iss)
	tasks := issueTaskSummary(iss)

	// The right-hand parts share what the title's 12-column floor leaves
	// (review F2 on #773): at the 80-column floor an in-progress cell, tags
	// and a source badge can outgrow it, and render's cut would take the
	// cell's counts. They give way in a fixed order — the occupant's state
	// word, then the tags, then the source badge, then the cell itself.
	room := width - (2 + len(id) + ansi.StringWidth(state) + 2 + 6) - 12
	over := func() int {
		return ansi.StringWidth(tags) + ansi.StringWidth(source) + ansi.StringWidth(tasks) - room
	}
	if over() > 0 {
		tasks = issueTaskSummaryShort(iss)
	}
	if n := over(); n > 0 {
		tags = fitWidth(tags, ansi.StringWidth(tags)-n)
	}
	if n := over(); n > 0 {
		source = fitWidth(source, ansi.StringWidth(source)-n)
	}
	if n := over(); n > 0 {
		tasks = fitWidth(tasks, ansi.StringWidth(tasks)-n)
	}

	fixed := 2 + len(id) + ansi.StringWidth(state) + 2 + ansi.StringWidth(tags) +
		ansi.StringWidth(source) + ansi.StringWidth(tasks) + 6
	titleW := max(width-fixed, 12)
	title := ansi.Truncate(iss.Title, titleW, "…")

	line := marker + styleKey.Render(id) + issueStateStyle(iss).Render(state) + "  " +
		padDisplayWidth(title, titleW)
	for _, part := range []string{tags, source} {
		if part != "" {
			line += "  " + styleDim.Render(part)
		}
	}
	return line + "  " + issueTaskStyle(iss).Render(tasks)
}

// issueStateBadge is the state word, with the close reason when closed.
func issueStateBadge(iss apiclient.Issue) string {
	if iss.State != "closed" || iss.CloseReason == "" {
		return iss.State
	}
	reason := strings.ReplaceAll(iss.CloseReason, "_", " ")
	if iss.CloseReason == "duplicate" && iss.DuplicateOf != nil {
		reason = "duplicate of #" + strconv.FormatInt(*iss.DuplicateOf, 10)
	}
	return "closed · " + reason
}

func issueStateStyle(iss apiclient.Issue) lipgloss.Style {
	if iss.State == "open" {
		return styleOK
	}
	return styleDim
}

// issueTags is the labels and the kind, the two things a row is filtered by
// besides its title.
func issueTags(iss apiclient.Issue) string {
	parts := make([]string, 0, len(iss.Labels)+1)
	for _, l := range iss.Labels {
		parts = append(parts, "["+l+"]")
	}
	if iss.Kind != "" {
		parts = append(parts, iss.Kind)
	}
	return strings.Join(parts, " ")
}

// issueSourceBadge is `owner/repo#N` for an imported issue, "" for a local one.
func issueSourceBadge(iss apiclient.Issue) string {
	if iss.Source == nil || iss.Source.Repo == "" {
		return ""
	}
	badge := iss.Source.Repo
	if iss.Source.Number > 0 {
		badge += "#" + strconv.Itoa(iss.Source.Number)
	}
	return badge
}

// issueTaskSummary is the row's linked-task column, worded for the issue's
// lane (task 134.16 decision 3). Every value comes from the list DTO; a row
// makes no task fetch of its own (task 130 decision 16.4). The close reason
// is never repeated here: the state badge carries it.
func issueTaskSummary(iss apiclient.Issue) string {
	return issueTaskCell(iss, true)
}

// issueTaskSummaryShort is issueTaskSummary without the occupant's state
// word, the first thing a row too narrow for its parts gives up: the
// side and merging counts the cell exists to show stay.
func issueTaskSummaryShort(iss apiclient.Issue) string {
	return issueTaskCell(iss, false)
}

// fitWidth cuts s to at most w columns with an ellipsis, or to nothing when
// there is no room for more than the ellipsis.
func fitWidth(s string, w int) string {
	if w <= 1 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

func issueTaskCell(iss apiclient.Issue, withState bool) string {
	tasks := plural(iss.TaskCount, "task", "tasks")
	switch iss.Lane {
	case "open":
		// Any task an open-lane issue has is aborted (task 134 decision 2).
		if iss.TaskCount == 0 {
			return "no tasks"
		}
		return tasks + " · cancelled"
	case "in_progress":
		mw := iss.MainWorktree
		if mw == nil || mw.OccupantTaskID == nil {
			// Queued, side-only, or from before roles: no occupant to name.
			return "● " + tasks
		}
		s := "● #" + strconv.FormatInt(*mw.OccupantTaskID, 10)
		if withState && mw.OccupantState != nil {
			s += " " + *mw.OccupantState
		}
		if iss.SideActive > 0 {
			s += " · +" + strconv.Itoa(iss.SideActive) + " side"
		}
		if iss.MergeBacksPending > 0 {
			s += " · " + strconv.Itoa(iss.MergeBacksPending) + " merging"
		}
		return s
	case "hand_off":
		// Nothing waits here: the lane has no unsettled root task.
		return "✓ " + strconv.Itoa(iss.TaskCount) + " done"
	case issueLaneDone:
		if iss.TaskCount == 0 {
			return "no tasks"
		}
		if iss.Active {
			return tasks + " · ● live"
		}
		return tasks
	}
	// A daemon too old to send a lane: the count and the activity marker.
	if iss.TaskCount == 0 {
		return "no tasks"
	}
	if iss.Active {
		return "● " + tasks
	}
	return tasks
}

func issueTaskStyle(iss apiclient.Issue) lipgloss.Style {
	if iss.Active {
		return styleWarn
	}
	return styleDim
}

// issuePriorityLabel names an issue's priority. The scale is Linear's and
// inverted against a task's (task 130 decision 4): 1 is the most urgent, and
// 0 is no priority at all rather than the lowest.
func issuePriorityLabel(p int) string {
	switch p {
	case 1:
		return "urgent"
	case 2:
		return "high"
	case 3:
		return "medium"
	case 4:
		return "low"
	case 0:
		return "none"
	}
	return strconv.Itoa(p)
}
