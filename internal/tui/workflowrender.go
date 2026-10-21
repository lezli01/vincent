package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/tui/workflowgraph"
)

func (w *workflowsView) render(width, height int) string {
	if width > 0 {
		w.width = width
	}
	if height > 0 {
		w.height = height
	}
	if w.create != nil {
		return w.renderCreate(width, height)
	}
	if w.editor != nil {
		return w.renderEditor(width, height)
	}
	if w.graph != nil {
		w.sizeGraph()
		var out string
		if guidedTakeover(w.width, w.height) {
			out = w.renderGuidedGraph()
		} else {
			out = w.renderGraph(width, height)
		}
		if w.graph.modal != nil {
			return w.overlayStepModal(out)
		}
		return out
	}
	if guidedTakeover(w.width, w.height) {
		return w.renderGuidedList()
	}
	return w.renderList(width, height)
}

func (w *workflowsView) renderGuidedList() string {
	lines := w.lines()
	rail := w.renderRegistryRail(lines, w.height-2)
	title := "Overview"
	var main string
	if line, ok := w.currentLine(); ok {
		title = "Overview · " + line.entry.Name
		_, mainWidth := guidedPaneWidths(w.width)
		main = w.renderWorkflowOverview(line, mainWidth-2, w.height-2)
	} else if body, ok := w.emptyBody(lines); ok {
		main = strings.Join(append(w.statusLines(), body), "\n")
	}
	return guidedSurface(w.width, w.height,
		"Registry", rail, title, main)
}

func (w *workflowsView) renderGuidedGraph() string {
	_, mainWidth := guidedPaneWidths(w.width)
	rail := w.renderRegistryRail(w.lines(), w.height-2)
	main := w.renderGraph(mainWidth-2, w.height-2)
	return guidedSurface(w.width, w.height,
		"Registry", rail, "Graph · "+w.graph.key.name, main)
}

func (w *workflowsView) renderRegistryRail(lines []wfLine, height int) string {
	rows := append([]string{}, w.statusLines()...)
	if len(rows) > 0 {
		rows = append(rows, "")
	}
	if len(lines) == 0 {
		if body, ok := w.emptyBody(lines); ok {
			rows = append(rows, body)
		}
		return strings.Join(window(rows, 0, height), "\n")
	}
	from, to := 0, 1
	for i, line := range lines {
		start := len(rows)
		if line.entry == nil {
			rows = append(rows, w.renderHeader(line))
			continue
		}
		marker := "   "
		name := line.entry.Name
		switch {
		case i == w.cursor:
			marker = styleFocus.Render(" ▸ ")
			name = styleTitle.Render(name)
			from = start
		case line.shadowedBy != "":
			name = styleDim.Render(name)
		}
		lead, badge := workflowRailBadge(*line.entry)
		rows = append(rows, marker+lead+name+badge)
		if note := shadowNote(line); note != "" {
			rows = append(rows, styleWarn.Render("      "+note))
		}
		if i == w.cursor {
			to = len(rows)
		}
	}
	return strings.Join(windowRange(rows, from, to, height), "\n")
}

// workflowRailBadge returns what goes before a rail entry's name and what
// goes after it. The two that say the entry cannot run lead, so a long name
// the rail cuts never takes them with it (#791): under NO_COLOR they are the
// only mark an entry is broken.
func workflowRailBadge(entry apiclient.WorkflowEntry) (lead, trail string) {
	switch {
	case !entry.Valid():
		return styleBad.Render("✗ invalid") + " ", ""
	case !entry.RunsHere():
		return styleWarn.Render("! platform") + " ", ""
	case len(entry.Warnings) > 0:
		return "", "  " + styleWarn.Render("⚠")
	case entry.File == "":
		return "", "  " + styleDim.Render("built-in")
	default:
		return "", ""
	}
}

func (w *workflowsView) renderWorkflowOverview(line wfLine, width, height int) string {
	e := line.entry
	rows := append([]string{}, w.statusLines()...)
	rows = append(rows, "  "+styleTitle.Render(e.Name))
	if e.Description != "" {
		rows = append(rows, hangingWrap("  ", "  ", e.Description, width, styleDim)...)
	}
	rows = append(rows, "",
		section("Registry"),
		workflowFact("scope", e.Scope),
		workflowFact("source", firstNonEmpty(e.File, "built-in")),
	)
	if note := shadowNote(line); note != "" {
		rows = append(rows, workflowFact("shadowing", styleWarn.Render(note)))
	}
	if warn := globalEditNote(line); warn != "" {
		rows = append(rows, workflowFact("editing", styleWarn.Render(warn)))
	}
	rows = append(rows, "", section("Availability"))
	rows = append(rows, workflowAvailability(e, width)...)
	rows = append(rows, "", section("Steps"))
	if w.expanded {
		steps := w.renderStepRows(line, "  ")
		available := max(height-len(rows)-1, 1)
		shown := min(len(steps), available)
		if shown < len(steps) {
			shown = max(shown-1, 0)
		}
		rows = append(rows, steps[:shown]...)
		if shown < len(steps) {
			rows = append(rows, styleDim.Render(fmt.Sprintf(
				"  … %d more top-level steps", len(steps)-shown)))
		}
	} else {
		rows = append(rows, styleDim.Render(fmt.Sprintf(
			"  %d top-level steps · enter to show the resolved list", len(e.Steps))))
	}
	rows = append(rows, styleDim.Render("  enter steps · g graph · e edit · R reload"))
	return strings.Join(window(rows, 0, height), "\n")
}

func workflowFact(label, value string) string {
	return "  " + styleDim.Render(fmt.Sprintf("%-12s", label)) + " " + value
}

func workflowAvailability(e *apiclient.WorkflowEntry, width int) []string {
	var rows []string
	switch {
	case !e.Valid():
		rows = append(rows, styleBad.Render("  ✗ validation failed"))
	case !e.RunsHere():
		rows = append(rows, styleWarn.Render("  ! not on this platform · "+e.PlatformNote()))
	default:
		rows = append(rows, styleOK.Render("  ✓ available on this host"))
	}
	if note := e.PlatformNote(); note != "" && e.RunsHere() {
		rows = append(rows, workflowFact("platforms", strings.TrimPrefix(note, "needs ")))
	}
	for _, finding := range e.Errors {
		rows = append(rows, hangingWrap("  ⚠ ", "    ", findingText(finding), width, styleBad)...)
	}
	for _, finding := range e.Warnings {
		rows = append(rows, hangingWrap("  ⚠ ", "    ", findingText(finding), width, styleWarn)...)
	}
	return rows
}

// hangingWrap wraps plain text into width columns behind lead, continuing
// under pad — modalRow's hanging indent — and styles each line whole, so a
// break never lands inside an escape sequence (#791). A width not yet known
// leaves the text on one line, the way a view that has not been sized draws.
func hangingWrap(lead, pad, text string, width int, style lipgloss.Style) []string {
	if width <= 0 {
		return []string{style.Render(lead + text)}
	}
	return styled(wrapText(text, width, wrapOpts{prefix: lead, indent: cols(pad)}), style)
}

// renderList draws the registry through a viewport. The status lines are
// pinned above it rather than scrolled with it, the way the diff pane pins
// its summary: a warning that scrolled away is a warning nobody sees.
func (w *workflowsView) renderList(width, height int) string {
	head := w.statusLines()
	lines := w.lines()
	if body, ok := w.emptyBody(lines); ok {
		return strings.Join(append(head, body), "\n")
	}

	var rows []string
	cursorRow, cursorEnd := 0, 0
	for i, line := range lines {
		if i == w.cursor {
			cursorRow = len(rows)
		}
		rows = append(rows, w.renderLine(i, line, width)...)
		if i == w.cursor {
			cursorEnd = len(rows) - 1
		}
		if i == w.cursor && w.expanded && line.entry != nil {
			rows = append(rows, w.renderSteps(line)...)
		}
	}

	// A view that has not been told its size yet has nothing to crop to.
	// Returning one row because the height is still zero would hide the
	// registry until the first resize.
	if width <= 0 || height <= 0 {
		return strings.Join(append(head, rows...), "\n")
	}
	body := max(height-len(head), 1)
	w.vp.SetWidth(max(width, 1))
	w.vp.SetHeight(body)
	w.vp.SetContent(strings.Join(rows, "\n"))
	// Reveal the selected entry rather than the expansion below it: the
	// entry you are on is what has to stay on screen — all of it, since its
	// description or error wraps under the name (#791, review F1). When it
	// is taller than the pane its name row wins.
	off := w.vp.YOffset()
	if cursorEnd >= off+body {
		off = cursorEnd - body + 1
	}
	if cursorRow < off {
		off = cursorRow
	}
	w.vp.SetYOffset(off)
	return strings.Join(append(head, w.vp.View()), "\n")
}

// renderGraph draws the open sub-layer: a header naming the entry, the graph
// itself, and a fixed inspector strip. The strip's height is fixed so the
// viewport's arithmetic does not change when a node with more detail is
// selected.
func (w *workflowsView) renderGraph(width, height int) string {
	g := w.graph
	out := []string{w.graphHeader()}
	body := max(height-graphChromeRows, 1)

	switch {
	case g.err != "":
		out = append(out, styleBad.Render("  ⚠ "+g.err),
			styleDim.Render("  R retries · esc returns to the registry"))
	case len(g.findings) > 0:
		// The file broke between the list load and this fetch.
		out = append(out, styleBad.Render("  ⚠ "+g.key.name+" no longer parses:"))
		for _, f := range g.findings {
			out = append(out, styleBad.Render("      "+findingText(f)))
		}
	case !g.loaded && g.loading:
		out = append(out, styleDim.Render("  loading the definition…"))
	case !g.loaded:
		out = append(out, styleDim.Render("  nothing to draw"))
	case g.graph.TooNarrow():
		// A narrow terminal is told so rather than shown a flattened graph
		// that would misrepresent the topology (decision 8).
		out = append(out, styleWarn.Render("  terminal too narrow for the graph"),
			styleDim.Render(fmt.Sprintf("  it needs %d columns; this one has %d",
				g.graph.MinWidth(), width)))
	default:
		out = append(out, strings.Split(g.graph.View(), "\n")...)
	}
	for len(out) < 1+body {
		out = append(out, "")
	}
	return strings.Join(append(out[:1+body], w.inspector(width)...), "\n")
}

func (w *workflowsView) graphHeader() string {
	g := w.graph
	parts := []string{" " + styleTitle.Render(g.key.name), styleDim.Render("[" + g.scope + "]")}
	if g.loading {
		parts = append(parts, styleDim.Render("refreshing…"))
	}
	if g.file != "" {
		parts = append(parts, styleDim.Render(g.file))
	}
	return strings.Join(parts, "  ")
}

// inspectorRows is the fixed height of the strip: a rule and two lines of
// detail.
const inspectorRows = 3

// inspector is the selected node's detail — the full label and the fields the
// box could only show as a badge. Prompts and command bodies are deliberately
// absent: `e` opens the file, and a prompt truncated to one line is the noise
// a badge already avoided (decision 15).
func (w *workflowsView) inspector(width int) []string {
	rule := strings.Repeat("─", max(width, 1))
	out := []string{styleDim.Render(rule)}
	fields := w.graph.graph.Detail()
	if len(fields) == 0 {
		return append(out, styleDim.Render("  nothing selected"), "")
	}
	var parts []string
	for _, f := range fields {
		parts = append(parts, styleDim.Render(f.Label+":")+" "+f.Value)
	}
	for _, row := range packRow(parts, max(width-2, 1), inspectorRows-1) {
		out = append(out, "  "+row)
	}
	for len(out) < inspectorRows {
		out = append(out, "")
	}
	return out[:inspectorRows]
}

// packRow fills at most rows lines with as many parts as fit, measuring
// display width so a wide label does not overflow the strip.
func packRow(parts []string, width, rows int) []string {
	var out []string
	current := ""
	for _, p := range parts {
		candidate := p
		if current != "" {
			candidate = current + styleDim.Render(" · ") + p
		}
		if lipgloss.Width(candidate) > width && current != "" {
			out = append(out, current)
			if len(out) == rows {
				return out
			}
			current = p
			continue
		}
		current = candidate
	}
	if current != "" && len(out) < rows {
		out = append(out, current)
	}
	return out
}

func (w *workflowsView) statusLines() []string {
	var out []string
	// With no project registered the global and builtin registry is still
	// listed and still editable, so the shared empty state is a line above it
	// rather than the whole view (review F1): what the keys act on stays drawn.
	if w.project.id == 0 && w.noProjects {
		out = append(out, " "+styleDim.Render(noProjectsEmpty()))
	}
	if w.loadErr != nil {
		note := " ⚠ refresh failed: " + errString(w.loadErr)
		if !w.lastLoad.IsZero() {
			note += " — showing " + w.lastLoad.Local().Format("15:04:05")
		}
		out = append(out, styleBad.Render(note))
	}
	if w.err != "" {
		out = append(out, styleBad.Render(" ⚠ "+w.err))
	}
	return out
}

// emptyBody separates a registry with nothing in it from a view that has not
// fetched yet.
func (w *workflowsView) emptyBody(lines []wfLine) (string, bool) {
	if len(lines) > 0 {
		return "", false
	}
	if !w.loaded && w.loadErr == nil {
		return styleDim.Render("\n  loading the registry…\n"), true
	}
	return styleDim.Render(
		"\n  no workflows registered — every project can still run the built-in adhoc\n"), true
}

// renderLine draws one registry entry. The selected entry wraps its detail
// under its name so the whole description or error is readable; every other
// entry stays one row, ended with an ellipsis when it is cut, so the list
// keeps its density at 80×24 (#791). A width not yet known draws one row
// uncut.
func (w *workflowsView) renderLine(i int, line wfLine, width int) []string {
	if line.entry == nil {
		return []string{w.renderHeader(line)}
	}
	marker := "   "
	if i == w.cursor {
		marker = styleFocus.Render(" ▸ ")
	}
	e := line.entry
	name := e.Name
	switch {
	case !e.Valid():
		name = styleBad.Render(name)
	case line.shadowedBy != "":
		// Overridden here: still listed, so the global file stays reachable,
		// but dimmed, because it is not what this project runs (decision 40).
		name = styleDim.Render(name)
	}
	// The badges that say an entry cannot run lead the name, so a long name
	// never pushes them off the edge (#791).
	lead := ""
	switch {
	case !e.Valid():
		lead = styleBad.Render("invalid") + " "
	case !e.RunsHere():
		lead = styleWarn.Render("not on this platform") + " "
	}
	head := marker + lead + name + "  " + styleDim.Render("["+e.Scope+"]")
	detail, style := "", styleDim
	switch {
	case !e.Valid():
		detail, style = e.FirstError(), styleBad
	case !e.RunsHere():
		// A platform-restricted workflow stays listed where the registry is
		// browsed — "where did it go?" is a worse answer than "not here, and
		// here is what it needs" (task 010).
		detail, style = e.PlatformNote(), styleWarn
	case e.File == "":
		detail = "built-in"
	case len(e.Warnings) > 0:
		detail, style = "⚠ "+e.Warnings[0].Message, styleWarn
	case e.Description != "":
		detail = e.Description
	}
	note := shadowNote(line)
	if i == w.cursor && width > 0 {
		out := []string{ansi.Truncate(head, width, "…")}
		if detail != "" {
			out = append(out, hangingWrap(entryDetailIndent, entryDetailIndent, detail, width, style)...)
		}
		if note != "" {
			out = append(out, hangingWrap(entryDetailIndent, entryDetailIndent, note, width, styleWarn)...)
		}
		return out
	}
	row := head
	if detail != "" {
		row += "  " + style.Render(detail)
	}
	if note != "" {
		row += "  " + styleWarn.Render(note)
	}
	if width > 0 {
		row = ansi.Truncate(row, width, "…")
	}
	return []string{row}
}

// entryDetailIndent hangs a selected entry's wrapped detail under its name.
const entryDetailIndent = "     "

// renderHeader draws the one non-entry line, the project fetch failing.
func (w *workflowsView) renderHeader(line wfLine) string {
	return " " + styleBad.Render(line.note)
}

// shadowNote says which side of a §5.2 override a line is on, or nothing.
func shadowNote(line wfLine) string {
	switch {
	case line.shadows != "":
		return "shadows " + line.shadows + " " + line.entry.Name
	case line.shadowedBy != "":
		return "shadowed here by " + line.shadowedBy
	}
	return ""
}

// globalEditNote warns that a global entry's file is shared: the list is one
// project's, but the file it opens is every project's.
func globalEditNote(line wfLine) string {
	if line.entry.Scope != "global" || line.entry.File == "" {
		return ""
	}
	return "global — editing it affects every project"
}

// renderSteps is the expanded step list: what the workflow actually does,
// which is the question a registry row cannot answer on one line.
func (w *workflowsView) renderSteps(line wfLine) []string {
	e := line.entry
	var out []string
	if e.Description != "" {
		out = append(out, styleDim.Render("      "+e.Description))
	}
	if e.File != "" {
		out = append(out, styleDim.Render("      "+e.File))
	}
	if warn := globalEditNote(line); warn != "" {
		out = append(out, styleWarn.Render("      "+warn))
	}
	if note := e.PlatformNote(); note != "" {
		row := "      platforms: " + strings.TrimPrefix(note, "needs ")
		if e.RunsHere() {
			out = append(out, styleDim.Render(row))
		} else {
			out = append(out, styleWarn.Render(row+" — not this one"))
		}
	}
	for _, f := range e.Errors {
		out = append(out, styleBad.Render("      ⚠ "+findingText(f)))
	}
	for _, f := range e.Warnings {
		out = append(out, styleWarn.Render("      ⚠ "+findingText(f)))
	}
	// The daemon's resolution names §8.6 level 4; the registry listing cannot
	// (T4.7). Until it arrives the old wording stands — "adapter default" is
	// incomplete, not wrong.
	out = append(out, w.renderStepRows(line, "      ")...)
	return out
}

func (w *workflowsView) renderStepRows(line wfLine, indent string) []string {
	e := line.entry
	res := w.resolutionFor(line)
	out := make([]string, 0, len(e.Steps))
	for i, step := range e.Steps {
		label := firstNonEmpty(step.Name, step.ID)
		row := fmt.Sprintf("%s%d. %s  %s", indent, i+1, label, styleDim.Render(step.Type))
		name := step.Agent
		if i < len(res.Steps) && res.Steps[i].Agent != nil {
			name = res.Steps[i].Agent.Value
		}
		switch {
		case name != "":
			row += "  " + styleDim.Render("→ "+name)
		case step.Type == "agent":
			row += "  " + styleDim.Render("→ adapter default")
		}
		out = append(out, row)
	}
	if len(e.Steps) == 0 && e.Valid() {
		out = append(out, styleDim.Render(indent+"no steps"))
	}
	return out
}

// resolutionFor returns the cached resolution for a line, or the zero value
// when none has arrived — callers index into Steps, which is empty then.
func (w *workflowsView) resolutionFor(line wfLine) apiclient.Resolution {
	if line.entry == nil {
		return apiclient.Resolution{}
	}
	return w.resolutions[line.key()]
}

// findingText prefixes a validation finding with its source line when the
// registry reported one, so a broken file can be opened straight at it.
func findingText(f apiclient.WorkflowFinding) string {
	if f.Line > 0 {
		return fmt.Sprintf("line %d: %s", f.Line, f.Message)
	}
	return f.Message
}

// The step-detail modal (task 053). It is a bordered popup over the graph at
// the geometry the answer, repair and follow-up popups already use, composed
// on top of renderGraph's output rather than woven into it: with no modal open
// the layer renders byte for byte as it did before, which is what the
// inspector strip's "unchanged, bytes included" decision rests on.
//
// Everything the DTO carries about the node is here, unwrapped by the diagram
// stage and wrapped here, at the width only this side knows.

// modalLabelWidth is the label column. A wider one would buy alignment for
// `permission_mode` at the cost of a column of reading width on every prompt
// line, and prompts are the reason the modal exists.
const modalLabelWidth = 15

// modalWidth is the popup's outer width — the same reading measure §15's
// popups use: nearly the whole body, capped where a line stops being
// comfortable to scan.
func (w *workflowsView) modalWidth() int {
	pw := min(w.width-6, 120)
	if pw < 20 {
		pw = max(w.width, 1)
	}
	return pw
}

// modalHeight is the popup's outer height: as tall as its content, capped so
// the graph it was opened from stays visible around it. Two of the inner rows
// are the frame's and one is the key line.
func (w *workflowsView) modalHeight(lines int) int {
	return min(lines+3, max(w.height-4, 6))
}

// syncModal re-lays the open modal at the current size. It runs eagerly, the
// way the graph's own canvas does: a viewport clamps every offset against
// content it does not have yet, so a scroll arriving before the first paint
// would silently do nothing.
func (w *workflowsView) syncModal() {
	if w.graph == nil || w.graph.modal == nil {
		return
	}
	m := w.graph.modal
	pw := w.modalWidth()
	lines := w.modalLines(max(pw-4, 8))
	ph := w.modalHeight(len(lines))
	m.vp.SetWidth(max(pw-2, 1))
	m.vp.SetHeight(max(ph-3, 1))
	m.vp.SetContent(strings.Join(lines, "\n"))
}

// overlayStepModal draws the popup over the layer's own render.
func (w *workflowsView) overlayStepModal(bg string) string {
	m := w.graph.modal
	pw := w.modalWidth()
	ph := m.vp.Height() + 3
	body := m.vp.View() + "\n" +
		styleDim.Render("  esc close · ↑/↓ scroll · pgup/pgdn page · e edit · R reload")
	popup := frame("Step · "+m.title, body, pw, ph, true)
	x := max((w.width-pw)/2, 0)
	y := max((w.height-ph)/3, 1)
	return overlay(bg, popup, x, y)
}

// modalLines is the modal's content: the workflow the node sits in, then the
// node's own sections. It returns plain rows the viewport crops; nothing here
// truncates a value.
func (w *workflowsView) modalLines(width int) []string {
	g := w.graph
	out := []string{""}
	out = append(out, w.modalHeaderLines(width)...)
	for _, sec := range g.graph.FullDetail() {
		out = append(out, "", section(sec.Title))
		for _, f := range sec.Fields {
			out = append(out, modalFieldLines(f, width)...)
		}
	}
	return append(out, "")
}

// modalHeaderLines is the workflow the node belongs to: the context a step
// read on its own does not carry.
func (w *workflowsView) modalHeaderLines(width int) []string {
	g := w.graph
	out := []string{section("Workflow")}
	out = append(out, modalRow("name", g.key.name, false, width)...)
	if b := g.body; b != nil {
		out = append(out, modalRow("description", b.Description, false, width)...)
		out = append(out, modalRow("platforms", strings.Join(b.Platforms, ", "), false, width)...)
		out = append(out, modalRow("fields", workflowFieldsSummary(b.Fields), false, width)...)
	}
	out = append(out, modalRow("scope", g.scope, false, width)...)
	out = append(out, modalRow("file", firstNonEmpty(g.file, "built-in"), false, width)...)
	return out
}

// workflowFieldsSummary names the inputs the workflow declares, marking the
// required ones — the same `*` the new-task form uses.
func workflowFieldsSummary(fields []apiclient.WorkflowField) string {
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		name := f.Name
		if f.Required {
			name += "*"
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

// modalFieldLines is one detail row, wrapped, with an inherited value marked
// as inherited rather than folded in silently (§8.6).
func modalFieldLines(f workflowgraph.DetailField, width int) []string {
	return modalRow(f.Label, f.Value, f.Inherited, width)
}

// modalRow lays a label and a value out in two columns, wrapping the value —
// including a multi-line one, a `prompt` or a `run:` body — under itself
// rather than cutting it. An empty value prints nothing at all: a field
// nothing sets is absent from the modal, never an em dash.
func modalRow(label, value string, inherited bool, width int) []string {
	if value == "" {
		return nil
	}
	// The value column starts past the two-space indent, the label and the
	// space after it; a continuation line is padded to the same column.
	indent := modalLabelWidth + 3
	valueWidth := max(width-indent, 8)
	var wrapped []string
	for _, para := range strings.Split(value, "\n") {
		if para == "" {
			wrapped = append(wrapped, "")
			continue
		}
		wrapped = append(wrapped, wrapText(para, valueWidth, wrapOpts{})...)
	}
	if inherited {
		// The marker rides the last line so a long inherited value keeps its
		// own shape and the mark still reads as belonging to it.
		last := len(wrapped) - 1
		wrapped[last] += styleDim.Render("  (inherited from defaults)")
	}
	out := make([]string, 0, len(wrapped))
	pad := strings.Repeat(" ", indent)
	for i, line := range wrapped {
		if i == 0 {
			out = append(out, "  "+styleDim.Render(fmt.Sprintf("%-*s", modalLabelWidth, label))+" "+line)
			continue
		}
		out = append(out, pad+line)
	}
	return out
}
