# 145 — Moving an issue between lanes from the TUI

**Status:** ✅ done (1/1) · **Opened:** 2026-11-16

The author asked that issues "can be moved around on the tui between the
states". Task 134 decision 1 had ruled out a stored, human-settable lane and
"dragging a card between lanes", and §15 view 12 said "no key moves an issue
between lanes". The author reopened that decision on 2026-11-16 and chose a
move that **runs the action a lane is derived from** over a stored lane.

Conventions for this file are in [the tasks README](README.md).

## What this reverses

- **Task 134 decision 1** — **kept**: the lane is still derived from the
  issue and its root tasks, never stored, never set. What is reversed is its
  last clause as the TUI read it, "no dragging a card between lanes": a card
  now has a move key, which changes the facts the lane is derived from.
- **§15 view 12, task 134.8's amendment**, "never draggable: no key moves an
  issue between lanes" — **narrowed** by the dated note of 2026-11-16 in the
  same section.

## Tasks

- [x] **145.1** `m` on the issues list and the issue detail: the lane
  picker, `planLaneMove`, the cancel move, the landed-lane note on reopen
  and cancel, the binding and fixed-key rows, the unit and live tests, the
  §15 view 12 amendment and the guides. `internal/tui` and
  `internal/keymap` only; no API, store or migration change. ✓ 2026-11-16

## Decisions

Taken 2026-11-16 by the author, from the options put to them.

1. **A move is an action, not a field.** Picking a lane runs what that lane
   is derived from. *Alternatives beaten:* a stored manual lane that
   overrides the derived one until task activity clears it, and one that
   sticks until a human moves it again. Both need a migration, a rule for
   when the override yields, and a value no remote tracker can hold — task
   134 decision 1's reasons, which still stand.
2. **The move table.** open → in progress starts a task (the new-task
   form, as `a`); in progress → open cancels the unsettled root tasks
   (asks first); in progress → hand-off is `ready` on an idle issue;
   hand-off → in progress is `unready` on a pending issue and `return` on a
   claimed one; any open lane → done closes (with `X`'s reason prompt);
   done → anything reopens. Close and reopen follow `available_actions`.
3. **Refuse, never pretend.** A move with no action behind it — open ⇄
   hand-off, in progress → open with finished work or with only a chat
   holding the issue, in progress → hand-off while a task runs, a
   delivered hand-off → in progress — is listed disabled with the reason
   and changes nothing. A reopen lands wherever the issue's tasks put it,
   so the picker says so before and the note names the lane after; a
   cancel's note does the same.
4. **`m` is a fixed key**, like `X` and `i` on the same screens, not a
   rebindable operation: the move is local to the two issue contexts and
   adds nothing to `tui.keys`.
5. **The TUI reads, the daemon decides.** `planLaneMove` reads only the
   issue DTO — lane, handoff marker, `available_actions`,
   `tasks.active_ids` — and every write is an existing endpoint, so the
   daemon still refuses whatever it would refuse from the CLI.
