package tui

import (
	"fmt"
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
)

func (w *workflowsView) render(width, height int) string {
	if width > 0 {
		w.width = width
	}
	if height > 0 {
		w.height = height
	}
	var out []string
	out = append(out, w.statusLines()...)

	lines := w.lines()
	if body, ok := w.emptyBody(lines); ok {
		return strings.Join(append(out, body), "\n")
	}
	for i, line := range lines {
		out = append(out, w.renderLine(i, line))
		if i == w.cursor && w.expanded && line.entry != nil {
			out = append(out, w.renderSteps(line)...)
		}
	}
	return strings.Join(out, "\n")
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
	for _, f := range e.Errors {
		out = append(out, styleBad.Render("      ⚠ "+findingText(f)))
	}
	for _, f := range e.Warnings {
		out = append(out, styleWarn.Render("      ⚠ "+findingText(f)))
	}
	// The daemon's resolution names §8.6 level 4; the registry listing cannot
	// (T4.7). Until it arrives the old wording stands — "adapter default" is
	// incomplete, not wrong.
	res := w.resolutionFor(line)
	for i, s := range e.Steps {
		label := firstNonEmpty(s.Name, s.ID)
		row := fmt.Sprintf("      %d. %s  %s", i+1, label, styleDim.Render(s.Type))
		name := s.Agent
		if i < len(res.Steps) && res.Steps[i].Agent != nil {
			name = res.Steps[i].Agent.Value
		}
		switch {
		case name != "":
			row += "  " + styleDim.Render("→ "+name)
		case s.Type == "agent":
			row += "  " + styleDim.Render("→ adapter default")
		}
		out = append(out, row)
	}
	if len(e.Steps) == 0 && e.Valid() {
		out = append(out, styleDim.Render("      no steps"))
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
