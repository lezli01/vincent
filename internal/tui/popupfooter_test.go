package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestRootPopupFooterNamesItsOwnKeys is #1180: a root popup sends every key
// but ctrl+c to itself, so the footer under it must offer that popup's own
// keys — read from the registry — and never the pinned `: commands  ? help
// q quit`, which would type into its query or be dropped.
func TestRootPopupFooterNamesItsOwnKeys(t *testing.T) {
	cases := []struct {
		name string
		open func(m *root)
		want []string
	}{
		{"palette", func(m *root) { m.Update(key(":")) }, []string{"↑↓ move", "enter run", "esc close"}},
		{"screens menu", func(m *root) { m.Update(key("`")) }, []string{"letter go", "↑↓ move", "enter go", "esc close"}},
		{"project picker", func(m *root) {
			m.projPick = newProjectPicker([]apiclient.Project{testProject(1, "api"), testProject(2, "web")}, 1, "")
		}, []string{"↑↓ move", "enter switch", "esc close"}},
		{"copy picker", func(m *root) {
			m.reader = newReaderPicker([]copyItem{{group: "message 1", label: "markdown", snippet: "the whole answer"}})
		}, []string{"↑↓ move", "enter copy", "esc close"}},
		{"link picker", func(m *root) {
			m.linkPick = newLinkPicker([]linkItem{{group: "message 1", link: mdLink{n: 1, label: "docs", dest: "https://example.com"}}})
		}, []string{"↑↓ move", "enter open", "ctrl+y copy", "esc close"}},
		{"export form", func(m *root) {
			m.exportForm = newChatExportForm(7, "Fix the build", t.TempDir())
		}, []string{"↑↓ move", "space change", "enter export", "esc close"}},
		{"export form, written", func(m *root) {
			m.exportForm = newChatExportForm(7, "Fix the build", t.TempDir())
			m.exportForm.written = "/tmp/x.md"
		}, []string{"↑↓ move", "enter copy", "esc close"}},
		{"rename prompt", func(m *root) {
			m.rename = newChatRenamePrompt(7, "Fix the build")
		}, []string{"enter rename", "esc cancel"}},
		{"chat with", func(m *root) {
			m.chatWith = newChatWithForm(chatWithTarget{})
		}, []string{"↑↓ move", "enter choose", "ctrl+s create", "esc close"}},
		{"chat with, list open", func(m *root) {
			m.chatWith = newChatWithForm(chatWithTarget{})
			m.chatWith.pick = newPicker(int(cwAgent), "agent", []pickerOption{{value: "claude", label: "claude"}}, false, "")
		}, []string{"↑↓ move", "enter pick", "esc close list"}},
		{"draft question", func(m *root) {
			m.leave = &pendingLeave{draft: "draft", quit: true}
		}, []string{"y yes", "n no"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := connectedRoot(t)
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
			c.open(m)
			if !m.popupOpen() {
				t.Fatal("the popup did not open")
			}
			raw := m.footerLine()
			if w := ansi.StringWidth(raw); w > 80 {
				t.Errorf("the footer is %d cells at 80 columns", w)
			}
			line := ansi.Strip(raw)
			for _, w := range c.want {
				if !strings.Contains(line, w) {
					t.Errorf("the footer lacks %q: %q", w, line)
				}
			}
			if !strings.HasSuffix(strings.TrimRight(line, " "), "ctrl+c quit") {
				t.Errorf("the footer does not end on ctrl+c quit: %q", line)
			}
			for _, gone := range []string{": commands", "? help", "q quit", "ctrl+p", "f1 help"} {
				if strings.Contains(line, gone) {
					t.Errorf("the footer offers %q, which the popup swallows: %q", gone, line)
				}
			}
		})
	}
}

// A deleted selection's switch cannot be declined (task 132.7): its
// question waits for y, so the footer offers no `n`.
func TestDraftQuestionFooterOmitsNoWhenItCannotBeDeclined(t *testing.T) {
	m := connectedRoot(t)
	m.pending = &pendingSwitch{project: testProject(2, "web"), draft: "draft", deleted: "api"}
	line := ansi.Strip(m.footerLine())
	if !strings.Contains(line, "y yes") || strings.Contains(line, "n no") {
		t.Errorf("a switch off a deleted project offers %q, want y alone", line)
	}
}
