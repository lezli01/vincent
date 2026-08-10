package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
)

// Paste reaches a TUI two ways, and both end up as a tea.PasteMsg the root
// routes to the focused field (see pasteReceiving):
//
//   - Bracketed paste. The terminal wraps pasted text in an escape sequence
//     and bubbletea delivers it as tea.PasteMsg. This is what Cmd+V on macOS
//     and right-click/Ctrl+Shift+V elsewhere produce, and it needs no
//     clipboard access at all — the terminal already has the text.
//   - ctrl+v, for terminals that pass the key through instead of pasting for
//     you. There the TUI has to read the system clipboard itself.
//
// bubbles' textinput binds ctrl+v to its own Paste command, but that command
// answers with an unexported message type only textinput understands, so a
// root that routes by message type cannot deliver it. Reading the clipboard
// here keeps one path into the field.

// readClipboardCmd reads the system clipboard as a paste. A failure is
// silent: the platforms where reading needs a helper binary (Linux wants
// xclip/xsel/wl-paste) are also the ones whose terminals send bracketed
// paste, so the fallback failing means the working path was not needed.
func readClipboardCmd() tea.Cmd {
	return func() tea.Msg {
		text, err := clipboard.ReadAll()
		if err != nil || text == "" {
			return nil
		}
		return tea.PasteMsg{Content: text}
	}
}
