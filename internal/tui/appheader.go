package tui

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/keymap"

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

// tabGlyph is a screen's icon: a main tab's, shared by the screens it opens.
func tabGlyph(id viewID) string {
	switch id {
	case viewIssues, viewIssue:
		return ic("▦")
	case viewHome, viewTask:
		return ic("▶")
	case viewChats, viewChat, viewCompare:
		return ic("◌")
	case viewProjects:
		return ic(headerProjectGlyph)
	case viewWorkflows:
		return ic("⟡")
	case viewDaemon:
		return ic("◉")
	case viewSettings:
		return ic("⚙")
	case viewPullRequests:
		return ic("⇅")
	case viewTriggers:
		return ic("⚡")
	case viewArchived, viewArchivedChats:
		return ic("▣")
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
	return toneMagenta.Bold(true).Render(ic(headerProjectGlyph) + " " + name)
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
	// The screens menu's key, where the eye looks for where it can go
	// (#1080). It rides with the version, so it is the first thing a narrow
	// header sheds and never costs the tabs room.
	if goTo := opKey(keymap.GoTo); goTo != "" {
		p.version = goTo + " screens · " + p.version
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
//
// An open chat inside the chats workspace keeps the strip too (task 150.5): it
// is still the Chats tab, with the keyboard in the conversation.
func (m *root) stripShown() bool {
	if tab, ok := m.workspaceTab(); ok {
		return len(m.tabs) > 1 && m.isTabRoot(tab)
	}
	return m.isTabRoot(m.active) && len(m.tabs) > 1 && !m.panelOwnsLocalKey("tab")
}

// chatInWorkspace reports that the screen is an open chat between the
// workspace's side panes.
func (m *root) chatInWorkspace() bool {
	v, ok := m.views[m.active].(*chatView)
	return ok && m.active == viewChat && v.frameless()
}

// workspaceTab is the main tab a full-width screen off the tab strip belongs to:
// an open chat, a new chat or a chat compare under Chats, and a new task
// under Tasks. tab moves
// between the form's own fields there, and the strip stays for where you are.
func (m *root) workspaceTab() (viewID, bool) {
	if m.chatInWorkspace() {
		return viewChats, true
	}
	switch v := m.views[m.active].(type) {
	case *newTask:
		return viewHome, v.frameless()
	case *chatsView:
		return viewChats, m.active == viewChats && v.createWide()
	case *compareView:
		// Two chats side by side are still the Chats tab (task 171).
		return viewChats, m.active == viewCompare
	}
	return 0, false
}

// footerSummarizer is a screen that says how it stands on the status bar.
type footerSummarizer interface {
	footerSummary(width int) string
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
		tab, inWorkspace := m.workspaceTab()
		if id == m.active || (inWorkspace && id == tab) {
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
	pill := styleTabActive.Render(" " + tabGlyph(m.active) + " " + title + " ")
	if s, ok := m.views[m.active].(footerSummarizer); ok {
		pill += s.footerSummary(m.width)
	}
	return pill
}
