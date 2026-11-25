package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// rootFooter is the root's footer row, stripped: where the one status line
// is drawn (#1164).
func rootFooter(m *root) string { return ansi.Strip(m.footerLine()) }

// #1164: a note goes on the next key press.
func TestStatusNoteClearsOnTheNextKey(t *testing.T) {
	m := phaseRoot(t, phaseConnected, 120, 30)
	m.status.note("approve: not available while running", false)
	if got := rootFooter(m); !strings.Contains(got, "  approve: not available while running") {
		t.Fatalf("the note does not lead the footer: %q", got)
	}
	m.Update(specialKey(tea.KeyDown))
	if m.status.text != "" || strings.Contains(rootFooter(m), "not available") {
		t.Errorf("the note outlived the next key: %q", m.status.text)
	}
}

// #1164: a note goes on a cursor move made with the mouse, too.
func TestStatusNoteClearsOnAClickOrTheWheel(t *testing.T) {
	for _, msg := range []tea.Msg{
		tea.MouseClickMsg{X: 1, Y: 5, Button: tea.MouseLeft},
		tea.MouseWheelMsg{X: 1, Y: 5, Button: tea.MouseWheelDown},
	} {
		m := phaseRoot(t, phaseConnected, 120, 30)
		m.status.note("nothing to copy", false)
		m.Update(msg)
		if m.status.text != "" {
			t.Errorf("%T: the note outlived it: %q", msg, m.status.text)
		}
	}
}

// #1164: a note goes on a tab change, whatever made it.
func TestStatusNoteClearsOnATabChange(t *testing.T) {
	m := phaseRoot(t, phaseConnected, 120, 30)
	m.status.note("nothing to copy", false)
	m.switchTo(viewChats)
	if m.status.text != "" {
		t.Errorf("the note outlived the tab change: %q", m.status.text)
	}
}

// #1164: a result stays through keys, moves and tab changes until a newer
// message replaces it.
func TestStatusResultStaysUntilReplaced(t *testing.T) {
	m := phaseRoot(t, phaseConnected, 120, 30)
	m.status.result("archive: worktree is dirty", true)
	m.Update(specialKey(tea.KeyDown))
	m.Update(tea.MouseWheelMsg{X: 1, Y: 5, Button: tea.MouseWheelDown})
	m.switchTo(viewChats)
	if m.status.text != "archive: worktree is dirty" || !m.status.bad {
		t.Fatalf("the result did not stay: %q (bad=%v)", m.status.text, m.status.bad)
	}
	if got := rootFooter(m); !strings.Contains(got, "  archive: worktree is dirty") {
		t.Errorf("the result does not lead the footer on another screen: %q", got)
	}
	m.status.result("retry · now running", false)
	if got := rootFooter(m); strings.Contains(got, "dirty") || !strings.Contains(got, "retry · now running") {
		t.Errorf("the newer result did not replace it: %q", got)
	}
}

// #1164: no queue — the newest message wins whatever its kind, and a note
// replacing a result takes the result with it when it goes.
func TestStatusNewestWins(t *testing.T) {
	h := newStatusHolder()
	h.result("cancel · now cancelled", false)
	h.note("approve: not available while cancelled", false)
	if h.text != "approve: not available while cancelled" {
		t.Fatalf("the note did not replace the result: %q", h.text)
	}
	h.dropNote()
	if h.text != "" {
		t.Errorf("the result came back from under the note: %q", h.text)
	}
	h.note("a", false)
	h.result("b", true)
	h.dropNote()
	if h.text != "b" {
		t.Errorf("a result replacing a note was dropped as one: %q", h.text)
	}
}

// #1164: the root hands every view that posts the one holder, so a view's
// message is the root's line.
func TestEveryViewPostsToTheOneLine(t *testing.T) {
	m := phaseRoot(t, phaseConnected, 120, 30)
	n := 0
	for i := range m.views {
		p, ok := m.views[i].(interface{ status() *statusHolder })
		if !ok {
			continue
		}
		n++
		if p.status() != m.status {
			t.Errorf("view %d posts to a line of its own", i)
		}
	}
	if n == 0 {
		t.Fatal("no view posts to the status line")
	}
	if s, ok := m.views[viewHome].(*shell); !ok || s.bar.status() != m.status {
		t.Error("the action bar posts to a line of its own")
	}
}

// #1164: an action bar's "not available" note and result reach the root's
// footer, and the note goes with the next key.
func TestActionBarPostsToTheRootLine(t *testing.T) {
	m := phaseRoot(t, phaseConnected, 120, 30)
	s := m.views[viewHome].(*shell)
	s.bar.setNote("approve: not available while running")
	if got := rootFooter(m); !strings.Contains(got, "approve: not available while running") {
		t.Fatalf("footer %q", got)
	}
	m.Update(specialKey(tea.KeyDown))
	if strings.Contains(rootFooter(m), "not available") {
		t.Error("the bar's note outlived the next key")
	}
	s.bar.setStatus("cancel · now cancelled", false)
	m.Update(specialKey(tea.KeyDown))
	if got := rootFooter(m); !strings.Contains(got, "cancel · now cancelled") {
		t.Errorf("the bar's result did not stay: %q", got)
	}
}

// #1164: questions and banners are not messages. A y/n owns the footer and
// the status line waits under it; the keymap warning and the pause banner
// keep the line under the header.
func TestStatusLineLeavesQuestionsAndBannersAlone(t *testing.T) {
	m := phaseRoot(t, phaseConnected, 120, 30)
	s := m.views[viewHome].(*shell)
	m.status.result("retry · now running", false)
	s.bar.pending, s.bar.pendingID = "cancel", 42
	if got := rootFooter(m); strings.Contains(got, "retry · now running") || !strings.Contains(got, "cancel #42? (y/n)") {
		t.Errorf("the question does not own the footer: %q", got)
	}
	s.bar.resetPending()
	if got := rootFooter(m); !strings.Contains(got, "retry · now running") {
		t.Errorf("the result did not come back after the question: %q", got)
	}

	m.keysNotice = " ⚠ tui.keys: x is bound twice"
	line, ok := m.statusLine()
	if !ok || !strings.Contains(ansi.Strip(line), "tui.keys") {
		t.Errorf("the keymap banner left the header line: %q", ansi.Strip(line))
	}
	if strings.Contains(rootFooter(m), "tui.keys") {
		t.Error("the keymap banner moved into the status line")
	}
}

// #1164: a long error keeps its start at 80 columns; the end is cut.
func TestStatusErrorKeepsItsStartAt80(t *testing.T) {
	m := phaseRoot(t, phaseConnected, 80, 24)
	msg := "comment: the daemon refused the request because the issue is mirrored from GitHub and vincent never posts there"
	m.status.result(msg, true)
	got := rootFooter(m)
	if !strings.HasPrefix(got, " comment: the daemon refused") {
		t.Errorf("the error lost its start: %q", got)
	}
	if w := ansi.StringWidth(got); w > 80 {
		t.Errorf("footer is %d cells", w)
	}
}

// buildFooterStatusLine is a footer at 120 columns leading with h, the way
// the root draws it: what a view's message looks like on screen (#1164).
func buildFooterStatusLine(h *statusHolder) string {
	line, _ := buildFooterStatus(120, nil, nil, taskActions{}, attentionTally{}, false, false, h.shown())
	return ansi.Strip(line)
}
