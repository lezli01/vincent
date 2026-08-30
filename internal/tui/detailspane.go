package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// detailsPane is the §15 Task Details inspector: a section sidebar against an
// independently scrolled content pane (task 049.8). It is a sub-model rather
// than fields on taskView because task 059 gives every open answer, repair and
// follow-up popup its own instance of the same inspector — one implementation,
// so the navigation is identical wherever it is read from.
//
// The pane is *fed* its lines rather than owning the data: the document comes
// from taskView.detailLines, which reads the task and — for the "GitHub pull
// request" section — the separately fetched pull row. Fetching is not the
// pane's business.
type detailsPane struct {
	// top is the scroll offset within the selected section's body; count and
	// h are what it was last clamped against, so a key press can page without
	// re-splitting the document.
	top   int
	count int
	h     int
	// section is the selected title and sections is the document's, in
	// document order. sidebarW/Y/Top are the last drawn geometry, kept for
	// hit-testing: a click lands on what was on screen.
	section    string
	sections   []string
	sidebarW   int
	sidebarY   int
	sidebarTop int
}

// reset returns the pane to the top of the first section, which is where a
// newly opened task — or a newly opened popup — should start reading.
func (p *detailsPane) reset() {
	p.top = 0
	p.section = ""
}

// render draws the pane at width×height. ready says the task document is real
// rather than a placeholder ("loading…", "task unavailable"): a placeholder has
// no sections to put in a sidebar, so it is windowed at the full width instead.
// lines produces the document at the content width the pane settles on.
func (p *detailsPane) render(width, height int, ready bool, lines func(width int) []string) string {
	if !ready {
		body := lines(width)
		p.count, p.h = len(body), height
		p.top = min(max(p.top, 0), max(len(body)-height, 0))
		return strings.Join(windowRange(body, p.top, p.top+height, height), "\n")
	}

	p.sidebarW = min(24, max(width/3, 16))
	contentWidth := max(width-p.sidebarW-3, 12)
	document := splitTaskDetailDocument(lines(contentWidth))
	if len(document.sections) == 0 {
		return strings.Join(document.header, "\n")
	}

	p.sections = p.sections[:0]
	selected := 0
	for i, item := range document.sections {
		p.sections = append(p.sections, item.title)
		if item.title == p.section {
			selected = i
		}
	}
	if p.section == "" || p.sections[selected] != p.section {
		p.section = p.sections[0]
		p.top = 0
		selected = 0
	}

	header := append([]string(nil), document.header...)
	for len(header) > 0 && header[len(header)-1] == "" {
		header = header[:len(header)-1]
	}
	if len(header) > 0 && len(header) < height {
		header = append(header, "")
	}
	p.sidebarY = len(header)
	bodyH := max(height-len(header), 1)
	content := document.sections[selected].lines
	p.count, p.h = len(content), bodyH
	p.top = min(max(p.top, 0), max(len(content)-bodyH, 0))
	visible := windowRange(content, p.top, p.top+bodyH, bodyH)
	sidebar := p.renderSidebar(bodyH)

	out := make([]string, 0, len(header)+bodyH)
	for _, line := range header {
		out = append(out, ansi.Truncate(line, max(width, 1), "…"))
	}
	separator := " " + styleDim.Render("│") + " "
	for row := range bodyH {
		left := ""
		if row < len(sidebar) {
			left = sidebar[row]
		}
		right := ""
		if row < len(visible) {
			right = ansi.Truncate(visible[row], contentWidth, "…")
		}
		out = append(out, padDisplayWidth(left, p.sidebarW)+separator+right)
	}
	return strings.Join(out, "\n")
}

func (p *detailsPane) renderSidebar(height int) []string {
	lines := make([]string, 0, height)
	selected := 0
	for i, title := range p.sections {
		if title == p.section {
			selected = i
			break
		}
	}
	p.sidebarTop = windowStart(len(p.sections), selected, height)
	end := min(p.sidebarTop+height, len(p.sections))
	for _, title := range p.sections[p.sidebarTop:end] {
		label := "  " + ansi.Truncate(title, max(p.sidebarW-4, 1), "…")
		label = padDisplayWidth(label, p.sidebarW)
		if title == p.section {
			label = styleSelected.Render("› " + strings.TrimPrefix(label, "  "))
		} else {
			label = styleDim.Render(label)
		}
		lines = append(lines, label)
	}
	if p.sidebarTop == 0 && end == len(p.sections) && len(lines)+3 <= height {
		lines = append(lines, "", styleDim.Render("  ↑/↓ select"), styleDim.Render("  pgup/pgdn scroll"))
	}
	return lines
}

// updateKey applies the pane's own navigation and reports whether it took the
// press. Every key it owns only moves a cursor or a window: nothing here posts
// an action, which is what lets the popup hand it unhandled keys and stop
// (task 059 decision 6), while the workspace tab forwards the rest on to the
// task actions.
func (p *detailsPane) updateKey(msg tea.KeyPressMsg) bool {
	page := max(p.h-1, 1)
	switch msg.String() {
	case "up", "k":
		p.moveSection(-1)
	case "down", "j":
		p.moveSection(1)
	case "pgup":
		p.top -= page
	case "pgdown":
		p.top += page
	case "home", "g":
		p.selectSection(0)
	case "end", "G":
		p.selectSection(len(p.sections) - 1)
	default:
		return false
	}
	p.clamp()
	return true
}

// clickSidebar selects the section under a click, where x and y are relative
// to the pane's own top-left corner. It reports whether the click landed on
// the sidebar at all.
func (p *detailsPane) clickSidebar(x, y int) bool {
	row := y - p.sidebarY
	section := p.sidebarTop + row
	if x > p.sidebarW+1 || row < 0 || section >= len(p.sections) {
		return false
	}
	p.selectSection(section)
	return true
}

// scrollAt is the wheel: over the sidebar it walks sections, over the content
// it scrolls the body — the two panes scroll independently by design.
func (p *detailsPane) scrollAt(x, delta int) {
	if x <= p.sidebarW+1 {
		p.moveSection(delta)
		return
	}
	p.top += delta
	p.clamp()
}

func (p *detailsPane) moveSection(delta int) {
	if len(p.sections) == 0 {
		return
	}
	i := 0
	for at, title := range p.sections {
		if title == p.section {
			i = at
			break
		}
	}
	p.selectSection(min(max(i+delta, 0), len(p.sections)-1))
}

func (p *detailsPane) selectSection(i int) {
	if i < 0 || i >= len(p.sections) {
		return
	}
	if p.section == p.sections[i] {
		return
	}
	p.section = p.sections[i]
	p.top = 0
}

func (p *detailsPane) clamp() {
	p.top = min(max(p.top, 0), max(p.count-p.h, 0))
}
