package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The project picker (task 132.4, §15): `@`, the palette's "switch project"
// row, or a click on the header's `◆` segment lists every registered project
// with the figures that say where work is waiting, and enter makes the
// highlighted one the TUI's selection through root.selectProject.
//
// It follows palette.go's shape: a root-owned popup, held on the root while it
// is up, that owns every key but ctrl+c and is never open beside the palette,
// the copy picker, the link picker or the help sheet.
//
// The rows are one GET /v1/projects?stats=true per refresh, whatever the
// number of projects: on open, then a debounced refetch on the events that
// move a figure, but only while the picker is up. The figures are the
// daemon's; nothing is counted client-side, so a row never disagrees with the
// projects view or `vincent project list --stats`.
type projectPicker struct {
	input    textField
	projects []apiclient.Project
	// current is the selection the picker was opened over, marked on its row.
	current int64
	// cursor indexes matches(); overview is the overview row instead, set
	// only by ↓ past the last match and cleared by ↑ or typing. A flag rather
	// than cursor == len(matches): a filter matching nothing or an empty
	// list would otherwise land on the row without ↓ (review F8).
	cursor   int
	overview bool
	// loaded reports that a stats answer has landed. Until one has, the rows
	// are the root's own name list, which carries no figures.
	loaded bool
	err    error
	// offline is set by the root while the daemon is unreachable: the rows
	// are the cached list, which may be stale (task 132.7).
	offline bool
}

// projectPickerGlyph marks the selected project's row: the header segment's
// glyph, so the two read as one thing.
const projectPickerGlyph = headerProjectGlyph

func newProjectPicker(projects []apiclient.Project, current int64) *projectPicker {
	in := newTextField()
	in.SetPlaceholder("type to filter projects")
	in.SetPrompt("@ ")
	in.Focus()
	pp := &projectPicker{input: in, projects: projects, current: current}
	// Open on the current project, so enter straight away is a no-op rather
	// than a switch to whichever project sorts first.
	for i, p := range pp.matches() {
		if p.ID == current {
			pp.cursor = i
		}
	}
	return pp
}

// projectPickerMsg is one answer to fetchProjectPicker.
type projectPickerMsg struct {
	seq      int
	projects []apiclient.Project
	err      error
}

// projectPickerRefreshMsg is the picker's debounce firing.
type projectPickerRefreshMsg struct{}

// openProjectPickerMsg asks the root to raise the picker. It is the palette's
// "switch project" row's own action, so the row does not depend on replaying
// the project key: a tui.keys that gives `@` to another operation leaves the
// op unbound, and the palette never runs an unbound key (review F3 on #718).
type openProjectPickerMsg struct{}

// fetchProjectPicker is the picker's one list call.
func fetchProjectPicker(client *apiclient.Client, seq int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		projects, err := client.ListProjects(ctx, apiclient.WithStats())
		return projectPickerMsg{seq: seq, projects: projects, err: err}
	}
}

// land adopts a fresh answer, keeping the cursor on the project it was on. A
// failed refetch leaves the rows standing and says so.
func (pp *projectPicker) land(msg projectPickerMsg) {
	if msg.err != nil {
		pp.err = msg.err
		return
	}
	var on int64
	if m := pp.matches(); pp.cursor < len(m) {
		on = m[pp.cursor].ID
	}
	pp.projects, pp.loaded, pp.err = msg.projects, true, nil
	if pp.overview {
		// On the overview row: it stays the last row whatever landed.
		return
	}
	pp.cursor = 0
	for i, p := range pp.matches() {
		if p.ID == on {
			pp.cursor = i
		}
	}
}

// matches is the projects whose names hold the filter, case-insensitive, in
// the daemon's order.
func (pp *projectPicker) matches() []apiclient.Project {
	q := strings.ToLower(strings.TrimSpace(pp.input.Value()))
	if q == "" {
		return pp.projects
	}
	out := make([]apiclient.Project, 0, len(pp.projects))
	for _, p := range pp.projects {
		if strings.Contains(strings.ToLower(p.Name), q) {
			out = append(out, p)
		}
	}
	return out
}

// update handles one key. done reports the picker should close; pick is the
// project chosen, when one was.
func (pp *projectPicker) update(msg tea.KeyPressMsg) (pick *apiclient.Project, done bool, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, true, nil
	case "up":
		if pp.overview {
			pp.overview = false
			pp.cursor = max(len(pp.matches())-1, 0)
		} else {
			pp.cursor = max(pp.cursor-1, 0)
		}
		return nil, false, nil
	case "down":
		if pp.cursor >= len(pp.matches())-1 {
			pp.overview = true
		} else {
			pp.cursor++
		}
		return nil, false, nil
	case "enter":
		if pp.overview {
			// The overview row, never a project: it switches screens and
			// leaves the selection alone.
			return nil, true, func() tea.Msg { return selectViewMsg{id: viewProjects} }
		}
		m := pp.matches()
		if len(m) == 0 {
			return nil, true, nil // nothing matches: enter just closes
		}
		p := m[min(pp.cursor, len(m)-1)]
		return &p, true, nil
	}
	var c tea.Cmd
	pp.input, c = pp.input.Update(msg)
	pp.overview = false
	pp.clampCursor()
	return nil, false, c
}

// paste types text into the filter, the way the palette takes one.
func (pp *projectPicker) paste(text string) tea.Cmd {
	var cmd tea.Cmd
	pp.input, cmd = pp.input.Update(tea.PasteMsg{Content: text})
	pp.overview = false
	pp.clampCursor()
	return cmd
}

// clampCursor keeps the cursor on a matching project as the filter narrows;
// the overview row is reached only by ↓.
func (pp *projectPicker) clampCursor() {
	if n := len(pp.matches()); pp.cursor >= n {
		pp.cursor = max(n-1, 0)
	}
}

// projectPickerOverview is the picker's last row (task 132.15): the way from
// the picker to the project overview, where projects are added, edited and
// compared. It is not a project — the filter never matches or hides it, and
// enter on it selects nothing.
const projectPickerOverview = "overview & manage…"

// render draws the picker box for overlaying: the filter line, then one row
// per matching project, windowed around the cursor.
func (pp *projectPicker) render(w, h int) string {
	inner := max(w-2, 10)
	lines := make([]string, 0, h)
	pp.input.SetWidth(max(inner-1, 1))
	lines = append(lines, fieldRows(" ", pp.input)...)
	if pp.offline {
		lines = append(lines, styleDim.Render(ansi.Truncate("  offline — list may be stale", inner, "…")))
	} else if pp.err != nil {
		lines = append(lines, styleWarn.Render(ansi.Truncate("  refresh failed: "+pp.err.Error(), inner, "…")))
	}

	m := pp.matches()
	if len(m) == 0 {
		msg := "  nothing matches — esc closes"
		if len(pp.projects) == 0 {
			msg = "  no projects registered — the overview adds one"
		}
		lines = append(lines, styleDim.Render(msg))
	}
	// cursor is the highlighted row: the overview row only when it was
	// reached by ↓, and none at all over an empty match list.
	cursor := min(pp.cursor, len(m)-1)
	if pp.overview {
		cursor = len(m)
	}
	rows := make([]string, 0, len(m)+1)
	for i, p := range m {
		rows = append(rows, pp.row(p, i == cursor, inner))
	}
	overview := "  " + styleDim.Render(projectPickerOverview)
	if pp.overview {
		overview = styleFocus.Render("› ") + styleTitle.Render(projectPickerOverview)
	}
	rows = append(rows, overview)
	lines = append(lines, window(rows, max(cursor, 0), h-2-len(lines))...)
	return frame("projects", strings.Join(lines, "\n"), w, h, true)
}

// row is one project: the cursor and current-project marks, the name, and its
// figures right-aligned (C1 of task 132.4).
func (pp *projectPicker) row(p apiclient.Project, focused bool, width int) string {
	mark, style := "  ", styleDim
	if focused {
		mark, style = styleFocus.Render("› "), styleTitle
	}
	current := "  "
	if p.ID == pp.current {
		current = styleTitle.Render(projectPickerGlyph + " ")
	}
	name, figures := projectPickerFit(p, max(width-4, 1))
	line := mark + current + style.Render(name)
	if figures == "" {
		return line
	}
	pad := max(width-ansi.StringWidth(line)-ansi.StringWidth(figures), 1)
	return line + strings.Repeat(" ", pad) + figures
}

// projectPickerFigures is a row's figure columns in shedding order reversed:
// attention, running, active, open issues. Attention is omitted at zero, and a
// row with no stats — the daemon degraded to null — has none at all.
//
// Attention is tasks.attention alone: chat attention is never part of `!N`
// (task 132 decision 2). Running is slots used against the project's own cap;
// an uncapped project has none of its own, and the global cap is not a
// per-project limit, so it reads `N running` with no denominator.
func projectPickerFigures(p apiclient.Project) []string {
	if p.Stats == nil {
		return nil
	}
	attention := ""
	if n := p.Stats.Tasks.Attention; n > 0 {
		attention = fmt.Sprintf("!%d", n)
	}
	running := fmt.Sprintf("%d running", p.SlotsUsed)
	if p.MaxParallelTasks != nil {
		running = fmt.Sprintf("%d/%d running", p.SlotsUsed, *p.MaxParallelTasks)
	}
	return []string{
		attention,
		running,
		fmt.Sprintf("%d active", p.Stats.Tasks.Active),
		fmt.Sprintf("%d open issues", p.Stats.Issues.Open),
	}
}

// projectPickerFit fits a row's name and figures into width. The columns drop
// from the end — issues, then active, then running, then attention — and only
// when none is left does the name shorten, behind an ellipsis.
func projectPickerFit(p apiclient.Project, width int) (name, figures string) {
	cols := projectPickerFigures(p)
	for n := len(cols); n >= 0; n-- {
		figures = joinFigures(cols[:n])
		need := ansi.StringWidth(p.Name)
		if figures != "" {
			need += 2 + ansi.StringWidth(figures)
		}
		if need <= width {
			return p.Name, figures
		}
	}
	return ansi.Truncate(p.Name, width, "…"), ""
}

func joinFigures(cols []string) string {
	kept := make([]string, 0, len(cols))
	for _, c := range cols {
		switch {
		case c == "":
		case strings.HasPrefix(c, "!"):
			kept = append(kept, styleWarn.Render(c))
		default:
			kept = append(kept, styleDim.Render(c))
		}
	}
	return strings.Join(kept, styleDim.Render(" · "))
}
