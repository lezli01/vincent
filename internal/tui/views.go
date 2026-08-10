package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// viewID indexes the root's routed screens: the fused home screen (§15
// views 1–2 as one persistent three-panel shell) and the four full-screen
// takeovers (§15 views 3–6). The digits 1..6 keep their §15 meanings until
// T3.11 retires them — 1 and 2 both land on the home screen.
type viewID int

const (
	viewHome viewID = iota
	viewNewTask
	viewProjects
	viewWorkflows
	viewDaemon
	viewCount
)

// panel is one routed screen (T3.10: renamed from view). Screens are
// sub-models: the root delegates non-global messages to the active one only.
// The home shell implements it too, and composes the board and detail
// sub-models into the three §15 panes behind it.
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
// true: the daemon view, and the home shell whose panels stay on screen
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
	return [viewCount]panel{
		viewHome:      newShell(ctx),
		viewNewTask:   newNewTask(),
		viewProjects:  newProjectsView(),
		viewWorkflows: newWorkflowsView(),
		viewDaemon:    newDaemonView(),
	}
}
