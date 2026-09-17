package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// f1 is the press a terminal delivers for the help key's alternate.
func f1() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyF1} }

// chatWithDraft is the chat workspace with "hello" in the composer: the
// surface where `?` had typed itself into the draft (issue #405).
func chatWithDraft(t *testing.T) (*root, *chatView) {
	t.Helper()
	m := connectedChatRoot(t)
	v := m.views[viewChat].(*chatView)
	for _, r := range "hello" {
		m.Update(key(string(r)))
	}
	if got := v.composer.Value(); got != "hello" {
		t.Fatalf("fixture draft is %q, want hello", got)
	}
	return m, v
}

// isQuit reports whether cmd is tea.Quit.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestF1OpensHelpFromAChat is task 112 decision 1: help is reachable from the
// chat composer, and reaching it leaves the draft alone.
func TestF1OpensHelpFromAChat(t *testing.T) {
	m, v := chatWithDraft(t)
	m.Update(f1())
	if !m.help {
		t.Fatal("f1 did not open help from a chat")
	}
	if got := v.composer.Value(); got != "hello" {
		t.Fatalf("f1 reached the composer: %q", got)
	}
	if !strings.Contains(content(m), "GLOBAL KEYS") {
		t.Fatalf("f1 set the flag but no help is drawn:\n%s", content(m))
	}
	// And `?` is still a character there.
	m.Update(f1())
	m.Update(key("?"))
	if m.help || v.composer.Value() != "hello?" {
		t.Fatalf("? in the composer: help=%v draft=%q, want no help and the ? typed", m.help, v.composer.Value())
	}
}

// TestHelpOverChatOwnsTheKeyboard is decision 2 over the surface where it
// matters most: a key under the sheet must neither type into a draft nobody
// can see nor leave the chat.
func TestHelpOverChatOwnsTheKeyboard(t *testing.T) {
	for _, c := range []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{"?", key("?")},
		{"f1", f1()},
		{"esc", tea.KeyPressMsg{Code: tea.KeyEscape}},
	} {
		t.Run("closes on "+c.name, func(t *testing.T) {
			m, v := chatWithDraft(t)
			m.Update(f1())
			m.Update(c.msg)
			if m.help {
				t.Fatalf("%s did not close help", c.name)
			}
			if m.active != viewChat {
				t.Fatalf("%s left the chat for %v", c.name, m.active)
			}
			if got := v.composer.Value(); got != "hello" {
				t.Fatalf("%s reached the composer: %q", c.name, got)
			}
		})
	}

	t.Run("swallows the rest", func(t *testing.T) {
		m, v := chatWithDraft(t)
		m.Update(f1())
		for _, k := range []tea.KeyPressMsg{key("x"), key("q"), {Code: tea.KeyEnter}, {Code: 'p', Mod: tea.ModCtrl}} {
			_, cmd := m.Update(k)
			if cmd != nil {
				t.Fatalf("%s under help returned a command", k.String())
			}
			if !m.help {
				t.Fatalf("%s closed help", k.String())
			}
		}
		if got := v.composer.Value(); got != "hello" {
			t.Fatalf("keys under help reached the composer: %q", got)
		}
		if m.palette != nil || m.active != viewChat {
			t.Fatalf("keys under help acted: palette=%v active=%v", m.palette != nil, m.active)
		}
	})

	t.Run("ctrl+c still quits", func(t *testing.T) {
		m, _ := chatWithDraft(t)
		m.Update(f1())
		if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); !isQuit(cmd) {
			t.Fatal("ctrl+c under help did not quit")
		}
	})
}

// boardRoot is the home board with three tasks and the cursor on the first.
func boardRoot(t *testing.T) (*root, *shell) {
	t.Helper()
	m := connectedRoot(t)
	m.Update(boardLoadedMsg{tasks: []apiclient.Task{
		task(3, "running"), task(2, "running"), task(1, "running"),
	}})
	s := m.views[viewHome].(*shell)
	if _, ok := s.board.selected(); !ok {
		t.Fatal("fixture board selects no task")
	}
	return m, s
}

// TestF1TogglesHelpOnTheBoard: f1 is not only for text fields — it is `?`
// wherever it is pressed.
func TestF1TogglesHelpOnTheBoard(t *testing.T) {
	m, _ := boardRoot(t)
	m.Update(f1())
	if !m.help {
		t.Fatal("f1 did not open help on the board")
	}
	m.Update(f1())
	if m.help {
		t.Fatal("f1 did not close help on the board")
	}
}

// TestHelpOverBoardSwallowsKeys is decision 2's accepted behaviour change:
// the arrows, enter and q no longer act on the board behind the sheet.
func TestHelpOverBoardSwallowsKeys(t *testing.T) {
	m, s := boardRoot(t)
	before, _ := s.board.selected()
	m.Update(key("?"))
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyDown}, {Code: tea.KeyEnter}, key("q")} {
		if _, cmd := m.Update(k); cmd != nil {
			t.Fatalf("%s under help returned a command", k.String())
		}
	}
	if after, _ := s.board.selected(); after != before {
		t.Fatalf("the cursor moved under help: %d → %d", before, after)
	}
	if m.active != viewHome || !m.help {
		t.Fatalf("keys under help acted: active=%v help=%v", m.active, m.help)
	}
}

// TestF1WorksInABoardFilter: the filter is a text field, and help reaches it.
func TestF1WorksInABoardFilter(t *testing.T) {
	m, s := boardRoot(t)
	m.Update(key("/"))
	if !m.activeCapturesInput() {
		t.Fatal("/ did not open the filter")
	}
	m.Update(key("2"))
	m.Update(f1())
	if !m.help {
		t.Fatal("f1 did not open help from the filter")
	}
	if got := s.board.filter.Value(); got != "2" {
		t.Fatalf("f1 reached the filter: %q", got)
	}
}

// runPaletteRow opens the palette with ctrl+p and runs the row labelled label.
func runPaletteRow(t *testing.T, m *root, label string) tea.Cmd {
	t.Helper()
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.palette == nil {
		t.Fatal("ctrl+p did not open the palette")
	}
	for i, e := range m.palette.matches() {
		if e.label == label {
			m.palette.cursor = i
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			return cmd
		}
	}
	t.Fatalf("the palette offers no %q row", label)
	return nil
}

// TestPaletteGlobalRowsActFromAChat is decision 3: a global row run from the
// palette in a chat does what its label says rather than typing its key into
// the draft.
func TestPaletteGlobalRowsActFromAChat(t *testing.T) {
	t.Run("help", func(t *testing.T) {
		m, v := chatWithDraft(t)
		runPaletteRow(t, m, "toggle this help")
		if !m.help {
			t.Fatal("the help row did not open help")
		}
		if got := v.composer.Value(); got != "hello" {
			t.Fatalf("the help row typed into the draft: %q", got)
		}
	})
	t.Run("quit", func(t *testing.T) {
		m, v := chatWithDraft(t)
		if cmd := runPaletteRow(t, m, "quit the TUI (the daemon keeps running)"); !isQuit(cmd) {
			t.Fatal("the quit row did not quit")
		}
		if got := v.composer.Value(); got != "hello" {
			t.Fatalf("the quit row typed into the draft: %q", got)
		}
	})
	t.Run("mouse", func(t *testing.T) {
		m, v := chatWithDraft(t)
		was := m.mouseOn
		runPaletteRow(t, m, "toggle the mouse (native text selection needs it off — or shift-drag)")
		if m.mouseOn == was {
			t.Fatal("the mouse row did not toggle the mouse")
		}
		if got := v.composer.Value(); got != "hello" {
			t.Fatalf("the mouse row typed into the draft: %q", got)
		}
	})
	t.Run("new task", func(t *testing.T) {
		m, v := chatWithDraft(t)
		runPaletteRow(t, m, "new task — for the project you are looking at")
		if m.active != viewNewTask {
			t.Fatalf("the new-task row left the root on %v", m.active)
		}
		if got := v.composer.Value(); got != "hello" {
			t.Fatalf("the new-task row typed into the draft: %q", got)
		}
	})
	t.Run("attention while disconnected", func(t *testing.T) {
		// A global key the root does not consume in its state is dropped, not
		// handed to the field that would type it.
		m, v := chatWithDraft(t)
		m.phase = phaseReconnecting
		runPaletteRow(t, m, "jump to the next task needing a human")
		if got := v.composer.Value(); got != "hello" {
			t.Fatalf("the ! row typed into the draft: %q", got)
		}
	})
}

// footerText renders the root's footer line as plain text and returns it with
// its clickable spans.
func footerText(m *root) (string, []footerHit) {
	line := ansi.Strip(m.footerLine())
	return line, m.footerHits
}

// TestFooterPinnedNamesTheKeysThatWork is decision 4: where a text field has
// the keyboard, the pinned part names the keys that reach past it.
func TestFooterPinnedNamesTheKeysThatWork(t *testing.T) {
	m, v := chatWithDraft(t)
	line, hits := footerText(m)
	if !strings.HasSuffix(line, "ctrl+p commands  f1 help  ctrl+c quit") {
		t.Fatalf("the chat footer's pinned part is not the text-field one:\n%q", line)
	}
	var help *footerHit
	for i := range hits {
		if hits[i].key == helpAltKey {
			help = &hits[i]
		}
	}
	if help == nil {
		t.Fatalf("no clickable f1 span in %+v", hits)
	}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: help.x0, Y: m.height - 1})
	if !m.help {
		t.Fatal("clicking f1 help did not open help")
	}
	if got := v.composer.Value(); got != "hello" {
		t.Fatalf("clicking f1 help typed into the draft: %q", got)
	}

	board, _ := boardRoot(t)
	if line, _ := footerText(board); !strings.HasSuffix(line, ": commands  ? help  q quit") {
		t.Fatalf("the board footer's pinned part changed:\n%q", line)
	}
}

// TestFooterClickReplaysGlobalKeysPastTheField: clicking the pinned quit in a
// chat quits, and nothing reaches the draft on the way.
func TestFooterClickReplaysGlobalKeysPastTheField(t *testing.T) {
	m, v := chatWithDraft(t)
	_, hits := footerText(m)
	clicked := false
	for _, h := range hits {
		if h.key == "ctrl+c" {
			clicked = true
			if _, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: h.x0, Y: m.height - 1}); !isQuit(cmd) {
				t.Fatal("clicking ctrl+c quit did not quit")
			}
		}
	}
	if !clicked {
		t.Fatalf("no clickable ctrl+c span in %+v", hits)
	}
	if got := v.composer.Value(); got != "hello" {
		t.Fatalf("a footer click typed into the draft: %q", got)
	}
}

// TestFooterTextFieldPinnedNeverTruncates is task 094's width guarantee for the
// wider pinned part: at every width it is whole, and the hints pay for it.
func TestFooterTextFieldPinnedNeverTruncates(t *testing.T) {
	const pinned = "ctrl+p commands  f1 help  ctrl+c quit"
	rows := bindingsFor(ctxTasks)
	for _, width := range []int{20, 40, 60, 80, 120, 200} {
		line, hits := buildFooter(width, rows, &actionBar{}, footerTarget, 3, true, true)
		plain := ansi.Strip(line)
		if !strings.HasSuffix(plain, pinned) {
			t.Fatalf("width %d: the pinned part was cut: %q", width, plain)
		}
		if w := ansi.StringWidth(plain); w > width && width > ansi.StringWidth(pinned)+2 {
			t.Fatalf("width %d: the line is %d wide: %q", width, w, plain)
		}
		n := 0
		for _, h := range hits {
			if h.global && (h.key == paletteAltKey || h.key == helpAltKey || h.key == "ctrl+c") {
				n++
			}
		}
		if n != 3 {
			t.Fatalf("width %d: %d of the three pinned spans are clickable", width, n)
		}
	}
}

// TestSynthKeyRoundTripsEveryRegistryKey: a palette row or a footer span fires
// its key through synthKey, so a key it cannot rebuild is a row that does
// something else — the ctrl+r/ctrl+g defect task 076 found, and f1 read as `f`.
func TestSynthKeyRoundTripsEveryRegistryKey(t *testing.T) {
	for _, b := range bindings {
		if b.key == "" {
			continue
		}
		if got := synthKey(b.key).String(); got != b.key {
			t.Errorf("synthKey(%q).String() = %q", b.key, got)
		}
	}
	if got := synthKey(helpAltKey); got.Code != tea.KeyF1 || got.Text != "" {
		t.Fatalf("synthKey(f1) = %+v, want the F1 key rather than a letter", got)
	}
}

// TestHelpListsF1UnderGlobalKeys: the overlay renders the new row, on a
// surface where it is the way in.
func TestHelpListsF1UnderGlobalKeys(t *testing.T) {
	text := helpText(ctxChat, false)
	_, global, ok := strings.Cut(text, "GLOBAL KEYS")
	if !ok {
		t.Fatalf("no global section:\n%s", text)
	}
	if !strings.Contains(global, "f1") || !strings.Contains(global, "works while a text field has the keyboard") {
		t.Fatalf("the global section does not list f1:\n%s", global)
	}
}
