package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

func (w *workflowsView) render(width, height int) string {
	if width > 0 {
		w.width = width
	}
	if height > 0 {
		w.height = height
	}
	if w.graph != nil {
		w.sizeGraph()
		if guidedTakeover(w.width, w.height) {
			return w.renderGuidedGraph()
		}
		return w.renderGraph(width, height)
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
		main = w.renderWorkflowOverview(line, w.height-2)
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
		if i == w.cursor {
			marker = styleFocus.Render(" ▸ ")
			name = styleTitle.Render(name)
			from = start
		}
		badge := workflowRailBadge(*line.entry)
		rows = append(rows, marker+name+badge)
		if line.shadows != "" {
			rows = append(rows, styleWarn.Render("      shadows global"))
		}
		if i == w.cursor {
			to = len(rows)
		}
	}
	return strings.Join(windowRange(rows, from, to, height), "\n")
}

func workflowRailBadge(entry apiclient.WorkflowEntry) string {
	switch {
	case !entry.Valid():
		return "  " + styleBad.Render("✗ invalid")
	case !entry.RunsHere():
		return "  " + styleWarn.Render("! platform")
	case len(entry.Warnings) > 0:
		return "  " + styleWarn.Render("⚠")
	case entry.File == "":
		return "  " + styleDim.Render("built-in")
	default:
		return ""
	}
}

func (w *workflowsView) renderWorkflowOverview(line wfLine, height int) string {
	e := line.entry
	rows := append([]string{}, w.statusLines()...)
	rows = append(rows, "  "+styleTitle.Render(e.Name))
	if e.Description != "" {
		rows = append(rows, "  "+styleDim.Render(e.Description))
	}
	rows = append(rows, "",
		section("Registry"),
		workflowFact("scope", e.Scope),
		workflowFact("source", firstNonEmpty(e.File, "built-in")),
	)
	if line.block != nil {
		rows = append(rows, workflowFact("registry", line.block.name))
	}
	if line.shadows != "" {
		rows = append(rows, workflowFact("shadowing", styleWarn.Render("global "+line.shadows)))
	}
	rows = append(rows, "", section("Availability"))
	rows = append(rows, workflowAvailability(e)...)
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

func workflowAvailability(e *apiclient.WorkflowEntry) []string {
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
		rows = append(rows, styleBad.Render("  ⚠ "+findingText(finding)))
	}
	for _, finding := range e.Warnings {
		rows = append(rows, styleWarn.Render("  ⚠ "+findingText(finding)))
	}
	return rows
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
	cursorRow := 0
	for i, line := range lines {
		if i == w.cursor {
			cursorRow = len(rows)
		}
		rows = append(rows, w.renderLine(i, line))
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
	// Reveal the cursor rather than the expansion below it: the row you are
	// on is the one that has to stay on screen.
	w.vp.EnsureVisible(cursorRow, 0, 0)
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
// fetched yet. A scope with no entries of its own is not empty in this sense
// — it renders as its own header with a note, because "this project has no
// workflows" and "there are no workflows" are different facts.
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

func (w *workflowsView) renderLine(i int, line wfLine) string {
	if line.header != "" {
		return w.renderHeader(line)
	}
	marker := "   "
	if i == w.cursor {
		marker = styleFocus.Render(" ▸ ")
	}
	e := line.entry
	name := e.Name
	if !e.Valid() {
		name = styleBad.Render(name)
	}
	parts := []string{marker + name}
	parts = append(parts, styleDim.Render("["+e.Scope+"]"))
	switch {
	case !e.Valid():
		parts = append(parts, styleBad.Render("invalid: "+e.FirstError()))
	case !e.RunsHere():
		// A platform-restricted workflow stays listed where the registry is
		// browsed — "where did it go?" is a worse answer than "not here, and
		// here is what it needs" (task 010).
		parts = append(parts, styleWarn.Render("not on this platform · "+e.PlatformNote()))
	case e.File == "":
		parts = append(parts, styleDim.Render("built-in"))
	case len(e.Warnings) > 0:
		parts = append(parts, styleWarn.Render("⚠ "+e.Warnings[0].Message))
	case e.Description != "":
		parts = append(parts, styleDim.Render(e.Description))
	}
	if line.shadows != "" {
		parts = append(parts, styleWarn.Render("shadows global "+line.shadows))
	}
	return strings.Join(parts, "  ")
}

func (w *workflowsView) renderHeader(line wfLine) string {
	b := line.block
	head := " " + styleTitle.Render(b.name)
	switch {
	case b.err != nil:
		return head + "  " + styleBad.Render("registry unavailable: "+errString(b.err))
	case len(b.entries) == 0 && b.projectID != 0:
		return head + "  " + styleDim.Render("no workflows of its own")
	case len(b.entries) == 0:
		return head + "  " + styleDim.Render("no global workflows")
	}
	return head
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
	key := wfResolveKey{name: line.entry.Name}
	if line.block != nil {
		key.projectID = line.block.projectID
	}
	return w.resolutions[key]
}

// findingText prefixes a validation finding with its source line when the
// registry reported one, so a broken file can be opened straight at it.
func findingText(f apiclient.WorkflowFinding) string {
	if f.Line > 0 {
		return fmt.Sprintf("line %d: %s", f.Line, f.Message)
	}
	return f.Message
}
