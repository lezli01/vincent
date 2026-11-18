package tui

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The app header (task 150.4): the selected project, the main tabs with their
// counts, and vincent's version at the far right, over a rule that separates
// the chrome from the screen. Navigation outlives decoration: as the width
// runs out the version goes first, then the elsewhere badge, then the counts,
// then the labels shorten to initials, and only then does the project's name
// truncate. The connection and pause badges are news and are never shed.

// headerRuleMinH is the shortest terminal that draws the rule. Shorter ones
// spend the row on content, and §15's three-pane floor is left where it was.
const headerRuleMinH = minShellH + 1

// headerProjectGlyph marks the selected-project segment of the app header. A
// glyph rather than a colour, so the segment reads the same uncoloured.
const headerProjectGlyph = "◆"

// headerTagFloor is the narrowest the view tag is truncated to before the
// header drops it altogether: below it a tag is an ellipsis, not a name.
const headerTagFloor = 8

// styleTabActive is the tab on screen. Reverse video, so it survives NO_COLOR.
var styleTabActive = lipgloss.NewStyle().Bold(true).Reverse(true)

// tabCounter is a main tab that can say how many things it holds. ok is false
// until it knows: a count of nothing is not the same as no count yet.
type tabCounter interface {
	tabCount() (n int, ok bool)
}

func (v *issuesView) tabCount() (int, bool) { return len(v.issues), v.loaded }

func (v *chatsView) tabCount() (int, bool) { return len(v.chats), v.loaded }

func (s *shell) tabCount() (int, bool) { return len(s.board.projectTasks()), s.board.loaded }

// tabGlyph is a main tab's icon.
func tabGlyph(id viewID) string {
	switch id {
	case viewIssues:
		return "▦"
	case viewHome:
		return "▶"
	case viewChats:
		return "◌"
	}
	return "•"
}

// headerRuleShown reports whether the rule under the header is drawn.
func (m *root) headerRuleShown() bool { return m.height >= headerRuleMinH }

// headerRule is the line between the chrome and the screen.
func (m *root) headerRule() string { return styleDim.Render(strings.Repeat("─", max(m.width, 0))) }

// headerParts is what every arrangement of the header is built from.
type headerParts struct {
	project   string
	badge     string
	elsewhere string
	version   string
}

// segment is the project's name with its glyph.
func (headerParts) segment(name string) string {
	return toneMagenta.Bold(true).Render(headerProjectGlyph + " " + name)
}

// left is the header's start: the project, then what is news about it.
func (p headerParts) left(name, elsewhere string) string {
	return " " + p.segment(name) + elsewhere + p.badge
}

func (m *root) headerLine() string {
	width := m.width
	if width <= 0 {
		width = 1 << 16
	}
	p := headerParts{project: "no project", elsewhere: m.elsewhereBadge(), version: "vincent"}
	if m.sel.id != 0 {
		p.project = m.sel.name
	}
	if m.version != "" {
		p.version += " " + m.version
	}
	// Connected is the normal case and says nothing a working screen does
	// not (task 129.15): the badge is drawn only while it is news. The global
	// pause (task 149) rides beside it, on the same terms.
	if m.phase != phaseConnected {
		p.badge += "  " + m.connBadge()
	}
	if pb := m.globalPause.badge(); pb != "" {
		p.badge += "  " + pb
	}
	m.headerTabs = nil
	// The click target never covers a badge: a click there is not a click on
	// the project.
	m.headerHit = [2]int{1, 1 + ansi.StringWidth(p.segment(p.project))}
	if m.stripShown() {
		return m.headerWithStrip(width, p)
	}
	return m.headerWithTag(width, p)
}

// headerJoin lays left, middle and right along the row, right at the far edge.
func (m *root) headerJoin(width int, left, middle, right string) string {
	line := left + "  " + middle
	if right == "" {
		return line
	}
	pad := 2
	if m.width > 0 {
		pad = max(width-ansi.StringWidth(line)-ansi.StringWidth(right)-1, 2)
	}
	return line + strings.Repeat(" ", pad) + right + " "
}

// headerFits reports whether the joined row is within width.
func headerFits(width int, left, middle, right string) bool {
	need := ansi.StringWidth(left) + 2 + ansi.StringWidth(middle)
	if right != "" {
		need += 2 + ansi.StringWidth(right) + 1
	}
	return need <= width
}

// headerWithTag is the header off a main tab: the screen's own name or
// breadcrumb where the tabs would be.
func (m *root) headerWithTag(width int, p headerParts) string {
	left := p.left(p.project, p.elsewhere)
	version := styleDim.Render(p.version)
	full := styleDim.Render(m.headerTagFull())
	if headerFits(width, left, full, version) {
		return m.headerJoin(width, left, full, version)
	}
	if room := width - ansi.StringWidth(left) - 2; room >= headerTagFloor {
		return left + "  " + styleDim.Render(m.headerTag(room))
	}
	if ansi.StringWidth(left) <= width {
		return left
	}
	if short := p.left(p.project, ""); ansi.StringWidth(short) <= width {
		return short
	}
	room := width - ansi.StringWidth(p.left("", ""))
	name := ansi.Truncate(p.project, max(room, 1), "…")
	m.headerHit = [2]int{1, 1 + ansi.StringWidth(p.segment(name))}
	return p.left(name, "")
}

// headerTabHit is one main-tab strip label's clickable span.
type headerTabHit struct {
	x0, x1 int
	id     viewID
}

// stripShown is where the header draws the main-tab strip instead of the
// view tag (task 136.3): only where tab switches, so the strip never
// advertises a key that means something else on the screen. Input capture
// does not hide it — a filter being typed is transient, and the strip
// should not flicker — so this is tabSwitches without that clause.
func (m *root) stripShown() bool {
	return m.isTabRoot(m.active) && len(m.tabs) > 1 && !m.panelOwnsLocalKey("tab")
}

// tabStripForm is how much a tab's label says.
type tabStripForm int

const (
	tabsCounted tabStripForm = iota // `▦ Issues (10)`
	tabsNamed                       // `▦ Issues`
	tabsInitial                     // `I`
)

// tabLabel is one tab's text in form. Labels are the views' own titles, so the
// strip, the tag and the frame cannot disagree.
func (m *root) tabLabel(id viewID, form tabStripForm) string {
	title := m.views[id].title()
	if form == tabsInitial {
		r, _ := utf8.DecodeRuneInString(title)
		return string(r)
	}
	label := tabGlyph(id) + " " + title
	if c, ok := m.views[id].(tabCounter); ok && form == tabsCounted {
		if n, known := c.tabCount(); known {
			label += " (" + strconv.Itoa(n) + ")"
		}
	}
	return label
}

// tabStrip renders the configured tabs in order, each label's span relative to
// the strip's first cell.
func (m *root) tabStrip(form tabStripForm) (string, []headerTabHit) {
	var b strings.Builder
	hits := make([]headerTabHit, 0, len(m.tabs))
	sep := styleDim.Render("│")
	x := 0
	for i, id := range m.tabs {
		if i > 0 {
			b.WriteString(sep)
			x++
		}
		label := " " + m.tabLabel(id, form) + " "
		style := styleDim
		if id == m.active {
			style = styleTabActive
		}
		b.WriteString(style.Render(label))
		w := ansi.StringWidth(label)
		hits = append(hits, headerTabHit{x0: x, x1: x + w, id: id})
		x += w
	}
	return b.String(), hits
}

// headerWithStrip is the header on a main tab (task 136.3). The strip never
// vanishes.
func (m *root) headerWithStrip(width int, p headerParts) string {
	place := func(left string, spans []headerTabHit) {
		x0 := ansi.StringWidth(left) + 2
		m.headerTabs = make([]headerTabHit, len(spans))
		for i, h := range spans {
			m.headerTabs[i] = headerTabHit{x0: x0 + h.x0, x1: x0 + h.x1, id: h.id}
		}
	}
	version := styleDim.Render(p.version)
	for _, c := range []struct {
		right, elsewhere string
		form             tabStripForm
	}{
		{version, p.elsewhere, tabsCounted},
		{"", p.elsewhere, tabsCounted},
		{"", "", tabsCounted},
		{"", "", tabsNamed},
		{"", "", tabsInitial},
	} {
		left := p.left(p.project, c.elsewhere)
		strip, spans := m.tabStrip(c.form)
		if headerFits(width, left, strip, c.right) {
			place(left, spans)
			return m.headerJoin(width, left, strip, c.right)
		}
	}
	strip, spans := m.tabStrip(tabsInitial)
	room := width - ansi.StringWidth(p.left("", "")) - 2 - ansi.StringWidth(strip)
	name := ansi.Truncate(p.project, max(room, 1), "…")
	m.headerHit = [2]int{1, 1 + ansi.StringWidth(p.segment(name))}
	left := p.left(name, "")
	place(left, spans)
	return left + "  " + strip
}

// footerPillMinW is the narrowest terminal whose status bar names the screen.
const footerPillMinW = 100

// footerPillMaxTitle caps the title in the pill: a chat is titled by its
// conversation, and the keys beside it matter more.
const footerPillMaxTitle = 24

// footerPill names the screen at the start of the status bar, "" on a terminal
// too narrow to spare the room.
func (m *root) footerPill() string {
	if m.width < footerPillMinW {
		return ""
	}
	title := ansi.Truncate(m.views[m.active].title(), footerPillMaxTitle, "…")
	return styleTabActive.Render(" " + tabGlyph(m.active) + " " + title + " ")
}
