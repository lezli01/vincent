// Package tui implements the Bubble Tea terminal UI (spec §15). The TUI is
// a pure API client: it holds no state the daemon doesn't have, and killing
// it never affects work. Bare `vincent` launches it, auto-starting the
// daemon in the background when unreachable (§12.1).
//
// The root owns connection management, the /v1/events subscription, screen
// routing and the global keys. The home screen (shell.go) fuses §15's board
// and task detail into one persistent three-panel layout — task table on
// top, step timeline and output|diff side by side below — with layout.go's
// pure layout function deciding the accordion, the single-panel fallback
// and the too-small floor (T3.10). The remaining §15 surfaces (new task,
// projects, workflows, daemon) stay full-screen takeovers.
package tui
