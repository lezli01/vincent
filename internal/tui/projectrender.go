package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/table"

	"github.com/lezli01/vincent/internal/apiclient"
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
		if guidedTakeover(p.width, p.height) {
			return p.renderGuided(rows)
		}
		return p.form.render(p.width)
	}
	p.syncTable(rows)
	if guidedTakeover(p.width, p.height) {
		return p.renderGuided(rows)
	}
	return p.renderCompact(rows)
}

func (p *projectsView) renderCompact(rows []apiclient.Project) string {
	var sb strings.Builder
	for _, line := range p.statusLines() {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	if body, ok := p.emptyBody(rows); ok {
		sb.WriteString(body)
		return sb.String()
	}
	sb.WriteString(p.tbl.View())
	return sb.String()
}

func (p *projectsView) syncTable(rows []apiclient.Project) {
	cols, set := projectColumns(p.width)
	if len(cols) != len(p.tbl.Columns()) {
		// Crossing a breakpoint: clear the rows first, or the table
		// re-renders the previous shape against the new column set.
		p.tbl.SetRows(nil)
	}
	p.tbl.SetColumns(cols)
	p.tbl.SetRows(p.rowsFor(rows, set))
	p.tbl.SetWidth(p.width)
	p.tbl.SetHeight(max(3, p.height-len(p.statusLines())-3))
	p.restoreSelection(rows)
	// Passing through an empty row set parks the cursor at -1; with rows on
	// screen it belongs on one, or nothing is selected and every key that
	// acts on a row goes nowhere.
	if p.tbl.Cursor() < 0 && len(rows) > 0 {
		p.tbl.SetCursor(0)
	}
}

func (p *projectsView) renderGuided(rows []apiclient.Project) string {
	rail := p.renderProjectRail(rows, p.height-2)
	mainTitle := "Overview"
	var main string
	if p.form != nil {
		mainTitle = p.form.heading()
		main = p.form.renderFocused(p.width/2, p.height-2)
	} else if pr, ok := p.current(); ok {
		mainTitle = "Overview · " + pr.Name
		main = p.renderProjectOverview(pr, p.height-2)
	} else if body, ok := p.emptyBody(rows); ok {
		main = strings.Join(append(p.statusLines(), body), "\n")
	}
	return guidedSurface(p.width, p.height,
		fmt.Sprintf("Projects · %d", len(rows)), rail, mainTitle, main)
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

func (p *projectsView) projectRailSummary(pr apiclient.Project) string {
	running := p.runningIn(pr.ID)
	if pr.MaxParallelTasks != nil {
		return fmt.Sprintf("%d running · cap %d", running, *pr.MaxParallelTasks)
	}
	if p.infoOK {
		return fmt.Sprintf("%d running · global %d", running, p.globalCap)
	}
	return fmt.Sprintf("%d running · no project cap", running)
}

func (p *projectsView) renderProjectOverview(pr apiclient.Project, height int) string {
	lines := append([]string{}, p.statusLines()...)
	lines = append(lines,
		"  "+styleTitle.Render(pr.Name),
		"  "+styleDim.Render(pr.Path),
		"",
		section("Repository"),
		p.projectFact("path", pr.Path),
		p.projectFact("default branch", pr.DefaultBranch),
		p.projectFact("branch naming", projectBranchTemplate(pr)),
		"",
		section("Execution defaults"),
		p.projectFact("workflow", pr.Workflow()),
		p.projectFact("project cap", projectCap(pr)),
		p.projectFact("global cap", p.globalCapLabel()),
		p.projectFact("running now", strconv.Itoa(p.runningIn(pr.ID))),
		"",
		section("Current workload"),
	)
	tasks := p.tasksFor(pr.ID)
	if len(tasks) == 0 {
		lines = append(lines, styleDim.Render("  No current tasks for this project."))
	} else {
		available := max(height-len(lines)-1, 1)
		shown := min(len(tasks), available)
		if shown < len(tasks) {
			shown = max(shown-1, 0)
		}
		for _, task := range tasks[:shown] {
			lines = append(lines, fmt.Sprintf("  #%-4d %-20s %s",
				task.ID, renderState(task.State), task.Title))
		}
		if shown < len(tasks) {
			lines = append(lines, styleDim.Render(fmt.Sprintf(
				"  … %d more on the board", len(tasks)-shown)))
		}
	}
	lines = append(lines, styleDim.Render("  enter/e edit · a add · d remove · n new task"))
	return strings.Join(window(lines, 0, height), "\n")
}

func (p *projectsView) projectFact(label, value string) string {
	return "  " + styleDim.Render(fmt.Sprintf("%-17s", label)) + " " + value
}

func projectBranchTemplate(pr apiclient.Project) string {
	if pr.BranchTemplate != nil && *pr.BranchTemplate != "" {
		return *pr.BranchTemplate
	}
	return styleDim.Render("inherits config.yaml")
}

func projectCap(pr apiclient.Project) string {
	if pr.MaxParallelTasks == nil {
		return styleDim.Render("none — global cap still applies")
	}
	return strconv.Itoa(*pr.MaxParallelTasks)
}

func (p *projectsView) globalCapLabel() string {
	if !p.infoOK {
		return styleDim.Render("unavailable")
	}
	return strconv.Itoa(p.globalCap)
}

func (p *projectsView) tasksFor(projectID int64) []apiclient.Task {
	out := make([]apiclient.Task, 0, len(p.tasks))
	for _, task := range p.tasks {
		if task.ProjectID == projectID {
			out = append(out, task)
		}
	}
	return out
}

// Projects column widths. As on the board, the table pads every cell by one
// space either side, so a column occupies its width plus two — the original
// widths ignored that and overflowed by a whole column's padding, which the
// table swallowed by cutting the last column: "running / cap" arrived
// unreadable (T3.8 finding).
const (
	pcolID       = 4
	pcolName     = 20
	pcolBranch   = 14
	pcolWorkflow = 16
	pcolCap      = 20
	// pcolMinName is where squeezing the name stops and a whole column goes
	// instead; pcolMaxName is where a wide terminal stops widening it.
	pcolMinName = 14
	pcolMaxName = 40
	// pcolMinPath is not worth rendering below; pcolMaxPath is where a wide
	// terminal stops handing space to the one column that needs it least.
	pcolMinPath = 24
	pcolMaxPath = 60
)

// projectColSet records which optional columns survived the current width,
// so the row builder cannot disagree with the header about how many cells a
// row has — a mismatch is an index panic on the next resize.
type projectColSet struct {
	path     bool
	branch   bool
	workflow bool
}

// projectColumns fits §15's project columns into the width it has. The path
// is the first thing a narrow terminal loses — it is the longest column and
// the one a name already stands in for — and on a wide one it stops growing
// at pcolMaxPath rather than pushing the figures off the edge.
func projectColumns(width int) ([]table.Column, projectColSet) {
	// id, name and running/cap always exist: the identity, the thing you
	// scan for, and the figure the view is for.
	base := pcolID + pcolCap + 3*colPadding
	name := pcolName
	branch, workflow := true, true
	cost := func() int {
		c := base + name
		if branch {
			c += pcolBranch + colPadding
		}
		if workflow {
			c += pcolWorkflow + colPadding
		}
		return c
	}
	// Squeeze the name to its floor first — repo names are short — then shed
	// the configuration columns, which a narrow terminal can live without.
squeeze:
	for cost() > width {
		switch {
		case name > pcolMinName:
			name = max(pcolMinName, name-(cost()-width))
		case workflow:
			workflow = false
		case branch:
			branch = false
		default:
			break squeeze // nothing left to shed; the table will truncate
		}
	}

	path := 0
	if slack := width - cost(); slack >= pcolMinPath+colPadding {
		path = min(slack-colPadding, pcolMaxPath)
	} else if slack > 0 {
		// No room for a path column, so the space goes to the name rather
		// than sitting empty at the right edge.
		name = min(name+slack, pcolMaxName)
	}

	cols := []table.Column{
		{Title: "id", Width: pcolID},
		{Title: "name", Width: name},
	}
	if path > 0 {
		cols = append(cols, table.Column{Title: "path", Width: path})
	}
	if branch {
		cols = append(cols, table.Column{Title: "branch", Width: pcolBranch})
	}
	if workflow {
		cols = append(cols, table.Column{Title: "workflow", Width: pcolWorkflow})
	}
	return append(cols,
		table.Column{Title: "running / cap", Width: pcolCap},
	), projectColSet{path: path > 0, branch: branch, workflow: workflow}
}

func (p *projectsView) rowsFor(projects []apiclient.Project, set projectColSet) []table.Row {
	out := make([]table.Row, 0, len(projects))
	for _, pr := range projects {
		row := table.Row{strconv.FormatInt(pr.ID, 10), pr.Name}
		if set.path {
			row = append(row, pr.Path)
		}
		if set.branch {
			row = append(row, pr.DefaultBranch)
		}
		if set.workflow {
			workflow := styleDim.Render("adhoc")
			if pr.DefaultWorkflow != nil && *pr.DefaultWorkflow != "" {
				workflow = *pr.DefaultWorkflow
			}
			row = append(row, workflow)
		}
		out = append(out, append(row, p.capCell(pr)))
	}
	return out
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
		return styleDim.Render("\n  no projects registered — press a to add a repository\n"), true
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
