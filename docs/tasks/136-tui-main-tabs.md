# 136 — Main tabs in the TUI: Issues, Tasks and Chats behind one `tab` key

**Status:** 🔄 in progress (3/4)
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
14. **The strip replaces the view tag in the header, on the one chrome row**
    (2026-10-21, #788): `Issues │ Tasks` in `root.tabs` order, the active
    label titled and the rest dim, labels from the views' `title()` so the
    strip, the tag and the frame cannot disagree. `chromeTop` and
    `bodyHeight` do not move. *Beat:* a second chrome row — #784 kept the
    chrome one line.
15. **The strip is drawn exactly where `tab` switches, minus the input
    clause** (2026-10-21, #788): a tab root, two or more tabs, no local `tab`
    row. A filter being typed does not hide it — that would flicker — while
    the new-chat form, sub-screens, takeovers and a single tab keep their tag
    or breadcrumb.
16. **Shedding: version, then the elsewhere badge, then initials, then the
    project name** (2026-10-21, #788). The strip is navigation and outlives
    decoration; it never vanishes, and the eight-cell tag floor applies only
    to a tag.
17. **A label click follows `tabSwitches()` exactly** (2026-10-21, #788):
    per-label `[x0, x1)` spans are recorded on every header render beside
    `headerHit` and cleared when no strip is drawn; the active label, the
    separators and a click while typing do nothing; every connection phase
    switches, as `tab` does.
18. **The board is called "Tasks"** (2026-10-21, #788): its `title()` and
    the task workspace's breadcrumb root, so the breadcrumb, the tag and the
    strip all name the tab `esc` returns to. Prose keeps "task board".

19. **Landing: at `Run`, adjusted once by the first config answer**
    (2026-10-21, #789). `tui.Run` sets the active view and `lastTab` to
    `defaultTabs()[0]` before the program starts, so the TUI is navigable
    in every connection phase; the first *successful* config answer moves
    a human who has not navigated yet to the configured `tabs[0]`. A
    `landing` flag on the root is armed by `land()` and disarmed by that
    answer or by any switch, whichever comes first. The default list never
    jumps; another jumps at most once, beside the startup project switch.
    `newRoot` does not land, so tests that build a root keep the board.
20. **Decision 13 holds** (2026-10-21, #789): the issue proposed moving a
    human whose active tab a reload removed; the author kept decision 13.
    The dropped screen stays up, and its `esc` now leaves to the first tab.
21. **One "back to your tab" message** (2026-10-21, #789): every
    takeover's last `esc` — projects, workflows, daemon, pull requests,
    triggers, new task, both archived boards, a list that is not a tab —
    sends `backToTabMsg`, resolved by the root to `lastTab` while it is
    still configured, else `tabs[0]`. A task's explicit `back` (an issue,
    the overview) is untouched, and the board's own open (`fromBoard`)
    still returns to the board; every other open with no `back` — the
    palette, pull requests, triggers, `!`, a new task — gets `viewLastTab`,
    the same message (review F1). The overview's return with no project-scoped history is
    the tab too.
22. **A board that is not a tab is left by `esc`** (2026-10-21, #789): the
    shell is `tabAware`, and with `tui.tabs` leaving `tasks` out, `esc` at
    the bottom of the board's stack sends `backToTabMsg`. This refines
    decision 12: the issues list and the chats board, when not tabs, leave
    to the human's tab rather than to the board.
23. **Tapes press a visible `Tab`** (2026-10-21, #789): the seed keeps the
    default `[issues, tasks]`; every tape whose first key is a board key
    presses `Tab` outside the `Hide` block, the issue tapes start on the
    landing instead of through `:`, and `TestBoardTapesSwitchToTasksFirst`
    holds the script to it.

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
- [x] **136.3** The tab strip (#788): the header strip and its shedding
  order, label clicks, the "Tasks" rename of the board and the breadcrumb
  root, tests, the TUI guide and the spec §15 amendments. ✓ 2026-10-21
- [~] **136.4** Landing on the first tab (#789): the landing, `backToTabMsg`,
  the board's `esc` off the tabs, tests (unit and e2e), the tape changes and
  their test, the TUI guide and the spec §15 amendments are in; the
  `docs/assets/tui-*.png` re-capture with VHS 0.11.0 remains.
