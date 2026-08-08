// Package tui implements the Bubble Tea terminal UI (spec §15). The TUI is
// a pure API client: it holds no state the daemon doesn't have, and killing
// it never affects work. Bare `vincent` launches it, auto-starting the
// daemon in the background when unreachable (§12.1).
//
// Phase 3 lands it PR by PR: this foundation ships the shell — connection
// management, /v1/events subscription, view routing, global keys, and the
// help overlay — with stub views that later PRs replace (board, task
// detail, new-task flow, projects/workflows/daemon).
package tui
