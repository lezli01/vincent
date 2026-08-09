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

// stubView is a placeholder screen for a view a later Phase 3 PR replaces.
type stubView struct {
	name string
	note string
}

func (s stubView) title() string                  { return s.name }
func (s stubView) update(tea.Msg) (view, tea.Cmd) { return s, nil }
func (s stubView) render(int, int) string         { return "\n  " + s.note + "\n" }

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

// clientAware is implemented by views that talk to the daemon themselves.
// The root hands each one the client as the connection comes up (and again
// after a reconnect) rather than widening the view interface, which every
// stub would then have to implement meaninglessly.
type clientAware interface {
	setClient(*apiclient.Client) tea.Cmd
}

// newViews returns the initial view set: all stubs in PR H; each later PR
// swaps its own slot for the real screen. ctx bounds background work a view
// owns — the detail view's per-task subscription.
func newViews(ctx context.Context) [viewCount]view {
	return [viewCount]view{
		viewBoard:     newBoard(),
		viewDetail:    newDetail(ctx),
		viewNewTask:   newNewTask(),
		viewProjects:  newProjectsView(),
		viewWorkflows: newWorkflowsView(),
		viewDaemon:    stubView{name: "Daemon", note: "The daemon view lands in PR M (T3.7)."},
	}
}
