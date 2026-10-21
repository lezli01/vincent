# 136 — Main tabs in the TUI: Issues, Tasks and Chats behind one `tab` key

**Status:** 🔄 in progress (2/4)
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
   and refuses it like `[]`. Over PATCH a JSON `null` is the API's "leave
   unchanged", as for every other field, not a refused value (review F7 of
   PR #805, 2026-10-21). A single entry is valid and is the opt-out of the
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
9. **The root owns the tab list** (2026-10-21, #787): `root.tabs` as views,
   `defaultTabs()` until a config answer lands, and `lastTab` recorded by
   `switchTo` for #789. No wrapper panel and no renumbering of `viewID` —
   `activeContext`, `surfaceTarget` and the switch fallbacks address panels
   by `viewID`. *Beat:* a tabs panel holding the three views.
10. **`tab`/`shift+tab` switch only when four things hold** (2026-10-21,
    #787): the active view is one of the tabs, there are two or more, it is
    not capturing input, and its context has no `tab` row of its own
    (main-tab rows aside — `panelOwnsLocalKey`). The check sits after the
    input-capture gate and before `globalKey`, in every connection phase, so
    a filter being typed takes the first `tab` (the screenshot tapes'
    `/ … Tab` idiom keeps working) and the new-chat form keeps its field key.
    Everywhere else the local meaning is unchanged. *Beat:* `ctrl+tab`,
    digits (retired by task 049), a leader key.
11. **The keys stay fixed** (2026-10-21, #787): spec row 35 lists `tab`
    among them, so no operation is added. The dead global `tab` row is gone;
    `ctxTasks`, `ctxIssues` and `ctxChats` carry a `mainTab` row instead,
    left out of the footer and `?` where `tab` does not switch and hinting
    the next tab's name where it does; `keymap.fixed` moves with it. The
    palette gains a keyless "tasks — the task board" screen row.
12. **`esc` on a main tab is a no-op below the view's own layers, and only
    while the view is a configured tab** (2026-10-21, #787): with
    `tui.tabs: [tasks]` the issues list and the chats board still leave to
    the board. A view that is not a tab has no reason to trap the human.
    `esc` from a takeover back to its owning tab is #789's.
13. **A reload never moves the human** (2026-10-21, #787): `applyTabs` reads
    the same three config answers `applyKeymap` does, ignores a failed one,
    maps names through `tabViewIDs` (dropping unknown ones) and falls back to
    the default on an empty result; a list that drops the active view leaves
    it on screen, where `tab` then stops switching.

## Tasks

- [x] **136.1** The `tui.tabs` key (#785): config field, vocabulary, default,
  validation (null included), bootstrap line, API GET/PATCH, apiclient fields,
  `vincent config get|set tui.tabs`, the TUI config editor row, the
  name→view table, tests at each layer, m11 gate scenario 10, the
  configuration reference and the spec §12.3 amendment. ✓ 2026-10-20
- [x] **136.2** Tab switching with `tab` (#787): `root.tabs`, `applyTabs`,
  the four-clause rule, the main-tab registry rows and fixed keys, the
  palette's tasks row, `esc` on a tab root, tests, the TUI guide, the
  configuration reference and the spec §15 amendments. ✓ 2026-10-21
- [ ] **136.3** The tab strip (#788).
- [ ] **136.4** Landing on the first tab (#789).
