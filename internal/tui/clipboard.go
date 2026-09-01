package tui

import (
	"errors"
	"fmt"

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

// Writing is the other direction, and it has two transports (task 076).
//
// github.com/atotto/clipboard shells out to the platform's helper — pbcopy,
// clip.exe, xclip/xsel/wl-copy — and returns a real error, which is what a
// visible success/failure notice needs. It is also wrong over SSH: with
// xclip installed on the remote box it succeeds against the *remote* display,
// and the text never reaches the human's machine.
//
// tea.SetClipboard is OSC 52 — the terminal itself is asked to take the
// text, so it lands on the machine the human is sitting at, no helper binary
// is needed, and it works over SSH. It is fire-and-forget: the terminal may
// refuse (many do by default) and says nothing either way.
//
// So: the helper first, because its answer can be trusted, and OSC 52 when it
// refuses, because an unverified copy beats none. The notice says which
// happened rather than claiming success for both — and the fallback's notice
// names the helper's error, following openurl.go's posture that a key press a
// human made must never fail silently.

// clipboardWrite is the system-clipboard write, indirected so tests assert
// the seam without a display, a helper binary or a real clipboard — the way
// openURL is.
var clipboardWrite = clipboard.WriteAll

// clipboardResultMsg reports what became of a copy. label names what was
// copied ("markdown", "code block") so the notice can say it.
type clipboardResultMsg struct {
	label string
	// osc is the payload the terminal must still be asked to take, set only
	// when the system clipboard refused. err then carries its refusal, which
	// the notice names.
	osc string
	err error
}

// notice renders the result as the line a human reads, and whether it is bad.
func (msg clipboardResultMsg) notice() (string, bool) {
	label := msg.label
	if label == "" {
		label = "copy"
	}
	switch {
	case msg.osc != "":
		// Not an error: the text was handed to the terminal, which usually
		// takes it. Unverified, so it does not claim "copied".
		return fmt.Sprintf("%s sent to the terminal — the system clipboard refused (%s)",
			label, errString(msg.err)), false
	case msg.err != nil:
		return fmt.Sprintf("%s: %s", label, errString(msg.err)), true
	default:
		return label + " copied", false
	}
}

// writeClipboardCmd puts text on the clipboard.
//
// The payload is sanitized here rather than at the call sites: a clipboard is
// pasted into a terminal, which is precisely the boundary §16 exists to hold,
// so every payload goes through the same chokepoint the pane's own text does
// (task 076 decision 4). "Original Markdown" therefore means the stored
// Markdown minus escape sequences and C0/C1 controls.
func writeClipboardCmd(label, text string) tea.Cmd {
	return func() tea.Msg {
		clean := sanitizeText(text)
		if clean == "" {
			return clipboardResultMsg{label: label, err: errors.New("nothing to copy")}
		}
		if err := clipboardWrite(clean); err != nil {
			return clipboardResultMsg{label: label, osc: clean, err: err}
		}
		return clipboardResultMsg{label: label}
	}
}
