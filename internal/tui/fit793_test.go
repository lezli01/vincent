package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The #793 reproductions: the root's chrome — the connection screens, the
// status line and the first-run notice — fitted to the terminal, and the
// alternate screen the code comments assume.

// errLongDial is a dial error the length of a real one (131 columns).
var errLongDial = errors.New("dial tcp 127.0.0.1:53917: connect: connection refused (is the daemon running? check the log below and `vincent doctor`)  ")

const longLogPath = "/Users/someone/Library/Application Support/vincent/data/logs/daemon-2026-10-20.log"

// phaseRoot is an acknowledged root sized w×h in the given connection phase,
// with no board ever loaded.
func phaseRoot(t *testing.T, phase connPhase, w, h int) *root {
	t.Helper()
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	switch phase {
	case phaseFailed:
		m.Update(connectFailedMsg{err: errLongDial, logPath: longLogPath})
	case phaseReconnecting:
		m.phase = phaseReconnecting
		m.connErr = errLongDial
	default:
		m.phase = phase
	}
	return m
}

var fitPhases = []struct {
	name  string
	phase connPhase
}{
	{"probing", phaseProbing},
	{"starting", phaseStarting},
	{"connected", phaseConnected},
	{"reconnecting", phaseReconnecting},
	{"failed", phaseFailed},
}

var fitSizes = [][2]int{{60, 15}, {80, 24}, {200, 60}}

// assertFits is the invariant: no line wider than the terminal, and the
// frame exactly the terminal's height.
func assertFits(t *testing.T, frame string, w, h int) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != h {
		t.Errorf("frame is %d lines, want exactly %d", len(lines), h)
	}
	for i, l := range lines {
		if lw := ansi.StringWidth(l); lw > w {
			t.Errorf("line %d is %d cells wide, terminal is %d: %q", i, lw, w, ansi.Strip(l))
		}
	}
}

// C1: bubbletea v2 runs inline unless the view asks for the alt screen.
func TestRootViewAsksForTheAltScreen(t *testing.T) {
	for _, p := range fitPhases {
		m := phaseRoot(t, p.phase, 80, 24)
		if !m.View().AltScreen {
			t.Errorf("%s: View().AltScreen is false", p.name)
		}
	}
	n := newRoot(testCtx(t), fakeConnector(), t.TempDir())
	n.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !n.notice.active {
		t.Fatal("a fresh data dir did not raise the first-run notice")
	}
	if !n.View().AltScreen {
		t.Error("first-run notice: View().AltScreen is false")
	}
}

// C2, C3 and the invariant test of the issue's step 7: every phase, bare and
// with each root popup open, at the 60×15 floor and two larger sizes.
func TestRootFrameFitsTheTerminal(t *testing.T) {
	overlays := []struct {
		name string
		open func(m *root)
	}{
		{"bare", func(*root) {}},
		{"help", func(m *root) { m.Update(key("?")) }},
		{"palette", func(m *root) { m.Update(key(":")) }},
	}
	for _, sz := range fitSizes {
		w, h := sz[0], sz[1]
		for _, p := range fitPhases {
			for _, o := range overlays {
				t.Run(fmt.Sprintf("%dx%d/%s/%s", w, h, p.name, o.name), func(t *testing.T) {
					m := phaseRoot(t, p.phase, w, h)
					o.open(m)
					assertFits(t, m.View().Content, w, h)
				})
			}
		}
	}
}

// C2: the palette over the failure screen must not be clipped by the short
// frame — the failure text sends the human to it for the daemon view.
func TestPaletteReachesTheDaemonViewWhileFailed(t *testing.T) {
	for _, phase := range []connPhase{phaseProbing, phaseFailed} {
		m := phaseRoot(t, phase, 80, 24)
		m.Update(key(":"))
		if m.palette == nil {
			t.Fatalf("phase %d: `:` did not open the palette", phase)
		}
		if got := ansi.Strip(m.View().Content); !strings.Contains(strings.ToLower(got), "daemon") {
			t.Errorf("phase %d: the palette on screen names no daemon entry:\n%s", phase, got)
		}
	}
}

// C3: at the 60-column floor the error and the log path are readable in full.
func TestFailureScreenWrapsTheErrorAndLogPath(t *testing.T) {
	m := phaseRoot(t, phaseFailed, 60, 15)
	// The renderer truncates an overflowing line (#793 renderer clip), so
	// what is readable is each line's first 60 cells.
	var kept []string
	for _, l := range strings.Split(m.View().Content, "\n") {
		kept = append(kept, ansi.Strip(ansi.Truncate(l, 60, "")))
	}
	flat := strings.Join(strings.Fields(strings.Join(kept, "\n")), "")
	for _, want := range []string{longLogPath, strings.TrimSpace(errLongDial.Error())} {
		if !strings.Contains(flat, strings.Join(strings.Fields(want), "")) {
			t.Errorf("at 60×15 the failure screen does not show %q in full", want)
		}
	}
}

// C4: the hidden board must not take keys behind the failure screen; `/`
// then `r` still retries, and the footer offers no board rows.
func TestFailureScreenOwnsItsKeys(t *testing.T) {
	m := phaseRoot(t, phaseFailed, 80, 24)
	footer := ansi.Strip(m.footerLine())
	for _, row := range []string{"filter", "select", "open"} {
		if strings.Contains(footer, row) {
			t.Errorf("the failure screen's footer offers the hidden board's %q: %q", row, footer)
		}
	}
	m.Update(key("/"))
	m.Update(key("r"))
	if m.phase != phaseProbing {
		t.Errorf("after `/` then `r` the phase is %d, want probing (a retry)", m.phase)
	}
}

// Review F1: a reconnect that lands while a chat draft is being typed must
// not read the next keystrokes as commands — `q` quitting past the draft
// most of all. The screen's own `r` still retries, and the footer and the
// screen name the keys that work over a hidden text field.
func TestReconnectOverADraftSwallowsTyping(t *testing.T) {
	m, v := chatWithDraft(t)
	m.phase = phaseReconnecting
	m.connErr = errLongDial
	m.setConnected(false)
	if !m.connScreen() {
		t.Fatal("fixture: the chat is not behind a connection screen")
	}
	for _, k := range []string{"q", ":", "?", "n"} {
		if _, cmd := m.Update(key(k)); isQuit(cmd) {
			t.Fatalf("%q quit the TUI over a hidden chat draft", k)
		}
		if m.palette != nil || m.help {
			t.Fatalf("%q opened an overlay over a hidden chat draft", k)
		}
	}
	if got := v.composer.Value(); got != "hello" {
		t.Errorf("the hidden composer changed to %q", got)
	}
	if footer := ansi.Strip(m.footerLine()); !strings.Contains(footer, "ctrl+c") {
		t.Errorf("the footer does not pin ctrl+c over a hidden text field: %q", footer)
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); !isQuit(cmd) {
		t.Error("ctrl+c did not quit from the connection screen")
	}
	m.Update(key("r"))
	if m.phase == phaseReconnecting {
		t.Error("`r` did not restart the connection over a hidden chat draft")
	}
}

// inputSpy is a text-capturing panel that records what reaches it.
type inputSpy struct{ got []tea.Msg }

func (s *inputSpy) title() string          { return "spy" }
func (s *inputSpy) render(_, _ int) string { return "" }
func (s *inputSpy) capturesInput() bool    { return true }
func (s *inputSpy) update(msg tea.Msg) (panel, tea.Cmd) {
	s.got = append(s.got, msg)
	return s, nil
}

func (s *inputSpy) paste(text string) tea.Cmd {
	s.got = append(s.got, tea.PasteMsg{Content: text})
	return nil
}

// Review F3: the view behind a connection screen receives nothing — not a
// bracketed paste, not ctrl+v's clipboard read, not a click in the body.
func TestConnectionScreenHidesPasteAndClicks(t *testing.T) {
	m := phaseRoot(t, phaseReconnecting, 80, 24)
	spy := &inputSpy{}
	m.views[m.active] = spy
	if !m.connScreen() {
		t.Fatal("fixture: no connection screen")
	}
	m.Update(tea.PasteMsg{Content: "XYZ"})
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}); cmd != nil {
		t.Error("ctrl+v read the clipboard for a view the connection screen hides")
	}
	m.Update(tea.MouseClickMsg{X: 10, Y: 5, Button: tea.MouseLeft})
	if len(spy.got) != 0 {
		t.Errorf("the hidden view received %#v", spy.got)
	}
}

// Review F4: `?` and `:` on the connection screen describe the connection
// screen — retry and the root's keys — never the hidden board's rows, which
// the screen's key gate drops.
func TestConnectionScreenHelpAndPaletteAreItsOwn(t *testing.T) {
	m := phaseRoot(t, phaseFailed, 80, 40)
	m.Update(key("?"))
	sheet := ansi.Strip(m.View().Content)
	if !strings.Contains(sheet, "Help — connection") || !strings.Contains(sheet, "retry the connection now") {
		t.Errorf("help over the connection screen is not about it:\n%s", sheet)
	}
	for _, hidden := range []string{"TASK TABLE", "│  /  ", "│  space"} {
		if strings.Contains(sheet, hidden) {
			t.Errorf("help over the connection screen lists the hidden board's %q:\n%s", hidden, sheet)
		}
	}
	m.Update(key("?"))
	m.Update(key(":"))
	if m.palette == nil {
		t.Fatal("`:` did not open the palette")
	}
	retry := false
	for _, e := range m.palette.entries {
		if e.group == string(ctxTasks) {
			t.Errorf("the palette offers the hidden board's %q", e.label)
		}
		if e.group == string(ctxConnection) && e.key == "r" {
			retry = true
		}
	}
	if !retry {
		t.Error("the palette over the connection screen offers no retry")
	}
}

// C5: the status line fits the width, and the pending switch's key survives.
func TestStatusLineFitsWithLongProjectNames(t *testing.T) {
	name := strings.Repeat("p", 40)
	gone := strings.Repeat("g", 40)
	for _, w := range []int{60, 80} {
		m := phaseRoot(t, phaseConnected, w, 24)
		m.pending = &pendingSwitch{
			project: apiclient.Project{ID: 2, Name: name},
			draft:   "task draft",
			deleted: gone,
		}
		line, ok := m.statusLine()
		if !ok {
			t.Fatal("no status line with a switch pending")
		}
		if lw := ansi.StringWidth(line); lw > w {
			t.Errorf("at %d columns the pending-switch line is %d cells", w, lw)
		}
		// What the renderer keeps is the first w cells; the answer key must
		// be among them.
		if vis := ansi.Strip(ansi.Truncate(line, w, "")); !strings.Contains(vis, "? y") && !strings.HasPrefix(strings.TrimSpace(vis), "y") {
			t.Errorf("at %d columns the visible prompt loses its key: %q", w, vis)
		}

		m.pending = nil
		m.selNotice = strings.Repeat("startup project notice ", 7)
		line, _ = m.statusLine()
		if lw := ansi.StringWidth(line); lw > w {
			t.Errorf("at %d columns the startup notice is %d cells", w, lw)
		}
	}
}

// Review F5/F6: every variant of the pending-switch question keeps its key
// and the words that say what the key does — "discard" — at the floor and
// at 80 columns, however long the project names.
func TestPendingSwitchPromptKeepsItsMeaning(t *testing.T) {
	long := strings.Repeat("p", 40)
	gone := strings.Repeat("g", 40)
	variants := []struct {
		name string
		p    pendingSwitch
		key  string
	}{
		{"plain switch", pendingSwitch{project: apiclient.Project{ID: 2, Name: long}, draft: "task draft"}, "y/n"},
		{"deleted, switch elsewhere", pendingSwitch{project: apiclient.Project{ID: 2, Name: long}, draft: "task draft", deleted: gone}, "y"},
		{"deleted, none remain", pendingSwitch{project: apiclient.Project{ID: 0}, draft: "task draft", deleted: gone}, "y"},
	}
	for _, v := range variants {
		for _, w := range []int{60, 80} {
			line := ansi.Strip(v.p.promptFit(w))
			if lw := ansi.StringWidth(line); lw > w {
				t.Errorf("%s at %d: %d cells: %q", v.name, w, lw, line)
			}
			if !strings.Contains(line, "discard") {
				t.Errorf("%s at %d: the line loses what the key does: %q", v.name, w, line)
			}
			if !strings.HasSuffix(line, " "+v.key) && !strings.HasPrefix(line, v.key+" — ") {
				t.Errorf("%s at %d: the answer key %q is not at either end: %q", v.name, w, v.key, line)
			}
		}
	}
}

// C8: the first-run notice fits the 60×15 floor with its dismiss key shown.
func TestFirstRunNoticeFitsTheFloor(t *testing.T) {
	for i, l := range strings.Split(noticeBody, "\n") {
		if lw := ansi.StringWidth(l); lw > 58 {
			t.Errorf("noticeBody line %d is %d cells, want ≤ 58: %q", i, lw, l)
		}
	}
	m := newRoot(testCtx(t), fakeConnector(), t.TempDir())
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 15})
	frame := m.View().Content
	assertFits(t, frame, 60, 15)
	if !strings.Contains(ansi.Strip(frame), "enter continue") {
		t.Error("`enter continue` is not on the 60×15 first-run frame")
	}
}
