package tui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The Workflows screen's wide layout (task 150.7): the registry grouped by
// scope, the selected workflow's graph beside it, and its details on the right.
//
// The graph follows the selection, so it is read once the selection has rested
// for wfPreviewDebounce: holding an arrow key costs one read rather than one per
// row. `g` or `enter` moves the keyboard into the picture on screen, and `esc`
// hands it back without losing the entry.

const (
	// wfDetailsMinW is the narrowest terminal that also shows the details.
	wfDetailsMinW = 150
	// wfPreviewDebounce is how long the selection rests before its graph is read.
	wfPreviewDebounce = 150 * time.Millisecond
	// wfRowH is one registry entry's height: its name and one line about it.
	wfRowH = 2
)

// workflowPreviewDueMsg says the selection may have rested on key long enough.
type workflowPreviewDueMsg struct{ key wfResolveKey }

// wideTerm reports whether the terminal is wide enough for this layout. The
// list's order hangs on it, so unlike frameless it ignores an open form: the
// cursor must keep meaning the same entry behind one.
func (w *workflowsView) wideTerm() bool { return w.termW >= mgmtWideMinW }

// wfPaneWidths is the registry's and the details' outer widths.
func wfPaneWidths(width int) (list, details int) {
	list = min(width*26/100, 44)
	if width >= wfDetailsMinW {
		details = min(width*24/100, 46)
	}
	return list, details
}

// wfScopeRank orders the groups: what is closest to this project comes first.
func wfScopeRank(scope string) int {
	switch scope {
	case scopeProject:
		return 0
	case "global":
		return 1
	default:
		return 2
	}
}

// wfGroupTitle is the heading over one rank's entries.
func wfGroupTitle(rank int) string {
	switch rank {
	case 0:
		return toneMagenta.Bold(true).Render(ic(headerProjectGlyph + " This project"))
	case 1:
		return toneCyan.Bold(true).Render(ic("⟡ Global"))
	default:
		return toneBlue.Bold(true).Render(ic("▣ Built-in"))
	}
}

// groupLines puts the wide layout's groups in order, keeping each one's own.
func groupLines(lines []wfLine) {
	sort.SliceStable(lines, func(i, j int) bool {
		return wfScopeRank(lines[i].entry.Scope) < wfScopeRank(lines[j].entry.Scope)
	})
}

// selectKey puts the cursor back on an entry after the list has been reordered.
func (w *workflowsView) selectKey(key wfResolveKey) {
	for i, line := range w.lines() {
		if line.entry != nil && line.key() == key {
			w.cursor = i
			return
		}
	}
}

// shownGraph is the layer on screen: the one with the keyboard, else the preview.
func (w *workflowsView) shownGraph() *graphLayer {
	if w.graph != nil {
		return w.graph
	}
	return w.preview
}

// previewCmd arms the read of the selected entry's graph, unless it is held or
// already on its way. An entry that does not parse has no graph to read.
func (w *workflowsView) previewCmd() tea.Cmd {
	if !w.wideTerm() || w.graph != nil || w.client == nil {
		return nil
	}
	line, ok := w.currentLine()
	if !ok || !line.entry.Valid() {
		w.preview, w.previewArmed = nil, wfResolveKey{}
		return nil
	}
	key := line.key()
	if (w.preview != nil && w.preview.key == key) || w.previewArmed == key {
		return nil
	}
	w.previewArmed = key
	return tea.Tick(wfPreviewDebounce, func(time.Time) tea.Msg { return workflowPreviewDueMsg{key} })
}

// previewDue reads the graph if the selection is still where the wait began.
func (w *workflowsView) previewDue(key wfResolveKey) tea.Cmd {
	if w.previewArmed == key {
		w.previewArmed = wfResolveKey{}
	}
	line, ok := w.currentLine()
	if !ok || line.key() != key || w.graph != nil || (w.preview != nil && w.preview.key == key) {
		return nil
	}
	w.preview = newGraphLayer(key, line.entry)
	w.sizeGraph()
	return w.definitionCmd(key)
}

// graphPaneSize is the room inside the graph's pane.
func (w *workflowsView) graphPaneSize() (width, height int) {
	list, details := wfPaneWidths(w.width)
	h := max(w.height-len(w.statusLines()), 4)
	return max(w.width-list-details-4, 1), max(h-2, 1)
}

// renderWide draws the layout in exactly width×height.
func (w *workflowsView) renderWide(width, height int) string {
	top := truncateRows(w.statusLines(), width)
	h := max(height-len(top), 4)
	listW, detailsW := wfPaneWidths(width)
	panes := joinColumns(w.registryPane(listW, h, len(top)), w.graphPane(width-listW-detailsW, h))
	if detailsW > 0 {
		panes = joinColumns(panes, w.detailsPane(detailsW, h))
	}
	return strings.Join(append(top, panes...), "\n")
}

// registryPane is the entries under their scopes, scrolled to keep the selected
// one on screen.
func (w *workflowsView) registryPane(width, h, top int) []string {
	inner := max(width-4, 1)
	lines := w.lines()
	var rows []string
	// owner is the entry each row belongs to, for the mouse.
	var owner []int
	add := func(index int, row ...string) {
		for _, r := range row {
			rows, owner = append(rows, r), append(owner, index)
		}
	}
	rank, from := -1, 0
	for i, line := range lines {
		if line.entry == nil {
			add(-1, w.renderHeader(line))
			continue
		}
		if r := wfScopeRank(line.entry.Scope); r != rank {
			if rank >= 0 {
				add(-1, "")
			}
			rank = r
			add(-1, wfGroupTitle(r))
		}
		if i == w.cursor {
			from = len(rows)
		}
		add(i, wfRegistryRow(line, inner, i == w.cursor)...)
	}
	if body, ok := w.emptyBody(lines); ok {
		rows = styled(wrapText(strings.TrimSpace(ansi.Strip(body)), inner, wrapOpts{}), styleDim)
	}

	room := max(h-2, 1)
	start := 0
	if len(rows) > room {
		start = max(min(windowStart(len(rows), from, room), from), 0)
		if from+wfRowH > start+room {
			start = from + wfRowH - room
		}
		start = max(min(start, len(rows)-room), 0)
	}
	w.hits = nil
	for r := start; r < min(start+room, len(owner)); r++ {
		if owner[r] >= 0 {
			y := top + 1 + r - start
			w.hits = append(w.hits, mgmtHit{1, width - 1, y, y + 1, owner[r]})
		}
	}
	border := styleDim
	if w.graph == nil {
		border = styleFocus
	}
	count := toneBlue.Render(strconv.Itoa(len(w.global) + len(w.own)))
	return drawBox(toneBlue.Bold(true).Render(ic("⟡ Workflows")), count, rows[start:], width, h, edgesRounded, border)
}

// wfRegistryRow is one entry: its name, then the most useful thing about it.
func wfRegistryRow(line wfLine, inner int, selected bool) []string {
	e := line.entry
	marker, name := "  ", e.Name
	switch {
	case selected:
		marker, name = styleFocus.Render("▸ "), styleTitle.Render(name)
	case !e.Valid():
		name = styleBad.Render(name)
	case line.shadowedBy != "":
		name = styleDim.Render(name)
	}
	lead, _ := workflowRailBadge(*e)
	steps := styleDim.Render(plural(len(e.Steps), "step", "steps"))
	room := max(inner-ansi.StringWidth(steps)-1, 1)
	head := padBetweenCells(ansi.Truncate(marker+lead+name, room, "…"), steps, inner)
	detail, style := wfRowDetail(line)
	return []string{head, "  " + style.Render(ansi.Truncate(oneLine(detail), max(inner-2, 1), "…"))}
}

// wfRowDetail picks the one line an entry gets, what stops it running first.
func wfRowDetail(line wfLine) (string, lipgloss.Style) {
	e := line.entry
	note := shadowNote(line)
	switch {
	case !e.Valid():
		return e.FirstError(), styleBad
	case !e.RunsHere():
		return e.PlatformNote(), styleWarn
	case note != "":
		return note, styleWarn
	case len(e.Warnings) > 0:
		return "⚠ " + e.Warnings[0].Message, styleWarn
	}
	return e.Description, styleDim
}

// graphPane is the selected entry's graph, framed in the focus colour while it
// has the keyboard.
func (w *workflowsView) graphPane(width, h int) []string {
	inner := max(width-4, 1)
	title := toneBlue.Bold(true).Render(ic("▤ Graph"))
	if w.graph != nil {
		return drawBox(title, styleDim.Render("esc returns to the list"),
			strings.Split(w.renderGraph(inner, h-2), "\n"), width, h, edgesRounded, styleFocus)
	}
	var body []string
	line, ok := w.currentLine()
	switch {
	case !ok:
		body = []string{styleDim.Render("Select a workflow to draw it.")}
	case !line.entry.Valid():
		body = []string{styleBad.Render(line.entry.Name + " does not parse, so there is no graph to draw.")}
		for _, f := range line.entry.Errors {
			body = append(body, styled(wrapText(findingText(f), inner, wrapOpts{}), styleBad)...)
		}
	case w.preview == nil || w.preview.key != line.key():
		body = []string{styleDim.Render("loading the definition…")}
	default:
		body = strings.Split(w.renderGraph(inner, h-2), "\n")
	}
	return drawBox(title, styleDim.Render("g explores"), body, width, h, edgesRounded, styleDim)
}

// detailsPane is what the graph cannot show: where the entry comes from, whether
// it runs here, what it asks for, and its steps with the agents they resolve to.
func (w *workflowsView) detailsPane(width, h int) []string {
	title := styleTitle.Render("Details")
	line, ok := w.currentLine()
	if !ok {
		return drawBox(title, "", nil, width, h, edgesRounded, styleDim)
	}
	e := line.entry
	inner := max(width-4, 1)
	body := styled(wrapText(e.Name, inner, wrapOpts{maxLines: 2}), styleTitle)
	if e.Description != "" {
		body = append(body, styled(wrapText(oneLine(e.Description), inner, wrapOpts{maxLines: 4}), styleDim)...)
	}
	body = append(body, "", styleTitle.Render(ic("⟡ Registry")), workflowFact("scope", e.Scope))
	body = append(body, styled(wrapText(firstNonEmpty(e.File, "built-in"), inner, wrapOpts{prefix: "  ", indent: 2, maxLines: 3}), styleDim)...)
	if note := shadowNote(line); note != "" {
		body = append(body, styled(wrapText(note, inner, wrapOpts{prefix: "  ", indent: 2}), styleWarn)...)
	}
	if warn := globalEditNote(line); warn != "" {
		body = append(body, styled(wrapText(warn, inner, wrapOpts{prefix: "  ", indent: 2}), styleWarn)...)
	}
	body = append(body, "", styleTitle.Render(ic("◉ Availability")))
	body = append(body, workflowAvailability(e, inner)...)
	if fields := workflowFieldsSummary(e.Fields); fields != "" {
		body = append(body, "", styleTitle.Render(ic("≡ Fields")))
		body = append(body, styled(wrapText(fields, inner, wrapOpts{prefix: "  ", indent: 2}), styleDim)...)
	}
	body = append(body, "", styleTitle.Render(ic("▶ Steps")))
	steps := w.renderStepRows(line, "  ")
	room := max(h-2-len(body), 1)
	if len(steps) > room {
		rest := len(steps) - room + 1
		steps = append(steps[:room-1:room-1], styleDim.Render("  … "+strconv.Itoa(rest)+" more"))
	}
	return drawBox(title, "", append(body, steps...), width, h, edgesRounded, styleDim)
}

// updateWideMouse selects an entry, moves the keyboard into the graph that was
// clicked, and scrolls whichever of the two the wheel is over.
func (w *workflowsView) updateWideMouse(msg tea.Msg) tea.Cmd {
	if w.graph != nil && w.graph.modal != nil {
		w.updateGraphMouse(msg)
		return nil
	}
	listW, detailsW := wfPaneWidths(w.width)
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if i, ok := mgmtHitAt(w.hits, msg); ok {
			w.graph, w.cursor = nil, i
			return tea.Batch(w.resolveCmd(), w.previewCmd())
		}
		if msg.X < listW || msg.X >= w.width-detailsW {
			return nil
		}
		if line, ok := w.currentLine(); ok && w.graph == nil && msg.Button == tea.MouseLeft &&
			w.preview != nil && w.preview.key == line.key() {
			w.graph = w.preview
		}
	case tea.MouseWheelMsg:
		if msg.X < listW && w.graph == nil {
			delta := 1
			if msg.Button == tea.MouseWheelUp {
				delta = -1
			}
			w.moveCursor(delta)
			return tea.Batch(w.resolveCmd(), w.previewCmd())
		}
	}
	w.updateGraphMouse(msg)
	return nil
}
