package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The editor's rendering. It draws rows, never YAML: the whole argument for a
// structured editor is that the schema is on screen instead of in the
// author's head (task 065).

// renderEditor draws the open form: a breadcrumb, the rows, and the footer of
// keys the binding table already declares.
func (w *workflowsView) renderEditor(width, height int) string {
	e := w.editor
	if width <= 0 {
		width = w.width
	}
	out := []string{
		styleTitle.Render("  " + e.key.name),
		"  " + styleDim.Render(e.scope+" · "+e.file),
	}
	if e.path != "" {
		out = append(out, "  "+styleDim.Render("in "+e.path+" — esc goes back up"))
	}
	out = append(out, "")
	switch {
	case e.loading && e.def == nil:
		out = append(out, styleDim.Render("  loading the schema and the definition…"))
	case e.def == nil:
		out = append(out, styleDim.Render("  nothing to edit"))
	default:
		out = append(out, w.renderEditorRows(width)...)
	}
	if e.saving {
		out = append(out, "", styleDim.Render("  saving…"))
	}
	if e.err != "" {
		// The refused value is still on its row above; the error names the
		// field it belongs to, which is what makes it actionable.
		out = append(out, "", "  "+styleBad.Render(e.err))
	}
	out = append(out, "", styleDim.Render(
		"  enter edit · R reload · esc back · e opens the file in $EDITOR from the list"))
	return clampLines(out, height)
}

func (w *workflowsView) renderEditorRows(width int) []string {
	e := w.editor
	rows := make([]string, 0, len(e.rows))
	for i, row := range e.rows {
		label := row.label
		if label == "" {
			label = row.field.Name
		}
		mark := "  "
		style := styleDim
		if i == e.cursor {
			mark = styleFocus.Render("› ")
			style = styleTitle
		}
		value := row.value
		if value == "" {
			value = unsetMarker
		}
		if e.input != nil && i == e.editing {
			value = e.input.View()
		}
		if row.descend != "" {
			value += "  →"
		}
		line := mark + style.Render(fmt.Sprintf("%-18s", label)) + " " + value
		if row.field.Required && row.value == "" {
			line += " " + styleBad.Render("required")
		}
		if width > 0 {
			line = ansi.Truncate(line, width, "…")
		}
		rows = append(rows, line)
		if i == e.cursor && row.field.Help != "" {
			rows = append(rows, "    "+styleDim.Render(row.field.Help))
		}
	}
	return rows
}

// renderCreate draws the create/fork prompt: a scope row and a file-name row.
func (w *workflowsView) renderCreate(_, height int) string {
	f := w.create
	title := "New workflow"
	if f.fork {
		title = "Fork " + f.source
	}
	scope := make([]string, 0, len(f.scopes))
	for i, s := range f.scopes {
		label := s.label
		if i == f.scope {
			label = styleFocus.Render("[" + label + "]")
		} else {
			label = styleDim.Render(" " + label + " ")
		}
		scope = append(scope, label)
	}
	rowMark := func(row int) string {
		if f.row == row {
			return styleFocus.Render("› ")
		}
		return "  "
	}
	out := []string{
		styleTitle.Render("  " + title),
		"",
		rowMark(wfCreateRowScope) + styleDim.Render(fmt.Sprintf("%-10s", "scope")) + " " + strings.Join(scope, " "),
		rowMark(wfCreateRowName) + styleDim.Render(fmt.Sprintf("%-10s", "file name")) + " " + f.name.View(),
	}
	if f.fork {
		out = append(out, "", styleDim.Render(
			"  the copy keeps "+f.source+"'s own name:, which is what makes it shadow the original"))
	}
	if f.saving {
		out = append(out, "", styleDim.Render("  writing…"))
	}
	if f.err != "" {
		out = append(out, "", "  "+styleBad.Render(f.err))
	}
	out = append(out, "", styleDim.Render("  tab row · ←→ scope · enter create · esc cancel"))
	return clampLines(out, height)
}

// clampLines cuts a rendered panel to the height it was given.
func clampLines(lines []string, height int) string {
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}
