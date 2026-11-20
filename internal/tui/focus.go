package tui

import tea "charm.land/bubbletea/v2"

// updateFocus records the terminal's focus report and passes it on (task
// 165). The root turns focus reporting on in View (ReportFocus), and keeps
// focused for the bell, which rings only while the terminal is unfocused —
// once it has said so at all: focusKnown records that a report arrived.
//
// The broadcast is a contract, not a convenience: every view in m.views
// hears tea.FocusMsg and tea.BlurMsg exactly as the root did, after the
// root has updated its own state. The chat workspace decides from them
// whether a turn that finishes while it is open counts as read — a chat on
// screen in an unfocused terminal has not been seen. Do not narrow this to
// the active view, and do not swallow either message here.
func (m *root) updateFocus(msg tea.Msg) tea.Cmd {
	m.focusKnown = true
	switch msg.(type) {
	case tea.FocusMsg:
		m.focused = true
	case tea.BlurMsg:
		m.focused = false
	}
	return m.broadcast(msg)
}
