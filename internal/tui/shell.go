package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// selectionSettle is how long the task-table cursor must rest on a row
// before the detail panels fetch it and — for a running task — subscribe to
// its live output. Holding `down` across twenty rows must open one
// subscription, not twenty (PR P decision: the fetch rides the same settle).
const selectionSettle = 250 * time.Millisecond

type (
	// selectionSettledMsg fires when the settle window closes. It carries
	// the task it was armed for, so a window opened for a row the cursor
	// has since left is ignored.
	selectionSettledMsg struct{ id int64 }
	// focusPanelMsg asks the shell to focus one panel.
	focusPanelMsg struct{ id panelID }
	// jumpAttentionMsg asks the shell to jump to the next task needing a
	// human — the root routes `!` here from any screen.
	jumpAttentionMsg struct{}
)

// shell is the fused home screen (§15 layout): the task table, the step
// timeline and the output|diff pane as three panels of one persistent
// screen. It owns layout, focus and the accordion; the board and detail
// sub-models own everything they always did, just rendered into boxes.
type shell struct {
	board  *board
	detail *detail
	// bar is the one §6 action bar both sub-models drive and the footer
	// renders (T3.12).
	bar *actionBar

	focus panelID
	// popup shows the §7.4 answer form over the panels. It never opens
	// itself — `enter` on the awaiting task does (§15: the form announces
	// itself and the human opens it).
	popup bool
	// connected mirrors the root's connection state: false renders the
	// panels marked stale behind a banner instead of hiding them (§15
	// Disconnected).
	connected bool

	// cursor is the task id under the table cursor at the last look;
	// lastSel is the task the detail panels are tracking (or waiting out a
	// settle window for). They differ so an explicit open — a created task
	// not yet in the table — is not immediately torn down by a cursor that
	// has not caught up.
	cursor  int64
	lastSel int64

	// termW/termH are the terminal size (for the §15 too-small line);
	// bodyW/bodyH the area the root hands render.
	termW, termH int
	bodyW, bodyH int

	// lastBoxes and bannerLines are the geometry of the last frame, kept
	// for hit-testing: a click lands on what was on screen, not on what the
	// next layout would be.
	lastBoxes   []box
	bannerLines int
}

func newShell(ctx context.Context) *shell {
	s := &shell{
		board:     newBoard(),
		detail:    newDetail(ctx),
		bar:       &actionBar{},
		connected: true,
	}
	// One bar: a confirmation started from any panel is the same pending
	// question wherever the eye lands, and the footer renders it once.
	s.board.actions = s.bar
	s.detail.actions = s.bar
	// The home screen starts on screen: the detail sub-model is live from
	// the first frame, unlike the old detail view that waited to be opened.
	s.detail.active = true
	return s
}

func (s *shell) title() string { return "Board" }

// setClient wires both sub-models to a connected daemon; called again on
// reconnect.
func (s *shell) setClient(c *apiclient.Client) tea.Cmd {
	return tea.Batch(s.board.setClient(c), s.detail.setClient(c))
}

// setConnected implements connectionAware: the panels stay rendered, the
// banner and stale marks come from this flag.
func (s *shell) setConnected(ok bool) { s.connected = ok }

// hintedProject forwards the board's cursor project to the new-task form.
func (s *shell) hintedProject() int64 { return s.board.hintedProject() }

// capturesInput reports whether a text surface owns the keyboard: the answer
// popup, or the focused panel's own capture (§15: the shell consults the
// focused panel only).
func (s *shell) capturesInput() bool {
	if s.popup {
		return true
	}
	return s.focusedCaptures()
}

// paste hands pasted text to the surface that owns the keyboard: the answer
// popup's free-text field, or the task filter while it is being typed.
func (s *shell) paste(text string) tea.Cmd {
	if s.popup && s.detail.form != nil {
		return s.detail.form.paste(text)
	}
	if s.focus == panelTasks {
		return s.board.paste(text)
	}
	return nil
}

func (s *shell) focusedCaptures() bool {
	if s.focus == panelTasks {
		return s.board.capturesInput()
	}
	return s.detail.capturesInput()
}

func (s *shell) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.termW, s.termH = msg.Width, msg.Height
		return s, s.forward(msg)
	case tea.KeyPressMsg:
		return s.updateKey(msg)
	case tea.MouseClickMsg:
		return s, s.updateClick(msg)
	case tea.MouseWheelMsg:
		return s, s.updateWheel(msg)
	case focusPanelMsg:
		s.focus = msg.id
		return s, nil
	case jumpAttentionMsg:
		return s, s.jumpAttention()
	case selectTaskMsg:
		// An explicit open — task creation, or a caller that knows the id.
		// It skips the settle window: a deliberate action is not a cursor
		// passing through.
		return s, s.openNow(msg.id)
	case selectionSettledMsg:
		if msg.id != s.lastSel || msg.id == s.detail.taskID {
			return s, nil // the cursor moved on, or the task is already open
		}
		return s, s.detail.open(msg.id, s.stateOf(msg.id))
	case viewActivatedMsg:
		if msg.id == viewHome {
			s.detail.active = true
			cmds := []tea.Cmd{s.detail.loadCmd(), s.detail.syncStream()}
			// A settle window that fired while a takeover screen was active
			// was delivered to that screen and lost; coming back with the
			// panels still empty re-opens the tracked row.
			if s.lastSel != 0 && s.detail.taskID != s.lastSel {
				cmds = append(cmds, s.detail.open(s.lastSel, s.stateOf(s.lastSel)))
			}
			return s, tea.Batch(cmds...)
		}
		return s, nil
	case viewDeactivatedMsg:
		if msg.id == viewHome {
			s.detail.active = false
			return s, s.detail.syncStream()
		}
		return s, nil
	}
	// Everything else — loads, notes, ticks, action results — goes to both
	// sub-models; each ignores the other's message types. The board may
	// have moved its cursor (a refresh that dropped the selected row), so
	// the selection is reconciled after.
	return s, tea.Batch(s.forward(msg), s.checkSelection())
}

// forward hands one message to both sub-models and keeps the popup honest:
// a request that was answered or withdrawn takes its form — and therefore
// the popup — with it.
func (s *shell) forward(msg tea.Msg) tea.Cmd {
	_, bc := s.board.update(msg)
	dc := s.detail.update(msg)
	if s.popup && s.detail.form == nil {
		s.popup = false
	}
	return tea.Batch(bc, dc)
}

func (s *shell) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	if s.popup && s.detail.form != nil {
		cmd, exit := s.detail.form.update(msg, s.detail.client, s.detail.taskID)
		if exit {
			s.popup = false
		}
		return s, cmd
	}
	if s.focusedCaptures() {
		// While the filter field is being typed into, tab commits it and
		// moves focus (§15): the filter is view state, not a mode, and
		// glancing at the output pane must not lose it. Only esc clears it.
		if key := msg.String(); (key == "tab" || key == "shift+tab") &&
			s.focus == panelTasks && s.board.filtering {
			s.board.commitFilter()
			if key == "tab" {
				s.cycleFocus(1)
			} else {
				s.cycleFocus(-1)
			}
			return s, nil
		}
		// A filter or a confirmation owns every key; the cursor may still
		// move underneath (a filter narrowing the rows), so reconcile.
		return s, tea.Batch(s.routeKey(msg), s.checkSelection())
	}
	switch msg.String() {
	case "tab":
		s.cycleFocus(1)
		return s, nil
	case "shift+tab":
		s.cycleFocus(-1)
		return s, nil
	case "esc":
		// The shell's layer of the §15 esc stack: clear the active filter,
		// from any panel focus. Below that is nothing — esc never quits,
		// and "back to the board" is not a meaning any more because the
		// board is always on screen; tab is the focus key.
		if s.board.filterActive() {
			s.board.clearFilter()
			return s, s.checkSelection()
		}
		return s, nil
	case "enter":
		if s.openAnswer() {
			return s, nil
		}
		if s.focus == panelTasks {
			return s, s.openSelected()
		}
	case "E":
		// Edit+retry needs the failing step's text, which detail holds —
		// the key acts on the task, so it works from any panel.
		s.syncDetailFocus()
		return s, s.detail.update(msg)
	}
	return s, tea.Batch(s.routeKey(msg), s.checkSelection())
}

// routeKey hands a key to the focused panel's sub-model.
func (s *shell) routeKey(msg tea.KeyPressMsg) tea.Cmd {
	if s.focus == panelTasks {
		_, cmd := s.board.update(msg)
		return cmd
	}
	s.syncDetailFocus()
	return s.detail.update(msg)
}

// syncDetailFocus maps the shell's panel focus onto detail's internal one,
// which decides whether ↑/↓ select an attempt or scroll the pane.
func (s *shell) syncDetailFocus() {
	if s.focus == panelOutput {
		s.detail.focus = focusOutput
	} else {
		s.detail.focus = focusTimeline
	}
}

func (s *shell) cycleFocus(delta int) {
	s.focus = panelID((int(s.focus) + delta + 3) % 3)
}

// openAnswer opens the answer popup when the tracked task has a pending
// request, reporting whether it did. It handles `enter` from any panel: the
// form is an interrupt, and the row badge plus the action-bar hint told the
// human to press it.
func (s *shell) openAnswer() bool {
	d := s.detail
	if d.form == nil || d.taskID == 0 || d.taskID != s.lastSel {
		return false
	}
	s.popup = true
	return true
}

// openSelected is `enter` on the task table: open the row under the cursor
// now — skipping any settle window still pending — and move focus to the
// timeline.
func (s *shell) openSelected() tea.Cmd {
	id, ok := s.board.selected()
	if !ok {
		return nil
	}
	s.focus = panelTimeline
	if s.detail.taskID == id {
		return nil
	}
	s.lastSel = id
	s.cursor = id
	return s.detail.open(id, s.stateOf(id))
}

// openNow selects a task in the table and opens it immediately — the
// explicit path (task creation, live tests). The board may not list the
// task yet; selectedID makes the next refresh put the cursor on it.
func (s *shell) openNow(id int64) tea.Cmd {
	s.board.selectedID = id
	s.board.restoreSelection(s.board.visible())
	s.lastSel = id
	s.focus = panelTimeline
	return s.detail.open(id, s.stateOf(id))
}

// checkSelection reconciles the detail panels with the table cursor. On a
// move the subscription is torn down immediately and the fetch waits out
// the settle window (PR P decision); a cursor that has not moved does
// nothing, so an explicit open of a task the table has not caught up with
// is left alone.
func (s *shell) checkSelection() tea.Cmd {
	id, ok := s.board.selected()
	if !ok {
		id = 0
	}
	if id == s.cursor {
		return nil
	}
	s.cursor = id
	if id == s.lastSel {
		return nil
	}
	s.lastSel = id
	s.detail.deselect()
	if id == 0 {
		return nil
	}
	return tea.Tick(selectionSettle, func(time.Time) tea.Msg {
		return selectionSettledMsg{id: id}
	})
}

// updateClick is §15's click scope on the home screen: click a panel to
// focus it, click a row to select it, click the output panel's title tabs
// to switch them. Coordinates arrive body-relative (the root strips its
// header); the banner line is the shell's own offset. Popups stay keyboard:
// a stray click must not answer a question.
func (s *shell) updateClick(msg tea.MouseClickMsg) tea.Cmd {
	if s.popup {
		return nil
	}
	y := msg.Y - s.bannerLines
	id, ok := hitTest(msg.X, y, s.lastBoxes)
	if !ok {
		return nil
	}
	// The frame that was clicked was rendered with the *old* focus — the
	// output title's tab spans shift by the focus glyph, so remember what
	// was actually on screen before moving focus.
	wasFocusedOutput := s.focus == panelOutput
	s.focus = id
	var b box
	for _, cand := range s.lastBoxes {
		if cand.id == id {
			b = cand
		}
	}
	switch id {
	case panelTasks:
		// The box's top border, then the board's own lines above the table
		// (firstRowLine), then the rows. A click above the rows just focuses.
		line := y - b.y - 1 - s.board.firstRowLine()
		if line >= 0 {
			s.board.clickRow(line)
		}
		return s.checkSelection()
	case panelTimeline:
		s.syncDetailFocus()
		return s.detail.clickTimeline(y - b.y - 1)
	default:
		s.syncDetailFocus()
		return s.detail.clickOutputTitle(msg.X-b.x, y-b.y, wasFocusedOutput)
	}
}

// updateWheel scrolls the focused panel (§15: focused, not hovered).
func (s *shell) updateWheel(msg tea.MouseWheelMsg) tea.Cmd {
	delta := 1
	if msg.Button == tea.MouseWheelUp {
		delta = -1
	}
	switch s.focus {
	case panelTasks:
		s.board.wheelMove(delta)
		return s.checkSelection()
	case panelTimeline:
		s.syncDetailFocus()
		return s.detail.moveSelection(delta)
	default:
		if delta > 0 {
			s.detail.vp.ScrollDown(1)
		} else {
			s.detail.vp.ScrollUp(1)
		}
		s.detail.syncFollowToViewport()
		return nil
	}
}

// jumpAttention moves to the next task needing a human, wrapping through
// the pinned attention rows in board order, and opens it immediately — a
// jump is deliberate, like enter, so it skips the settle window.
func (s *shell) jumpAttention() tea.Cmd {
	var attention []int64
	for _, t := range s.board.visible() {
		if needsAttention(t.State) {
			attention = append(attention, t.ID)
		}
	}
	if len(attention) == 0 {
		return nil
	}
	cur, _ := s.board.selected()
	next := attention[0]
	for i, id := range attention {
		if id == cur && i+1 < len(attention) {
			next = attention[i+1]
			break
		}
	}
	return s.openNow(next)
}

// focusedContext names the focused panel for the binding registry.
func (s *shell) focusedContext() bindingContext {
	switch s.focus {
	case panelTimeline:
		return ctxTimeline
	case panelOutput:
		return ctxOutput
	default:
		return ctxTasks
	}
}

// stateOf reads a task's state off the board's rows — the hint that decides
// whether an open subscribes before the authoritative fetch lands.
func (s *shell) stateOf(id int64) string {
	for _, t := range s.board.tasks {
		if t.ID == id {
			return t.State
		}
	}
	return ""
}

func (s *shell) render(width, height int) string {
	if width > 0 {
		s.bodyW = width
	}
	if height > 0 {
		s.bodyH = height
	}

	areaH := s.bodyH
	var banner string
	if !s.connected {
		banner = styleWarn.Render(" ⚠ daemon unreachable — panels show the last known state · ") +
			styleKey.Render("r") + styleWarn.Render(" retry")
		areaH--
	}

	boxes := layout(s.bodyW, areaH, s.focus)
	// Kept for hit-testing: a click lands on what was on screen.
	s.lastBoxes = boxes
	s.bannerLines = 0
	if banner != "" {
		s.bannerLines = 1
	}
	if boxes == nil {
		return fmt.Sprintf("\n  terminal too small (%d×%d, need %d×%d)",
			s.termW, s.termH, minTermW, minTermH)
	}

	parts := make([]string, 0, 4)
	if banner != "" {
		parts = append(parts, banner)
	}
	if len(boxes) == 1 {
		parts = append(parts, s.renderBox(boxes[0]))
	} else {
		parts = append(parts,
			s.renderBox(boxes[0]),
			lipgloss.JoinHorizontal(lipgloss.Top,
				s.renderBox(boxes[1]), s.renderBox(boxes[2])))
	}
	out := strings.Join(parts, "\n")
	if s.popup && s.detail.form != nil {
		out = s.overlayPopup(out)
	}
	return out
}

func (s *shell) renderBox(b box) string {
	title := s.panelTitle(b.id)
	if !s.connected {
		title += styleDim.Render(" · stale")
	}
	return frame(title, s.panelContent(b.id, b.w-2, b.h-2), b.w, b.h, s.focus == b.id)
}

func (s *shell) panelTitle(id panelID) string {
	switch id {
	case panelTasks:
		// A committed filter is view state, applied and named here (§15) —
		// losing track of why rows are missing trains people to distrust
		// the table.
		if v := s.board.filter.Value(); v != "" && !s.board.filtering {
			return "Tasks — /" + v
		}
		return "Tasks"
	case panelTimeline:
		if s.detail.taskID != 0 {
			return fmt.Sprintf("Timeline — #%d", s.detail.taskID)
		}
		return "Timeline"
	default:
		return s.detail.outputTitle()
	}
}

func (s *shell) panelContent(id panelID, w, h int) string {
	if id == panelTasks {
		return s.board.render(w, h)
	}
	if body, ok := s.detailPlaceholder(); ok {
		return body
	}
	if id == panelTimeline {
		return s.detail.timelinePanel(h)
	}
	return s.detail.outputPanel(w, h)
}

// detailPlaceholder is what the bottom panels show while no task is tracked
// or a settle window is still open — the previous task's content would be a
// lie about the row under the cursor.
func (s *shell) detailPlaceholder() (string, bool) {
	switch {
	case s.lastSel == 0:
		return styleDim.Render("  no task selected"), true
	case s.detail.taskID != s.lastSel:
		return styleDim.Render(fmt.Sprintf("  #%d — loading…", s.lastSel)), true
	default:
		return "", false
	}
}

// overlayPopup draws the answer form over the panels. A popup gets the full
// width a quarter-pane could not give multi-select options and free text
// (§15), and the panels stay visible around it — the tail underneath is
// what says why the agent is asking.
func (s *shell) overlayPopup(bg string) string {
	form := s.detail.form
	pw := min(s.bodyW-6, 76)
	if pw < 20 {
		pw = s.bodyW
	}
	ph := min(form.height()+2, max(s.bodyH-4, 6))
	title := fmt.Sprintf("Answer — #%d", s.detail.taskID)
	popup := frame(title, form.render(ph-2), pw, ph, true)
	x := max((s.bodyW-pw)/2, 0)
	y := max((s.bodyH-ph)/3, 1)
	return overlay(bg, popup, x, y)
}

// frame draws content inside a bordered box of exactly w×h cells, the title
// embedded in the top border. Focus is a colour *and* a glyph, so it
// survives NO_COLOR (§15 Colour). Content lines are ANSI-truncated to the
// inner width — a panel must never leak into its neighbour — and padded to
// the inner height.
func frame(title, content string, w, h int, focused bool) string {
	if w < 2 || h < 2 {
		return ""
	}
	border := styleDim
	label := " " + title + " "
	if focused {
		border = styleFocus
		label = " " + focusGlyph + " " + title + " "
	}
	inner := w - 2

	label = ansi.Truncate(label, max(inner-1, 0), "…")
	fill := inner - 1 - ansi.StringWidth(label)
	var sb strings.Builder
	sb.WriteString(border.Render("┌─"))
	sb.WriteString(label)
	sb.WriteString(border.Render(strings.Repeat("─", max(fill, 0)) + "┐"))

	lines := strings.Split(content, "\n")
	side := border.Render("│")
	for i := range h - 2 {
		var line string
		if i < len(lines) {
			line = ansi.Truncate(lines[i], inner, "…")
		}
		pad := inner - ansi.StringWidth(line)
		sb.WriteString("\n")
		sb.WriteString(side)
		sb.WriteString(line)
		if pad > 0 {
			sb.WriteString(strings.Repeat(" ", pad))
		}
		sb.WriteString(side)
	}
	sb.WriteString("\n")
	sb.WriteString(border.Render("└" + strings.Repeat("─", inner) + "┘"))
	return sb.String()
}

// focusGlyph marks the focused panel's title. A glyph, not just a colour:
// it has to survive a monochrome terminal.
const focusGlyph = "▸"

// overlay splices fg over bg with fg's top-left at column x, row y. The
// background lines are cut ANSI-aware around the box; a background shorter
// than the box clips the box rather than growing the screen.
func overlay(bg, fg string, x, y int) string {
	bgLines := strings.Split(bg, "\n")
	for i, fl := range strings.Split(fg, "\n") {
		r := y + i
		if r < 0 || r >= len(bgLines) {
			break
		}
		line := bgLines[r]
		left := ansi.Truncate(line, x, "")
		if pad := x - ansi.StringWidth(left); pad > 0 {
			left += strings.Repeat(" ", pad)
		}
		right := ansi.TruncateLeft(line, x+ansi.StringWidth(fl), "")
		bgLines[r] = left + fl + right
	}
	return strings.Join(bgLines, "\n")
}
