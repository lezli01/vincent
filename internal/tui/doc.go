// Package tui implements the Bubble Tea terminal UI (spec §15). The TUI is
// a pure API client: it holds no state the daemon doesn't have, and killing
// it never affects work. Bare `vincent` launches it, auto-starting the
// daemon in the background when unreachable (§12.1).
//
// The root owns connection management, the /v1/events subscription, screen
// routing and the global keys. The home screen (shell.go) is the board alone;
// enter opens task detail (taskview.go) as a full-screen workspace with Steps &
// Attempts, Task Details, Output and Diff tabs. The remaining §15 surfaces
// (new task, projects, workflows, daemon) are full-screen takeovers too.
//
// The root also owns the one selected project (§15 Layout, task 132): view
// state the TUI keeps for itself, never a daemon fact. It caches the project
// list, refreshed on connect, on reconnect and on project.* events, names the
// selection in the app header, and hands it to every project-bearing view
// through the optional projectScoped interface — directly, never by
// broadcast, and again after each setClient walk.
package tui
