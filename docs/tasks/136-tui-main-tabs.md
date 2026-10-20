# 136 — Main tabs in the TUI: Issues, Tasks and Chats behind one `tab` key

**Status:** 🔄 in progress (1/4)
**Opened:** 2026-10-20
**Spec:** §12.3 (`tui.tabs`), §15
**Issues:** #784 (parent), #785, #787, #788, #789

## Problem

The issues pillar (tasks 130, 134) gave each project a second board beside the
task board, and chats (task 067) a third. Moving between them goes through the
command palette or a view key; there is no single "next board" move, and a
project that lives mostly in its issues still opens on the task board. #784
asks for main tabs — a strip of the project-scoped boards, cycled with `tab`,
opening on the first — and for the set and order to be the human's.

This document records the feature's decisions. #785 lands the setting end to
end with no routing behaviour; #787–#789 consume it and extend this record.

## Decisions

1. **`tui.tabs` is a flat, ordered list of view names** (2026-10-20, #785).
   Order is the strip's, the cycle's and the landing order, so the first entry
   is where the TUI opens. *Beat:* `tui.tabs.views` — a nesting level for one
   knob. *Beat:* a separate `tui.landing_tab` — a second key that could
   disagree with the list.
2. **The vocabulary is `issues`, `tasks`, `chats`, declared as
   `config.TabViews` beside `OutputLevels`** (2026-10-20, #785). These are the
   project-scoped board-plus-record pairs. It starts narrow because strict
   decoding makes removing a name a breaking change while adding one is not.
   Declaring it in `config` keeps the leaf rule (config imports only
   `taskstate` and `keymap`). *Deferred:* `pull_requests`; *out:* workflows,
   triggers, the archives, projects, daemon — they are not per-project boards.
3. **Default `[issues, tasks]`;** an absent key keeps it (2026-10-20, #785).
4. **One validation rule on every path** — load, hot reload, `Decode`, PATCH —
   in `TUI.validate`, ordered like `tui.board.group_by` (2026-10-20, #785): an
   empty list or `null` (`list at least one view`), then a repeat, then an
   unknown name; a non-list is refused naming the key. go-yaml leaves the
   default in place on `tabs: null`, so `decode` probes the raw shape first
   and refuses it like `[]`. A single entry is valid and is the opt-out of the
   strip. The "want …" list is generated from `TabViews`. *Beat:* a lenient
   load path like `tui.keys`' (task 128) — a fixed vocabulary cannot collide
   with a default an upgrade adds, so there is nothing to be lenient about.
5. **Served as an array, never `null`; PATCH replaces the whole list**
   through `config.Decode`, so a refused value never touches disk
   (2026-10-20, #785). No config-change SSE event (spec decision row 35).
6. **Client rules** (2026-10-20, #785), documented on
   `apiclient.ConfigTUI.Tabs`: a missing field is an older daemon → the
   default; an unknown name is a newer daemon's → dropped; an empty result →
   the default.
7. **The TUI maps names to views in one table** — `tabViewIDs` in
   `internal/tui/configkeys.go`: `issues`→`viewIssues`, `tasks`→`viewHome`,
   `chats`→`viewChats`, its keys held equal to `config.TabViews` by a test
   (2026-10-20, #785). Switching (#787) routes by it.
8. **Landing comes from the first config answer** (#789), and **`tab`
   switches between the main tabs** (#787) — recorded here as the parent's
   design; the detail is those issues' to add.

## Tasks

- [x] **136.1** The `tui.tabs` key (#785): config field, vocabulary, default,
  validation (null included), bootstrap line, API GET/PATCH, apiclient fields,
  `vincent config get|set tui.tabs`, the TUI config editor row, the
  name→view table, tests at each layer, m11 gate scenario 10, the
  configuration reference and the spec §12.3 amendment. ✓ 2026-10-20
- [ ] **136.2** Tab switching with `tab` (#787).
- [ ] **136.3** The tab strip (#788).
- [ ] **136.4** Landing on the first tab (#789).
