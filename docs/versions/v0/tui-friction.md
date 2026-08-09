# TUI friction record (T3.9)

The "before" the panel refactor (T3.10–T3.13) is graded against. Every item here
comes back as a checkbox in the T3.8 walkthrough, each carrying its disposition:
**fixed**, **deferred** to a Phase 4 task ID, or **won't-fix** with a reason.

Written *before* the refactor on purpose. Without a fixed record, "is the TUI
easier now?" is settled by whoever argues hardest in the PR thread — which is how
42 keystrokes accumulated without anyone deciding to add them.

## How this was produced

Derived from the source, not from a walkthrough. The outgoing UI is never walked
(see the Phase 3 TUI refactor decisions in `tasks.md`): a two-OS session with real
`claude` costs hours and pins a commit SHA the next PR invalidates, and the
friction is already legible in `internal/tui`. Measurements below are reproducible
against the tree at the commit that adds this file:

- **Bound keystrokes** — every distinct string in a `case "…":` in a non-test
  `internal/tui/*.go`, minus event-type constants, unioned with the keys of the
  `actionKeys` map in `actionbar.go`.
- **Documented keystrokes** — the left column of the row tables in `help.go`.

Counts are of *distinct keystrokes*, not of help rows: `help.go` has 45 rows, but
several rows carry two keys (`a / x`, `f / G`, `+ / -`, `enter/e`) and one carries
six (`1..6`).

## Findings

| ID | Finding | Evidence |
|---|---|---|
| **F1** | **42 distinct keystrokes are bound; the screen shows a fraction of them at any moment.** The only complete list is behind `?`. | 42 measured by the method above |
| **F2** | **`?` is a full-screen modal you must leave the work to read, and cannot act from.** It is 45 rows in 9 groups — longer than most terminals, so it also scrolls. | `help.go:6-92`; 9 groups counted at `help.go:73-90` |
| **F3** | **Bindings live in three hand-maintained places and have already drifted.** Task actions are a map, everything else is per-view `case` arms, and the overlay is a third literal list. At least 8 bound keystrokes appear nowhere in `?`: `j` `k` `y` `Y` `_` `=` `end` `shift+tab`. | `actionbar.go:32`, per-view switches, `help.go:7-71` |
| **F4** | **Navigation is six numbered keys with no on-screen legend.** Nothing maps `3` to "new task" except the overlay; there is no tab strip, no breadcrumb, no title bar naming the other five. | `views.go:14-22`, `root.go:189` |
| **F5** | **Board and detail are a modal round-trip.** `enter` in, `esc` out. A task's live output and the rest of the queue are never on screen together, so watching three parallel tasks means cycling. | `board.go:347`, `detail.go:582` |
| **F6** | **The same key means different things per view, with nothing on screen saying which.** `d` is output/diff in detail and *delete* in projects. `a` is approve on a task and *add* in projects. `r` and `R` are different commands. | `detail.go:572`, `projects.go`, `workflows.go` |
| **F7** | **Only task actions get a contextual hint; every other view's keys are invisible until `?`.** The action bar renders exactly the valid `available_actions` — the right idea, applied to one of nine key groups. | `actionbar.go:231-256` |
| **F8** | **Attention tasks are pinned and ring the bell, but there is no way to *go* to one.** You find the task blocking on you by eye, then move the cursor by hand. | §15 pinning; no jump binding exists |
| **F9** | **No mouse.** Clicking a row, a pane, or a hint does nothing. | no mouse handling in `internal/tui` |
| **F10** | **There is no command surface.** If you do not know the key, the only recourse is `?` and reading. Nothing is searchable by intent — you cannot type "archive" and find `A`. | absent |
| **F11** | **`esc` is overloaded and its meaning is positional.** Clear filter · back to board · leave the answer form · in new-task, leave the *field*, then the *form*, with a confirm if the draft was touched. | `board.go:342`, `detail.go:582`, `newtask.go` |
| **F12** | **The answer form — the one screen where a task is blocked on the human — is a stop in a `tab` cycle.** Reaching it requires knowing that `tab` cycles timeline → pane → form, and that the form is only in the cycle when a question is pending. | `detail.go:566`, `help.go:32` |

## Where each is addressed

| Finding | Task | Mechanism |
|---|---|---|
| F1, F10 | T3.11 | `:` command palette — searchable by intent, shows each entry's direct key |
| F2, F3 | T3.11 | One `bindings.go` registry renders palette, footer and `?`; drift becomes a test failure |
| F4 | T3.11 | `1..6` retired; the four takeover screens are palette entries |
| F5 | T3.10 | Board and detail fuse into one three-pane screen |
| F6 | T3.12 | Footer names the focused panel's keys, so the meaning in force is on screen |
| F7 | T3.12 | The action bar generalises into the footer for every panel |
| F8 | T3.11 | Jump-to-next-attention key, surfaced in the footer only when the count is non-zero |
| F9 | T3.13 | Click-to-focus, click-row-select, wheel scroll, clickable hints and tabs |
| F11 | T3.10/T3.11 | Explicit `esc` stack: popup → takeover → filter → no-op; never quits |
| F12 | T3.10 | Answer form becomes a popup with a row badge and a footer hint; it never steals focus |

## Non-goals

Two things this record deliberately does **not** ask for, so that T3.8 does not
grade against them:

- **A smaller command surface.** The refactor unhides keys; it does not delete
  capability. The count goes from 42 to roughly 41 — `1..6` dies, one
  attention-jump key is born. The win is that memorisation stops being required,
  not that there is less to memorise. Cutting further means removing things the
  TUI can do, which was considered and rejected.
- **A tutorial or guided tour.** One line on the existing first-run screen names
  `:` and `?`; the permanently pinned footer hint is the real teacher. A tour is
  what you build when the UI cannot explain itself.
