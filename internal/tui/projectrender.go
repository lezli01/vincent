package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

func (p *projectsView) render(width, height int) string {
	if width > 0 {
		p.width = width
	}
	if height > 0 {
		p.height = height
	}
	rows := p.visible()
	if p.form != nil {
		// The add/edit form takes the focused surface as it always has
		// (task 132 decision 35), beside the list it edits.
		if guidedTakeover(p.width, p.height) {
			return p.renderGuided(rows)
		}
		return p.form.render(p.width)
	}
	return p.renderOverview(rows)
}

// Overview layout. The detail pane is the first thing a narrow terminal sheds
// and the table's columns the second (task 132 decision 35): the pane only
// shows when the full table still fits beside it.
const (
	overviewDetailWidth = 40
	// overviewAttentionMax bounds the "needs you" list's share of the
	// screen; the rest of it scrolls with the cursor.
	overviewAttentionMax = 8
)

// renderOverview is the table, its totals row, the "needs you" list and, on a
// wide terminal, the highlighted project's configuration beside them.
func (p *projectsView) renderOverview(rows []apiclient.Project) string {
	leftW := p.width
	detail := p.width >= overviewFullWidth()+overviewDetailWidth && p.height >= guidedTakeoverMinHeight
	if detail {
		leftW = p.width - overviewDetailWidth
	}
	left := p.renderOverviewMain(rows, leftW)
	if !detail {
		return left
	}
	title := "Project"
	body := ""
	if pr, ok := p.current(); ok {
		title = "Project · " + pr.Name
		body = p.renderProjectDetail(pr, p.height-2)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftW).MaxWidth(leftW).Render(left),
		frame(title, body, overviewDetailWidth, p.height, false))
}

func (p *projectsView) renderOverviewMain(rows []apiclient.Project, width int) string {
	lines := append([]string{}, p.statusLines()...)
	if body, ok := p.emptyBody(rows); ok {
		return strings.Join(append(lines, body), "\n")
	}
	attn := p.attention()
	attnLines := p.attentionLines(attn, width)
	// Status, the table's header, the totals row, a gap, the list's heading
	// and its rows.
	avail := p.height - len(lines) - 1 - 1 - 2 - len(attnLines)
	p.syncTable(rows, width, max(2, min(len(rows), avail)+1))
	lines = append(lines, p.tbl.View(), p.totalsLine(), "", section("Needs you, across projects"))
	lines = append(lines, attnLines...)
	return strings.Join(lines, "\n")
}

// attentionLines is the "needs you" list, windowed around its cursor. Each
// row leads with its project, which is the point of the list.
func (p *projectsView) attentionLines(attn []apiclient.Task, width int) []string {
	if len(attn) == 0 {
		return []string{styleDim.Render("  nothing needs you in any project")}
	}
	out := make([]string, 0, len(attn))
	for i, t := range attn {
		marker := "  "
		if p.inAttention && i == p.attnCursor {
			marker = styleFocus.Render("› ")
		}
		line := fmt.Sprintf("%s%s  #%-4d %s  %s", marker,
			styleTitle.Render(t.ProjectName), t.ID, renderBoardState(t), t.Title)
		out = append(out, ansi.Truncate(line, max(width, 1), "…"))
	}
	cursor := 0
	if p.inAttention {
		cursor = p.attnCursor
	}
	return window(out, cursor, overviewAttentionMax)
}

func (p *projectsView) syncTable(rows []apiclient.Project, width, height int) {
	cols, name := overviewColumns(width)
	if len(cols) != len(p.tbl.Columns()) {
		// Crossing a breakpoint: clear the rows first, or the table
		// re-renders the previous shape against the new column set.
		p.tbl.SetRows(nil)
	}
	p.tbl.SetColumns(overviewHeader(cols, name))
	p.tbl.SetRows(p.rowsFor(rows, cols))
	p.tbl.SetWidth(width)
	p.tbl.SetHeight(height)
	p.restoreSelection(rows)
	// Passing through an empty row set parks the cursor at -1; with rows on
	// screen it belongs on one, or nothing is selected and every key that
	// acts on a row goes nowhere.
	if p.tbl.Cursor() < 0 && len(rows) > 0 {
		p.tbl.SetCursor(0)
	}
	p.overviewCols, p.overviewName = cols, name
}

// renderGuided is the add/edit form beside the project list.
func (p *projectsView) renderGuided(rows []apiclient.Project) string {
	rail := p.renderProjectRail(rows, p.height-2)
	return guidedSurface(p.width, p.height,
		fmt.Sprintf("Projects · %d", len(rows)), rail,
		p.form.heading(), p.form.renderFocused(p.width/2, p.height-2))
}

func (p *projectsView) renderProjectRail(rows []apiclient.Project, height int) string {
	lines := []string{styleDim.Render("  Registered repositories"), ""}
	if p.filter.Value() != "" {
		lines = append(lines, styleDim.Render("  Filter: "+p.filter.Value()), "")
	}
	if len(rows) == 0 {
		lines = append(lines, styleDim.Render("  No matching projects"))
		return strings.Join(window(lines, 0, height), "\n")
	}
	from, to := 0, 1
	for i, pr := range rows {
		start := len(lines)
		marker := "  "
		name := pr.Name
		if i == p.tbl.Cursor() {
			marker = styleFocus.Render("› ")
			name = styleTitle.Render(name)
			from = start
		}
		lines = append(lines,
			marker+name,
			styleDim.Render("    "+p.projectRailSummary(pr)),
			"")
		if i == p.tbl.Cursor() {
			to = len(lines)
		}
	}
	return strings.Join(windowRange(lines, from, to, height), "\n")
}

// projectRailSummary pairs a project's slot count with the cap that count is
// measured against, so both numbers come from the daemon's §11 arithmetic
// (see capCell) rather than from the root-only task list.
func (p *projectsView) projectRailSummary(pr apiclient.Project) string {
	running := pr.SlotsUsed
	if pr.MaxParallelTasks != nil {
		return fmt.Sprintf("%d running · cap %d", running, *pr.MaxParallelTasks)
	}
	if p.infoOK {
		return fmt.Sprintf("%d running · global %d", running, p.globalCap)
	}
	return fmt.Sprintf("%d running · no project cap", running)
}

// renderProjectDetail is the wide terminal's detail pane: the highlighted
// project's repository and execution defaults, which the table has no room
// for (task 132 decision 35). The old focus pane's client-filtered workload
// is gone — the row's figures and the "needs you" list replace it.
func (p *projectsView) renderProjectDetail(pr apiclient.Project, height int) string {
	lines := []string{
		"  " + styleTitle.Render(pr.Name),
		"  " + styleDim.Render(pr.Path),
		"",
		section("Repository"),
		p.projectFact("default branch", pr.DefaultBranch),
		p.projectFact("branch naming", projectBranchTemplate(pr)),
		"",
		section("Execution defaults"),
		p.projectFact("workflow", pr.Workflow()),
		p.projectFact("project cap", projectCap(pr)),
		p.projectFact("global cap", p.globalCapLabel()),
		p.projectFact("running now", strconv.Itoa(pr.SlotsUsed)),
		p.projectFact("running / cap", p.capCell(pr)),
		"",
		styleDim.Render("  enter select · e edit · " + opKey(keymap.Delete) + " remove"),
	}
	return strings.Join(window(lines, 0, height), "\n")
}

func (p *projectsView) projectFact(label, value string) string {
	return "  " + styleDim.Render(fmt.Sprintf("%-15s", label)) + " " + value
}

func projectBranchTemplate(pr apiclient.Project) string {
	if pr.BranchTemplate != nil && *pr.BranchTemplate != "" {
		return *pr.BranchTemplate
	}
	return styleDim.Render("inherits config.yaml")
}

func projectCap(pr apiclient.Project) string {
	if pr.MaxParallelTasks == nil {
		return styleDim.Render("none")
	}
	return strconv.Itoa(*pr.MaxParallelTasks)
}

func (p *projectsView) globalCapLabel() string {
	if !p.infoOK {
		return styleDim.Render("unavailable")
	}
	return strconv.Itoa(p.globalCap)
}

// ovCol is one of the overview table's columns, in display order.
type ovCol int

const (
	ocName ovCol = iota
	ocAttention
	ocRunning
	ocQueued
	ocBlocked
	ocDone
	ocIssues
	ocChats
	ocSync
	ocGitHub
	ocActivity
	ocCount
)

// ovColSpec is each column's header and width. As on the board, the table
// pads every cell by one space either side, so a column occupies its width
// plus colPadding (T3.8 finding).
var ovColSpec = [ocCount]struct {
	title string
	width int
}{
	ocName:      {"name", pcolName},
	ocAttention: {"!", 3},
	ocRunning:   {"running", 7},
	ocQueued:    {"queued", 6},
	ocBlocked:   {"blocked", 7},
	ocDone:      {"done", 5},
	ocIssues:    {"issues (gh)", 11},
	ocChats:     {"chats (wait)", 12},
	ocSync:      {"sync", 4},
	ocGitHub:    {"github", 18},
	ocActivity:  {"activity", 10},
}

// ovShedOrder is the order a narrowing terminal loses columns in, after the
// detail pane and after the name has been squeezed to its floor: the
// context first, then the settled figures, so the name, attention and
// running — what the overview is for — are the last three standing.
var ovShedOrder = []ovCol{ocActivity, ocGitHub, ocSync, ocChats, ocIssues, ocDone, ocBlocked, ocQueued}

const (
	pcolName = 20
	// pcolMinName is where squeezing the name stops and a whole column goes
	// instead; pcolMaxName is where a wide terminal stops widening it.
	pcolMinName = 12
	pcolMaxName = 32
)

// overviewFullWidth is every column at its natural width: the width below
// which the detail pane goes first.
func overviewFullWidth() int {
	w := 0
	for c := range ocCount {
		w += ovColSpec[c].width + colPadding
	}
	return w
}

// overviewColumns fits the overview's columns into width: the name squeezes
// to its floor first, then columns go in ovShedOrder, and slack widens the
// name up to pcolMaxName.
func overviewColumns(width int) (cols []ovCol, name int) {
	keep := [ocCount]bool{}
	for c := range ocCount {
		keep[c] = true
	}
	name = pcolName
	cost := func() int {
		c := 0
		for col := range ocCount {
			if !keep[col] {
				continue
			}
			w := ovColSpec[col].width
			if col == ocName {
				w = name
			}
			c += w + colPadding
		}
		return c
	}
	shed := 0
	for cost() > width {
		switch {
		case name > pcolMinName:
			name = max(pcolMinName, name-(cost()-width))
		case shed < len(ovShedOrder):
			keep[ovShedOrder[shed]] = false
			shed++
		default:
			name = max(pcolMinName, name) // nothing left; the table truncates
			return keptCols(keep), name
		}
	}
	if slack := width - cost(); slack > 0 {
		name = min(name+slack, pcolMaxName)
	}
	return keptCols(keep), name
}

func keptCols(keep [ocCount]bool) []ovCol {
	out := make([]ovCol, 0, ocCount)
	for c := range ocCount {
		if keep[c] {
			out = append(out, c)
		}
	}
	return out
}

func overviewHeader(cols []ovCol, name int) []table.Column {
	out := make([]table.Column, 0, len(cols))
	for _, c := range cols {
		w := ovColSpec[c].width
		if c == ocName {
			w = name
		}
		out = append(out, table.Column{Title: ovColSpec[c].title, Width: w})
	}
	return out
}

func (p *projectsView) rowsFor(projects []apiclient.Project, cols []ovCol) []table.Row {
	out := make([]table.Row, 0, len(projects))
	for _, pr := range projects {
		row := make(table.Row, 0, len(cols))
		for _, c := range cols {
			row = append(row, p.overviewCell(pr, c))
		}
		out = append(out, row)
	}
	return out
}

// overviewDash is a figure the daemon could not count: a row whose stats are
// null reads as unknown, never as zero.
const overviewDash = "—"

// overviewCell is one figure of one row. Every figure is the daemon's.
func (p *projectsView) overviewCell(pr apiclient.Project, c ovCol) string {
	switch c {
	case ocName:
		return pr.Name
	case ocRunning:
		return runningFigure(pr)
	case ocGitHub:
		return p.githubCell(pr.ID)
	case ocCount:
		return ""
	}
	st := pr.Stats
	if st == nil {
		return overviewDash
	}
	switch c {
	case ocAttention:
		return strconv.Itoa(st.Tasks.Attention)
	case ocQueued:
		return strconv.Itoa(st.Tasks.ByState[stateQueued])
	case ocBlocked:
		return strconv.Itoa(st.Tasks.ByState[stateBlocked])
	case ocDone:
		return strconv.Itoa(st.Tasks.ByState[stateDone])
	case ocIssues:
		return fmt.Sprintf("%d (%d)", st.Issues.Open, st.Issues.OpenImported)
	case ocChats:
		return fmt.Sprintf("%d (%d)", st.Chats.Live, st.Chats.AwaitingInput)
	case ocSync:
		return syncGlyph(st)
	case ocActivity:
		return p.agoCell(st.LastActivityAt)
	case ocName, ocRunning, ocGitHub, ocCount:
	}
	return ""
}

// runningFigure is slots used against the project's own cap; an uncapped
// project reads `N` with no denominator, because the global cap is not a
// per-project limit (task 132 decision 33).
func runningFigure(pr apiclient.Project) string {
	if pr.MaxParallelTasks != nil {
		return fmt.Sprintf("%d/%d", pr.SlotsUsed, *pr.MaxParallelTasks)
	}
	return strconv.Itoa(pr.SlotsUsed)
}

// notGitHub is the reason the daemon gives — on the §13.2 probe and on the
// stored sync row alike — for a project whose origin is not on github.com.
// That is the absence of an integration, not a failing one, so it reads as a
// dash rather than as ✗ or a reason.
const notGitHub = "not_github"

// syncGlyph is the stored issue-sync health: off, healthy, or failing.
func syncGlyph(st *apiclient.ProjectStats) string {
	switch {
	case !st.IssueSync.Enabled, st.IssueSync.Reason == notGitHub:
		return overviewDash
	case st.IssueSync.OK:
		return "✓"
	default:
		return "✗"
	}
}

// githubCell is the root's §13.2 probe for the project (task 132 decision
// 36): the repository when usable, the probe's reason when not, and a dash
// when the project has no GitHub remote or no answer has arrived.
func (p *projectsView) githubCell(id int64) string {
	st, ok := p.github[id]
	switch {
	case !ok || !st.Enabled, st.Reason == notGitHub:
		return overviewDash
	case st.Available:
		return "✓ " + st.Repo
	case st.Reason != "":
		return st.Reason
	default:
		return overviewDash
	}
}

func (p *projectsView) agoCell(at *time.Time) string {
	if at == nil {
		return overviewDash
	}
	return formatElapsed(max(p.now().Sub(*at), 0).Truncate(time.Second)) + " ago"
}

// totalsLine sums the columns under the table. The sums are exact — every
// figure is partitioned by project — except running, which is /v1/info's
// installation-wide `slots.used / max_parallel_tasks` and never a sum of the
// rows (#324's rule, §13.2). A row with null stats adds nothing.
func (p *projectsView) totalsLine() string {
	cells := make([]string, 0, len(p.overviewCols))
	for _, c := range p.overviewCols {
		w := ovColSpec[c].width
		if c == ocName {
			w = p.overviewName
		}
		cells = append(cells, " "+padCell(p.totalCell(c), w)+" ")
	}
	return styleTitle.Render(strings.Join(cells, ""))
}

func (p *projectsView) totalCell(c ovCol) string {
	if c == ocName {
		return "total"
	}
	if c == ocRunning {
		if !p.infoOK {
			return overviewDash
		}
		return fmt.Sprintf("%d/%d", p.slotsUsed, p.globalCap)
	}
	var a, b int
	var newest *time.Time
	counted := false
	for _, pr := range p.visible() {
		st := pr.Stats
		if st == nil {
			continue
		}
		counted = true
		switch c {
		case ocAttention:
			a += st.Tasks.Attention
		case ocQueued:
			a += st.Tasks.ByState[stateQueued]
		case ocBlocked:
			a += st.Tasks.ByState[stateBlocked]
		case ocDone:
			a += st.Tasks.ByState[stateDone]
		case ocIssues:
			a, b = a+st.Issues.Open, b+st.Issues.OpenImported
		case ocChats:
			a, b = a+st.Chats.Live, b+st.Chats.AwaitingInput
		case ocActivity:
			if at := st.LastActivityAt; at != nil && (newest == nil || at.After(*newest)) {
				newest = at
			}
		case ocName, ocRunning, ocSync, ocGitHub, ocCount:
		}
	}
	switch c {
	case ocSync, ocGitHub:
		return ""
	case ocActivity:
		return p.agoCell(newest)
	case ocIssues, ocChats:
		if !counted {
			return overviewDash
		}
		return fmt.Sprintf("%d (%d)", a, b)
	case ocName, ocRunning, ocAttention, ocQueued, ocBlocked, ocDone, ocCount:
	}
	if !counted {
		return overviewDash
	}
	return strconv.Itoa(a)
}

// padCell fits s to exactly w cells, the way the table renders its own.
func padCell(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}

// statusLines carries the three things that can be true above the table at
// once: a stale list, a confirmation waiting on a keypress, and an error the
// daemon returned that no confirmation can resolve.
func (p *projectsView) statusLines() []string {
	var out []string
	if p.filtering || p.filter.Value() != "" {
		p.filter.SetWidth(max(p.width-1, 10))
		out = append(out, fieldRows(" ", p.filter)...)
	}
	if p.loadErr != nil {
		note := " ⚠ refresh failed: " + errString(p.loadErr)
		if !p.lastLoad.IsZero() {
			note += " — showing " + p.lastLoad.Local().Format("15:04:05")
		}
		out = append(out, styleBad.Render(note))
	}
	if p.err != "" {
		out = append(out, styleBad.Render(" ⚠ "+p.err))
	}
	if p.confirm != nil {
		out = append(out, styleWarn.Render(" "+p.confirm.text+" ")+
			styleKey.Render("y")+styleDim.Render("/")+styleKey.Render("n"))
	}
	return out
}

func (p *projectsView) emptyBody(rows []apiclient.Project) (string, bool) {
	if len(rows) > 0 {
		return "", false
	}
	switch {
	case !p.loaded && p.loadErr == nil:
		return styleDim.Render("\n  loading projects…\n"), true
	case len(p.projects) > 0:
		return styleDim.Render(fmt.Sprintf(
			"\n  no projects match %q — esc to clear the filter\n", p.filter.Value())), true
	default:
		return styleDim.Render("\n  no projects registered — press " + opKey(keymap.Add) +
			" to add a repository, or run `vincent project add <path>`\n"), true
	}
}

func (f *projectForm) heading() string {
	if f.adding() {
		return "Add project"
	}
	return "Edit project"
}

func (f *projectForm) render(width int) string {
	lines, _ := f.renderLines(width, true)
	return strings.Join(lines, "\n")
}

func (f *projectForm) renderFocused(width, height int) string {
	lines, cursorLine := f.renderLines(width, false)
	return strings.Join(window(lines, cursorLine, height), "\n")
}

func (f *projectForm) renderLines(width int, includeHeading bool) ([]string, int) {
	// pfRowIndent is the two-space gutter, the marker and the padded label
	// every row carries, and so what a text row has left of the pane (#299).
	const pfRowIndent = 2 + 2 + 20
	w := max(width-pfRowIndent, 10)
	f.path.SetWidth(w)
	f.name.SetWidth(w)
	f.branch.SetWidth(w)
	f.cap.SetWidth(w)
	var lines []string
	if includeHeading {
		lines = append(lines, " "+styleTitle.Render(strings.ToLower(f.heading())), "")
	}
	cursorLine := len(lines)
	for row := pfRow(0); row < pfRowCount; row++ {
		if row == f.cursor {
			cursorLine = len(lines)
		}
		lines = append(lines, strings.Split(f.renderRow(row), "\n")...)
		if msg, ok := f.rowErr[row]; ok {
			lines = append(lines, styleBad.Render("      ⚠ "+msg))
		}
		if row == pfWorkflow && f.pick != nil {
			f.pick.setWidth(width)
			lines = append(lines, f.pick.renderBody()...)
			lines = append(lines, styleDim.Render("    enter select · esc cancel"))
		}
	}
	if f.err != "" {
		lines = append(lines, "", styleBad.Render("  ⚠ "+f.err))
	}
	if f.saving {
		lines = append(lines, "", styleDim.Render("  saving…"))
	}
	lines = append(lines, "", styleDim.Render(
		"  enter edit the row · ctrl+s save · esc close"))
	if f.adding() {
		lines = append(lines, styleDim.Render(
			"  only the repository is required; the daemon names the project and detects the branch"))
	} else {
		lines = append(lines, styleDim.Render(
			"  an empty cap means no project cap — the daemon-wide limit still applies"))
	}
	return lines, cursorLine
}

func (f *projectForm) renderRow(row pfRow) string {
	marker := "  "
	if row == f.cursor {
		marker = styleFocus.Render("▸ ")
	}
	label := styleDim.Render(fmt.Sprintf("%-20s", row.label()))
	if row == pfSave {
		return "  " + marker + styleTitle.Render("save")
	}
	return strings.Join(indentRows("  "+marker+label, strings.Split(f.rowValue(row), "\n")), "\n")
}

func (f *projectForm) rowValue(row pfRow) string {
	editing := f.editing && f.cursor == row
	switch row {
	case pfPath:
		return textOrView(f.path.Value(), f.path.View(), editing, "(required)")
	case pfName:
		return textOrView(f.name.Value(), f.name.View(), editing, "(the directory name)")
	case pfBranch:
		return textOrView(f.branch.Value(), f.branch.View(), editing, "(detected from the repository)")
	case pfCap:
		return textOrView(f.cap.Value(), f.cap.View(), editing, "(no project cap)")
	case pfWorkflow:
		if f.workflow == "" {
			return styleDim.Render("(none — new tasks use adhoc)")
		}
		return f.workflow
	case pfSave, pfRowCount:
	}
	return ""
}

// textOrView shows the live input while a row is being typed into and the
// committed value otherwise, so an untouched row reads as its placeholder
// rather than as an empty box.
func textOrView(value, view string, editing bool, placeholder string) string {
	if editing {
		return view
	}
	if strings.TrimSpace(value) == "" {
		return styleDim.Render(placeholder)
	}
	return value
}
