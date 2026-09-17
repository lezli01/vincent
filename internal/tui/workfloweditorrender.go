package tui

import (
	"fmt"
	"strings"

	"github.com/lezli01/vincent/internal/keymap"
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
	case e.overlay != nil && e.overlay.FullPane():
		// A full-pane overlay is the body: the multi-line pane a `prompt:`
		// is edited in has no room to share with the rows behind it. The
		// breadcrumb and the key footer stay, so it is still one layer of
		// the same form (issue #320's write half).
		out = append(out, e.overlay.View(width, height))
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
	// The hint names the keys that are live *now*. An overlay owns every key
	// while it is open and draws its own line — `d` means "drop this mapping
	// key" inside the `env:` sub-form and "remove this step" outside it — and
	// the one-line field's `R` is a letter being typed, not a reload. A hint
	// that names a key which does nothing is worse than one that names none.
	switch {
	case e.overlay != nil:
	case e.input != nil:
		out = append(out, "", styleDim.Render("  enter commit · esc cancel"))
	default:
		out = append(out, "", styleDim.Render(
			"  enter edit · "+opKey(keymap.Add)+" add · "+opKey(keymap.DraftRemove)+" remove · K/J move · "+
				opKey(keymap.Refresh)+" reload · esc back · "+opKey(keymap.Editor)+" $EDITOR from the list"))
	}
	return clampLines(out, height)
}

func (w *workflowsView) renderEditorRows(width int) []string {
	e := w.editor
	return renderFormRows(e.rows, e.cursor, e.editing, e.input, e.overlay, width, nil)
}

// renderFormRows draws a schema-driven form's rows: the label column, the
// value (or the field or overlay editing it), the descend arrow, the required
// marker, and the help of the row under the cursor. It is shared by the
// workflow editor and the triggers form (task 096.6), which build their rows
// from two different served descriptors and draw them one way. note, when set,
// returns a trailing annotation for row i — the triggers form's refusal on
// the field that caused it.
func renderFormRows(formRows []wfEditRow, cursor, editing int, input *textField,
	overlay wfEditorOverlay, width int, note func(i int) string,
) []string {
	rows := make([]string, 0, len(formRows))
	for i, row := range formRows {
		label := row.label
		if label == "" {
			label = row.field.Name
		}
		mark := "  "
		style := styleDim
		if i == cursor {
			mark = styleFocus.Render("› ")
			style = styleTitle
		}
		value := row.value
		if value == "" {
			value = unsetMarker
		}
		if i == editing {
			// 21 is the mark and the padded label the value sits after.
			switch {
			case input != nil:
				input.SetWidth(max(width-21, 10))
				value = input.View()
			case overlay != nil:
				value = overlay.View(max(width-21, 10), 0)
			}
		}
		if row.descend != "" {
			value += "  →"
		}
		line := mark + style.Render(fmt.Sprintf("%-18s", label)) + " " + value
		if row.field.Required && row.value == "" {
			line += " " + styleBad.Render("required")
		}
		if note != nil {
			if n := note(i); n != "" {
				line += "  " + n
			}
		}
		if width > 0 {
			line = strings.Join(truncateRows(strings.Split(line, "\n"), width), "\n")
		}
		rows = append(rows, strings.Split(line, "\n")...)
		if i == cursor && row.field.Help != "" {
			rows = append(rows, "    "+styleDim.Render(row.field.Help))
		}
	}
	return rows
}

// renderCreate draws the create/fork prompt: a scope row and a file-name row.
func (w *workflowsView) renderCreate(width, height int) string {
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
	}
	f.name.SetWidth(max(width-13, 10))
	out = append(out, indentRows(
		rowMark(wfCreateRowName)+styleDim.Render(fmt.Sprintf("%-10s", "file name"))+" ",
		f.name.rows())...)
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
