package tui

import (
	"context"
	"encoding/json"
	"fmt"
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
// It answers the question the pull-requests takeover answers for pull
// requests — what is open across everything I run — for vincent's own issues,
// local and imported alike. So it is cross-project and grouped by project,
// with the headings drawn by the renderer rather than offered as rows
// (130.9 decision 1).
//
// Issues are local rows, so one GET /v1/issues fills the whole screen. That
// makes a load error screen-wide: the per-project error band the pull-requests
// takeover carries exists because each of its groups is its own request, and
// here there is no request that could fail for one project alone.
//
// `R` re-reads and nothing more (decision 2). Whether an imported issue is
// current is the reconciler's business and `vincent issue sync`'s, never a
// keypress on a list.

// issueEventPrefix is every issue.* event (§13.3): created, updated,
// state_changed, labels_changed, comment_added. Each carries the issue id in
// its payload, and any of them can change a row.
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

// issueStates is the cycle `s` walks: open first, because open is what a
// person comes to an issue list to see.
var issueStates = []string{"open", "closed", "all"}

// Issues-list messages.
type (
	issuesRefreshMsg struct{}
	issuesLoadedMsg  struct {
		// state is the scope the listing was asked for. `s` pressed twice
		// in quick succession has two listings in flight, and only the one
		// for the scope on screen may land.
		state    string
		issues   []apiclient.Issue
		projects []apiclient.Project
		err      error
	}
	// openIssueMsg asks the root to open one issue's detail screen. The
	// root points the detail at it before switching, the order every
	// takeover that carries an argument uses (task 049 decision 1).
	openIssueMsg struct{ id int64 }
)

// issueGroup is one project's issues, in the order the daemon served them.
type issueGroup struct {
	project apiclient.Project
	issues  []apiclient.Issue
}

// issueRow is one selectable line.
type issueRow struct {
	project apiclient.Project
	issue   apiclient.Issue
}

// issuesView is §15's view 12.
type issuesView struct {
	client *apiclient.Client
	now    func() time.Time

	issues   []apiclient.Issue
	projects []apiclient.Project

	loaded   bool
	loading  bool
	loadErr  error
	lastLoad time.Time

	cursor int
	// selected is the id under the cursor, so a re-list that reorders the
	// rows — every update moves an issue to the top — keeps the selection
	// on the issue rather than on the index.
	selected int64

	filter    textField
	filtering bool

	// state is the listing's `state=`, cycled by `s`; "all" sends none.
	state       string
	note        string
	noteBad     bool
	refreshWait bool
}

func newIssuesView() *issuesView {
	fi := newTextField()
	fi.SetPlaceholder("filter by id, title, label, kind or project")
	fi.SetPrompt("/")
	return &issuesView{now: time.Now, filter: fi, state: issueStates[0]}
}

func (v *issuesView) title() string { return "Issues" }

func (v *issuesView) setClient(c *apiclient.Client) tea.Cmd {
	v.client = c
	return v.loadCmd()
}

func (v *issuesView) capturesInput() bool { return v.filtering }

func (v *issuesView) paste(text string) tea.Cmd {
	if !v.filtering {
		return nil
	}
	var cmd tea.Cmd
	v.filter, cmd = v.filter.Update(tea.PasteMsg{Content: text})
	return cmd
}

// hintedProject lets `n` open the new-task form on the selected row's project.
func (v *issuesView) hintedProject() int64 {
	if row, ok := v.current(); ok {
		return row.project.ID
	}
	return 0
}

func (v *issuesView) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
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

// loadCmd lists every project's issues in one call, and the projects whose
// names head the groups.
func (v *issuesView) loadCmd() tea.Cmd {
	client := v.client
	if client == nil {
		return nil
	}
	v.loading = true
	state := v.state
	opts := apiclient.IssueListOptions{}
	if state != "all" {
		opts.States = []string{v.state}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		issues, err := client.ListIssues(ctx, opts)
		if err != nil {
			return issuesLoadedMsg{state: state, err: err}
		}
		// A heading with no name still groups correctly, so the project
		// listing failing is not a reason to hide the issues.
		projects, err := client.ListProjects(ctx)
		if err != nil {
			projects = nil
		}
		return issuesLoadedMsg{state: state, issues: issues, projects: projects}
	}
}

func (v *issuesView) applyLoaded(msg issuesLoadedMsg) {
	if msg.state != v.state {
		return
	}
	v.loading = false
	if msg.err != nil {
		v.loadErr = msg.err
		return
	}
	v.loaded, v.loadErr = true, nil
	v.lastLoad = v.now()
	v.issues = msg.issues
	if msg.projects != nil {
		v.projects = msg.projects
	}
	v.reselect()
}

func (v *issuesView) scheduleRefresh() tea.Cmd {
	if v.refreshWait || v.client == nil {
		return nil
	}
	v.refreshWait = true
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return issuesRefreshMsg{} })
}

// updateNote re-lists on any issue event, and on task events, which move a
// row's task count and its active marker.
func (v *issuesView) updateNote(n apiclient.Note) tea.Cmd {
	ev, ok := n.(apiclient.EventNote)
	if !ok {
		return nil
	}
	if isIssueEvent(ev.Event.Type) || isTaskEvent(ev.Event.Type) {
		return v.scheduleRefresh()
	}
	return nil
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
		v.cycleState()
		return v, v.loadCmd()
	case opKey(keymap.OpenRow):
		row, ok := v.current()
		if !ok {
			return v, nil
		}
		id := row.issue.ID
		return v, func() tea.Msg { return openIssueMsg{id: id} }
	case opKey(keymap.Browser):
		return v, v.openSelected()
	}
	return v, nil
}

func (v *issuesView) cycleState() {
	for i, s := range issueStates {
		if s == v.state {
			v.state = issueStates[(i+1)%len(issueStates)]
			v.setNote("listing "+v.state+" issues…", false)
			return
		}
	}
	v.state = issueStates[0]
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

// groups is the listing grouped by project: the projects in their listing
// order, then any project the listing did not name, by id. Within a group the
// daemon's order stands.
func (v *issuesView) groups() []issueGroup {
	byProject := map[int64][]apiclient.Issue{}
	for _, iss := range v.issues {
		byProject[iss.ProjectID] = append(byProject[iss.ProjectID], iss)
	}
	out := make([]issueGroup, 0, len(byProject))
	seen := map[int64]bool{}
	for _, p := range v.projects {
		if issues := byProject[p.ID]; len(issues) > 0 {
			out = append(out, issueGroup{project: p, issues: issues})
		}
		seen[p.ID] = true
	}
	var orphans []int64
	for id := range byProject {
		if !seen[id] {
			orphans = append(orphans, id)
		}
	}
	slices.Sort(orphans)
	for _, id := range orphans {
		out = append(out, issueGroup{
			project: apiclient.Project{ID: id, Name: "project #" + strconv.FormatInt(id, 10)},
			issues:  byProject[id],
		})
	}
	return out
}

// rows is the flattened, filtered selection order.
func (v *issuesView) rows() []issueRow {
	q := strings.ToLower(strings.TrimSpace(v.filter.Value()))
	out := make([]issueRow, 0, len(v.issues))
	for _, g := range v.groups() {
		for _, iss := range g.issues {
			if q != "" && !issueMatches(g.project, iss, q) {
				continue
			}
			out = append(out, issueRow{project: g.project, issue: iss})
		}
	}
	return out
}

// issueMatches is `/`'s client-side match: id, title, labels, kind and the
// project's name (decision 1).
func issueMatches(project apiclient.Project, iss apiclient.Issue, q string) bool {
	id := strconv.FormatInt(iss.ID, 10)
	hay := strings.ToLower(strings.Join(append([]string{
		"#" + id, id, iss.Title, iss.Kind, project.Name,
	}, iss.Labels...), " "))
	return strings.Contains(hay, q)
}

func (v *issuesView) current() (issueRow, bool) {
	rows := v.rows()
	if v.cursor < 0 || v.cursor >= len(rows) {
		return issueRow{}, false
	}
	return rows[v.cursor], true
}

func (v *issuesView) moveCursor(delta int) {
	v.cursor += delta
	v.clampCursor()
}

func (v *issuesView) clampCursor() {
	rows := v.rows()
	v.cursor = min(max(v.cursor, 0), max(len(rows)-1, 0))
	if v.cursor < len(rows) {
		v.selected = rows[v.cursor].issue.ID
	}
}

// reselect puts the cursor back on the issue it was on before a re-list.
func (v *issuesView) reselect() {
	if v.selected != 0 {
		for i, row := range v.rows() {
			if row.issue.ID == v.selected {
				v.cursor = i
				return
			}
		}
	}
	v.clampCursor()
}

// --- rendering ---

func (v *issuesView) render(width, height int) string {
	if width < 4 || height < 2 {
		return ""
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
	room := max(height-len(lines)-len(footer), 1)
	lines = append(lines, window(body, cursorRow, room)...)
	lines = append(lines, footer...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

func (v *issuesView) headerLine(width int) string {
	left := " " + styleTitle.Render(v.state+" issues")
	switch {
	case !v.loaded && v.loading:
		left += styleDim.Render("  ·  listing…")
	case v.loaded:
		left += styleDim.Render(fmt.Sprintf("  ·  %s across %s",
			plural(len(v.issues), "issue", "issues"),
			plural(len(v.groups()), "project", "projects")))
	}
	right := ""
	if !v.lastLoad.IsZero() {
		right = styleDim.Render("updated "+v.lastLoad.Format("15:04:05")) + " "
	}
	return padBetween(left, right, width)
}

func (v *issuesView) bodyLines(width int) (lines []string, cursorRow int) {
	if v.loadErr != nil {
		// Screen-wide, because the list is one request (decision 1).
		out := []string{styleBad.Render("  ⚠ could not list issues: " + errString(v.loadErr))}
		if v.loaded {
			out = append(out, styleDim.Render("  showing what was listed at "+v.lastLoad.Format("15:04:05")), "")
		} else {
			return out, 0
		}
		rest, row := v.groupLines(width)
		return append(out, rest...), row + len(out)
	}
	if !v.loaded {
		return []string{styleDim.Render("  listing issues…")}, 0
	}
	if len(v.issues) == 0 {
		return []string{
			styleDim.Render("  No " + strings.TrimPrefix(v.state+" ", "all ") + "issues in any project."),
			"",
			styleDim.Render("  `vincent issue add` files one; a GitHub project's issues arrive on the reconciler tick."),
		}, 0
	}
	return v.groupLines(width)
}

func (v *issuesView) groupLines(width int) (lines []string, cursorRow int) {
	q := strings.ToLower(strings.TrimSpace(v.filter.Value()))
	seen := 0
	for _, g := range v.groups() {
		shown := make([]apiclient.Issue, 0, len(g.issues))
		for _, iss := range g.issues {
			if q == "" || issueMatches(g.project, iss, q) {
				shown = append(shown, iss)
			}
		}
		if len(shown) == 0 {
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, padBetween(" "+styleTitle.Render(g.project.Name),
			styleDim.Render(plural(len(shown), "issue", "issues")+" "), width))
		for _, iss := range shown {
			selected := seen == v.cursor
			if selected {
				cursorRow = len(lines)
			}
			lines = append(lines, issueLine(iss, width, selected))
			seen++
		}
	}
	if seen == 0 && q != "" {
		lines = append(lines, styleDim.Render(fmt.Sprintf("  none of %s match %q",
			plural(len(v.issues), "issue", "issues"), q)))
	}
	return lines, cursorRow
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

// issueTaskSummary is the row's linked-task column: the count, and a marker
// when one of them is still unsettled (decision 4). Both come from the list
// DTO; a row makes no task fetch of its own.
func issueTaskSummary(iss apiclient.Issue) string {
	if iss.TaskCount == 0 {
		return "no tasks"
	}
	s := plural(iss.TaskCount, "task", "tasks")
	if iss.Active {
		s = "● " + s
	}
	return s
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
