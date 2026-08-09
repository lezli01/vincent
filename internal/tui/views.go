package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// viewID indexes the six §15 views; the 1..6 global keys map onto it.
type viewID int

const (
	viewBoard viewID = iota
	viewDetail
	viewNewTask
	viewProjects
	viewWorkflows
	viewDaemon
	viewCount
)

// view is one routed screen. Views are sub-models: the root delegates
// non-global messages to the active view only.
type view interface {
	title() string
	update(msg tea.Msg) (view, tea.Cmd)
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
// true. Only the daemon view is reachable in that state.
type connectionAware interface {
	setConnected(bool)
}

// clientAware is implemented by views that talk to the daemon themselves.
// The root hands each one the client as the connection comes up (and again
// after a reconnect) rather than widening the view interface, which every
// stub would then have to implement meaninglessly.
type clientAware interface {
	setClient(*apiclient.Client) tea.Cmd
}

// newViews returns the initial view set. ctx bounds background work a view
// owns — the detail view's per-task subscription.
func newViews(ctx context.Context) [viewCount]view {
	return [viewCount]view{
		viewBoard:     newBoard(),
		viewDetail:    newDetail(ctx),
		viewNewTask:   newNewTask(),
		viewProjects:  newProjectsView(),
		viewWorkflows: newWorkflowsView(),
		viewDaemon:    newDaemonView(),
	}
}
