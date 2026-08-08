package tui

import (
	"fmt"

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

// detailStub stands in for the task detail view until PR J. It exists as its
// own type only to receive selectTaskMsg, so the board's hand-off is wired
// and tested now and PR J inherits a working selection instead of inventing
// one.
type detailStub struct{ taskID int64 }

func (d *detailStub) title() string { return "Task detail" }

func (d *detailStub) update(msg tea.Msg) (view, tea.Cmd) {
	if sel, ok := msg.(selectTaskMsg); ok {
		d.taskID = sel.id
	}
	return d, nil
}

func (d *detailStub) render(int, int) string {
	if d.taskID == 0 {
		return "\n  Task detail lands in PRs J/K (T3.3, T3.4) — press enter on a board row.\n"
	}
	return fmt.Sprintf("\n  task %d — detail lands in PRs J/K (T3.3, T3.4).\n", d.taskID)
}

// inputCapturing is implemented by views that consume raw keystrokes while
// a text field is focused. The root checks it before applying the global
// single-key bindings, so typing "q" into the board's filter does not quit.
type inputCapturing interface {
	capturesInput() bool
}

// clientAware is implemented by views that talk to the daemon themselves.
// The root hands each one the client as the connection comes up (and again
// after a reconnect) rather than widening the view interface, which every
// stub would then have to implement meaninglessly.
type clientAware interface {
	setClient(*apiclient.Client) tea.Cmd
}

// newViews returns the initial view set: all stubs in PR H; each later PR
// swaps its own slot for the real screen.
func newViews() [viewCount]view {
	return [viewCount]view{
		viewBoard:     newBoard(),
		viewDetail:    &detailStub{},
		viewNewTask:   stubView{name: "New task", note: "The new-task flow lands in PR L (T3.5)."},
		viewProjects:  stubView{name: "Projects", note: "Projects lands in PR M (T3.6)."},
		viewWorkflows: stubView{name: "Workflows", note: "Workflows lands in PR M (T3.6)."},
		viewDaemon:    stubView{name: "Daemon", note: "The daemon view lands in PR M (T3.7)."},
	}
}
