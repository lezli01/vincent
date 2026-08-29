package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// viewID indexes the root's routed screens: the board-only home screen, the
// full-screen task workspace, and the four management takeovers (§15).
type viewID int

const (
	viewHome viewID = iota
	viewTask
	viewNewTask
	viewProjects
	viewWorkflows
	viewDaemon
	viewCount
)

// panel is one routed screen (T3.10: renamed from view). Screens are
// sub-models: the root delegates non-global messages to the active one only.
type panel interface {
	title() string
	update(msg tea.Msg) (panel, tea.Cmd)
	render(width, height int) string
}

// selectViewMsg asks the shell to switch views. A view uses it rather than
// reaching into the view table itself — routing stays the root's job.
type selectViewMsg struct{ id viewID }

// inputCapturing is implemented by views that consume raw keystrokes while
// a text field is focused. The root checks it before applying the global
// single-key bindings, so typing "q" into the board's filter does not quit.
type inputCapturing interface {
	capturesInput() bool
}

// pasteReceiving is implemented by views that hold text fields. The root
// hands pasted text to the *active* view only — a paste belongs to the one
// field that has the keyboard, and the broadcast path every other background
// message takes would type the same text into every view at once. A view
// with no field taking input returns nil, and the paste is dropped.
//
// The sub-models below speak tea.KeyPressMsg, not tea.Msg, so a paste has no
// key to ride in on; each view walks to its focused field and feeds it a
// tea.PasteMsg, which bubbles' textinput/textarea insert verbatim.
type pasteReceiving interface {
	paste(text string) tea.Cmd
}

// contextual is implemented by views whose binding context depends on which
// of their own layers has the keyboard. The workflows takeover is the one:
// its graph sub-layer has a different set of keys from the list behind it,
// and the footer and the ? overlay have to name the live one.
type contextual interface {
	bindingContext() bindingContext
}

// projectHinting is implemented by views that know which project the user is
// looking at, so the new-task form opens on it rather than making them pick
// the project they were just staring at.
type projectHinting interface {
	hintedProject() int64
}

// dataDirAware is implemented by views that read files the daemon writes.
// The daemon view's log tail is the only one, and §15 records why: an
// endpoint cannot serve the log when the daemon is what died.
type dataDirAware interface {
	setDataDir(string)
}

// connectionAware is implemented by views that render while the daemon is
// unreachable and therefore have to say which of their contents are still
// true: the daemon view, the board, and an already-loaded task workspace
// marked stale (§15 Disconnected).
type connectionAware interface {
	setConnected(bool)
}

// clientAware is implemented by views that talk to the daemon themselves.
// The root hands each one the client as the connection comes up (and again
// after a reconnect) rather than widening the panel interface, which every
// stub would then have to implement meaninglessly.
type clientAware interface {
	setClient(*apiclient.Client) tea.Cmd
}

// newViews returns the initial view set. ctx bounds background work a view
// owns — the detail sub-model's per-task subscription.
func newViews(ctx context.Context) [viewCount]panel {
	home := newShell(ctx)
	// Keep the board and detail sub-models independently testable while routing
	// them as separate screens. The task view owns detail updates; the home shell
	// retains the pointer only so both screens share the established action state.
	home.boardOnly = true
	home.detail.active = false
	return [viewCount]panel{
		viewHome:      home,
		viewTask:      newTaskView(home.detail),
		viewNewTask:   newNewTask(),
		viewProjects:  newProjectsView(),
		viewWorkflows: newWorkflowsView(),
		viewDaemon:    newDaemonView(),
	}
}
