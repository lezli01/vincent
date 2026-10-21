package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

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
	head := []string{
		styleTitle.Render("  " + e.key.name),
		"  " + styleDim.Render(e.scope+" · "+e.file),
	}
	if e.scope == "global" {
		head = append(head, "  "+styleWarn.Render("global — editing it affects every project"))
	}
	if e.path != "" {
		head = append(head, "  "+styleDim.Render("in "+e.path+" — esc goes back up"))
	}
	head = append(head, "")
	var foot []string
	if e.saving {
		foot = append(foot, "", styleDim.Render("  saving…"))
	}
	if e.err != "" {
		// The refused value is still on its row above; the error names the
		// field it belongs to, which is what makes it actionable.
		foot = append(foot, "")
		for _, l := range wrapText(e.err, max(width-2, 1), wrapOpts{}) {
			foot = append(foot, "  "+styleBad.Render(l))
		}
	}
	// The hint names the keys that are live *now*. An overlay owns every key
	// while it is open and draws its own line — `d` means "drop this mapping
	// key" inside the `env:` sub-form and "remove this step" outside it — and
	// the one-line field's `R` is a letter being typed, not a reload. A hint
	// that names a key which does nothing is worse than one that names none.
	switch {
	case e.overlay != nil:
	case e.input != nil:
		foot = append(foot, "", styleDim.Render("  enter commit · esc cancel"))
	default:
		foot = append(foot, "", styleDim.Render(
			"  enter edit · "+opKey(keymap.Add)+" add · "+opKey(keymap.DraftRemove)+" remove · K/J move · "+
				opKey(keymap.Refresh)+" reload · esc back · "+opKey(keymap.Editor)+" $EDITOR from the list"))
	}
	foot = truncateRows(foot, max(width, 1))
	var body []string
	from, to := 0, 0
	switch {
	case e.overlay != nil && e.overlay.FullPane():
		// A full-pane overlay is the body: the multi-line pane a `prompt:`
		// is edited in has no room to share with the rows behind it. The
		// breadcrumb and the key footer stay, so it is still one layer of
		// the same form (issue #320's write half). It gets the height that
		// is left, and every line of it is drawn (issue #802).
		body = strings.Split(e.overlay.View(width, paneRoom(height, len(head)+len(foot))), "\n")
	case e.loading && e.def == nil:
		body = []string{styleDim.Render("  loading the schema and the definition…")}
	case e.def == nil:
		body = []string{styleDim.Render("  nothing to edit")}
	default:
		body, from, to = layoutFormRows(e.rows, e.cursor, e.editing, e.input, e.overlay, width, nil)
	}
	return formBody(head, body, from, to, foot, height)
}

// renderFormRows draws a schema-driven form's rows: the label column, the
// value (or the field or overlay editing it), the descend arrow, the required
// marker, and the help of the row under the cursor. It is shared by the
// workflow editor and the triggers form (task 096.6), which build their rows
// from two different served descriptors and draw them one way. note, when set,
// returns an annotation for row i — the triggers form's refusal on the field
// that caused it — drawn on its own line under the value.
func renderFormRows(formRows []wfEditRow, cursor, editing int, input *textField,
	overlay wfEditorOverlay, width int, note func(i int) string,
) []string {
	lines, _, _ := layoutFormRows(formRows, cursor, editing, input, overlay, width, note)
	return lines
}

// formLabelMax caps the label column, so one long label cannot push every
// value off to the right (issue #802 WF-10).
const formLabelMax = 24

// layoutFormRows is renderFormRows with the cursor row's line span
// [from,to), which is what a host windows the rows around (spec §15 "Wrap or
// truncate, never clip", issue #802). The cursor row's value and help wrap
// under the value column; every other row stays one line ending in "…".
func layoutFormRows(formRows []wfEditRow, cursor, editing int, input *textField,
	overlay wfEditorOverlay, width int, note func(i int) string,
) (lines []string, from, to int) {
	labelW := 1
	for _, row := range formRows {
		labelW = max(labelW, cols(formLabel(row)))
	}
	labelW = min(labelW, formLabelMax)
	if width > 0 {
		labelW = min(labelW, max(width/3, 1))
	}
	// The value column starts after the mark, the label and one space.
	valueAt := 2 + labelW + 1
	valueW := max(width-valueAt, 1)
	pad := strings.Repeat(" ", valueAt)
	fit := func(l string) string {
		if width > 0 {
			return ansi.Truncate(l, width, "…")
		}
		return l
	}
	lines = make([]string, 0, len(formRows))
	for i, row := range formRows {
		mark := "  "
		style := styleDim
		if i == cursor {
			mark = styleFocus.Render("› ")
			style = styleTitle
			from = len(lines)
		}
		label := ansi.Truncate(formLabel(row), labelW, "…")
		prefix := mark + style.Render(label+strings.Repeat(" ", labelW-cols(label))) + " "
		var tail string
		if row.descend != "" {
			tail += "  →"
		}
		if row.field.Required && row.value == "" {
			tail += " " + styleBad.Render("required")
		}
		value := row.value
		if value == "" {
			value = unsetMarker
		}
		switch {
		case i == editing && input != nil:
			input.SetWidth(valueW)
			lines = append(lines, indentRows(prefix, input.rows())...)
		case i == editing && overlay != nil:
			lines = append(lines, truncateRows(indentRows(prefix,
				strings.Split(overlay.View(valueW, 0), "\n")), max(width, 1))...)
		case i == cursor && width > 0:
			wrapped := wrapText(value, valueW, wrapOpts{})
			last := len(wrapped) - 1
			if cols(wrapped[last])+cols(tail) <= valueW {
				wrapped[last] += tail
			} else if tail != "" {
				wrapped = append(wrapped, strings.TrimLeft(tail, " "))
			}
			lines = append(lines, indentRows(prefix, wrapped)...)
		default:
			first, rest, _ := strings.Cut(value, "\n")
			if rest != "" {
				first += " …"
			}
			if width > 0 {
				first = ansi.Truncate(first, max(valueW-cols(tail), 1), "…")
			}
			lines = append(lines, fit(prefix+first+tail))
		}
		if note != nil {
			if n := note(i); n != "" {
				lines = append(lines, fit(pad+n))
			}
		}
		if i == cursor && row.field.Help != "" {
			help := []string{row.field.Help}
			if width > 0 {
				help = wrapText(row.field.Help, valueW, wrapOpts{})
			}
			for _, h := range help {
				lines = append(lines, pad+styleDim.Render(h))
			}
		}
		if i == cursor {
			to = len(lines)
		}
	}
	return lines, from, to
}

// formLabel is the text of a form row's label column.
func formLabel(row wfEditRow) string {
	if row.label != "" {
		return row.label
	}
	return row.field.Name
}

// paneRoom is the height left for a full-pane overlay under used lines of
// head and foot, zero (no limit) when the pane's own height is unknown.
func paneRoom(height, used int) int {
	if height <= 0 {
		return 0
	}
	return max(height-used, 1)
}

// formBody lays a form's sections into height lines: the head and the foot
// pinned, and the body between them windowed around the cursor's [from,to)
// span (issue #802 WF-6), so a long form never pushes its error, its key hint
// or the row being edited off the bottom. A head and foot that leave the body
// no room keep the foot, which says what the keys do.
func formBody(head, body []string, from, to int, foot []string, height int) string {
	if height <= 0 {
		return strings.Join(append(append(head, body...), foot...), "\n")
	}
	room := height - len(head) - len(foot)
	if room < 1 {
		head = head[:max(min(len(head), height-len(foot)-1), 0)]
		room = max(height-len(head)-len(foot), 0)
	}
	out := make([]string, 0, height)
	out = append(out, head...)
	if room > 0 {
		out = append(out, windowRange(body, from, to, room)...)
	}
	out = append(out, foot...)
	return clampLines(out, height)
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
	if f.scopes[f.scope].scope == "global" {
		out = append(out, "", "  "+styleWarn.Render("global — a workflow written here affects every project"))
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
