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
	if p.form != nil {
		return p.form.render()
	}

	var sb strings.Builder
	for _, line := range p.statusLines() {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	rows := p.visible()
	if body, ok := p.emptyBody(rows); ok {
		sb.WriteString(body)
		return sb.String()
	}

	p.tbl.SetColumns(projectColumns(p.width))
	p.tbl.SetRows(p.rowsFor(rows))
	p.tbl.SetWidth(p.width)
	p.tbl.SetHeight(max(3, p.height-len(p.statusLines())-3))
	p.restoreSelection(rows)
	sb.WriteString(p.tbl.View())
	return sb.String()
}

// projectColumns drops the path first on a narrow terminal: it is the
// longest column and the one a name already stands in for.
func projectColumns(width int) []table.Column {
	cols := []table.Column{
		{Title: "id", Width: 4},
		{Title: "name", Width: 18},
	}
	if width >= 90 {
		cols = append(cols, table.Column{Title: "path", Width: width - 76})
	}
	return append(cols,
		table.Column{Title: "branch", Width: 14},
		table.Column{Title: "workflow", Width: 16},
		table.Column{Title: "running / cap", Width: 20},
	)
}

func (p *projectsView) rowsFor(projects []apiclient.Project) []table.Row {
	out := make([]table.Row, 0, len(projects))
	for _, pr := range projects {
		row := table.Row{strconv.FormatInt(pr.ID, 10), pr.Name}
		if p.width >= 90 {
			row = append(row, pr.Path)
		}
		workflow := styleDim.Render("adhoc")
		if pr.DefaultWorkflow != nil && *pr.DefaultWorkflow != "" {
			workflow = *pr.DefaultWorkflow
		}
		out = append(out, append(row, pr.DefaultBranch, workflow, p.capCell(pr)))
	}
	return out
}

// statusLines carries the three things that can be true above the table at
// once: a stale list, a confirmation waiting on a keypress, and an error the
// daemon returned that no confirmation can resolve.
func (p *projectsView) statusLines() []string {
	var out []string
	if p.filtering || p.filter.Value() != "" {
		out = append(out, " "+p.filter.View())
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

func (f *projectForm) render() string {
	var lines []string
	heading := "edit project"
	if f.adding() {
		heading = "add a project"
	}
	lines = append(lines, " "+styleTitle.Render(heading), "")
	for row := pfRow(0); row < pfRowCount; row++ {
		lines = append(lines, f.renderRow(row))
		if msg, ok := f.rowErr[row]; ok {
			lines = append(lines, styleBad.Render("      ⚠ "+msg))
		}
		if row == pfWorkflow && f.pick != nil {
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
	return strings.Join(lines, "\n")
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
	return "  " + marker + label + f.rowValue(row)
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
