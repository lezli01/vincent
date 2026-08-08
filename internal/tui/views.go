package tui

import (
	tea "charm.land/bubbletea/v2"
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

// newViews returns the initial view set: all stubs in PR H; each later PR
// swaps its own slot for the real screen.
func newViews() [viewCount]view {
	return [viewCount]view{
		viewBoard:     stubView{name: "Board", note: "Board lands in PR I (T3.2)."},
		viewDetail:    stubView{name: "Task detail", note: "Task detail lands in PRs J/K (T3.3, T3.4)."},
		viewNewTask:   stubView{name: "New task", note: "The new-task flow lands in PR L (T3.5)."},
		viewProjects:  stubView{name: "Projects", note: "Projects lands in PR M (T3.6)."},
		viewWorkflows: stubView{name: "Workflows", note: "Workflows lands in PR M (T3.6)."},
		viewDaemon:    stubView{name: "Daemon", note: "The daemon view lands in PR M (T3.7)."},
	}
}
