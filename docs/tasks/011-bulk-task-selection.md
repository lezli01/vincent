# 011 — Selecting several tasks for one action

Status: **done** (5/5)

The board is where triage happens, and triage arrives in batches: a sweep of
finished tasks to archive after a morning's work, a run of queued ones to cancel
after a bad workflow edit. Today every §6 action acts on exactly the row under
the cursor, so a batch is the same keypress N times with a confirmation between
each — which is where a human either stops tidying up, or stops reading the
confirmations. Both outcomes are worse than the keystrokes saved.

This adds a **selection** to the task table: `space` marks the row under the
cursor, `V` marks everything the filter is showing, and while anything is marked
the §6 action keys act on the whole set instead of the cursor row.

## Decisions

- **The selection is a set of tasks, not of rows** *(2026-08-16)*. It survives a
  filter, a `g` regroup and a refresh, because all three are ways of *looking* at
  the board rather than statements about what was chosen. The alternative —
  narrowing the selection to what is currently visible — means typing a filter
  silently changes what a confirmed archive will destroy, which is the one thing
  a bulk action must never do. The count in the panel title (`Tasks — 5
  selected`) is what keeps a marked-but-hidden task honest.
- **An action is offered when *some* marked task offers it, and the key carries
  the count** *(2026-08-16)*. `A archive (7)` on nine marked rows says both that
  the key works and that two rows will not move. Requiring *every* marked task to
  offer the action — the other candidate — would make `archive` vanish from a
  sweep of finished work because one task in it is still running, with nothing on
  screen to explain the absence. Tasks that do not offer the action are left
  alone; the invariant §15 actually holds is "an action that cannot happen is not
  on screen", and one that can happen to seven of nine can happen.
- **One call per task, sequentially, from the client** *(2026-08-16)*. No bulk
  endpoint: §6 lives in the API and the TUI is one of three clients, so a second
  definition of what `archive` means is a second thing to keep in step. Sequential
  rather than concurrent because N parallel archives are N worktree removals
  racing the scheduler for the slots they free, and because the report a human
  reads afterwards ("5 of 7") has to be built from outcomes that actually
  happened. Each call keeps the single-action timeout; the batch as a whole is not
  bounded, since a batch cut off halfway leaves no account of which half.
- **`force` stays the dirty confirmation, in bulk too** *(2026-08-16)*. A bulk
  archive that meets dirty worktrees archives the clean ones and re-asks about
  exactly the refusals — "2 of 5 selected tasks have uncommitted changes" — the
  same second ask a single archive already makes (§6). The alternative, failing
  the whole batch, would make the common case (one dirty task among six) require
  finding and de-selecting it.
- **The selection keeps what failed** *(2026-08-16)*. Tasks the daemon accepted
  are unmarked when the result lands; refusals stay marked, so a retry needs no
  re-selection and the rows still on screen are the ones still to deal with.
- **Bulk keys work from any panel** *(2026-08-16)*. The footer counts the marked
  tasks wherever the eye is, so the shell routes action keys at the selection
  before the focused panel sees them — otherwise `A` over the output pane would
  archive the one task the detail panels happen to be showing while the footer
  promised seven.
- **`esc` clears the selection before the filter** *(2026-08-16)*, one layer per
  press, innermost first — the selection is the more recent thing in the flow
  that builds it.

## Tasks

- [x] **011.1** The selection itself: `markSet` on the board, `space` toggling
  the cursor row, `V` marking (and unmarking) everything the filter shows, `esc`
  clearing it, marks pruned to tasks the daemon still lists.
  *Done when:* the set survives a refresh that reorders rows and a regroup, and a
  task the daemon dropped leaves the set with it. ✓ 2026-08-16 —
  `TestMarksSurviveARefreshThatReorders`,
  `TestMarksArePrunedForTasksTheDaemonDropped`,
  `TestMarkVisibleTakesTheFilterButTheSelectionKeepsWhatItHad`.
- [x] **011.2** Rendering: a marker column that exists only while something is
  marked, the count in the panel title, counts beside the action keys in the
  action line, the footer and the palette.
  *Done when:* the marker column costs nothing at any width when nothing is
  marked, and `A archive (7)` renders for nine marked rows of which seven can be
  archived. ✓ 2026-08-16 — `TestMarkerColumnExistsOnlyWhileSomethingIsMarked`,
  `TestBoardTitleCountsTheSelection`, `TestBulkActionKeysCountTheTasksTheyMove`,
  and the column/row agreement check in `TestGroupedRowsMatchTheColumnCount`
  extended to the marked case.
- [x] **011.3** Bulk dispatch: one call per task, the dirty-worktree re-ask over
  the refusals only, and a one-line report of what happened.
  *Done when:* a bulk archive over a mixed selection archives what it can, re-asks
  for the dirty ones, and reports `5 of 7` with the first failure named.
  ✓ 2026-08-16 — `TestBulkArchiveAsksOnceAndSendsOneCallPerTask`,
  `TestBulkArchiveReAsksForTheDirtyOnesOnly`, `TestBulkReportNamesTheFirstRefusal`,
  `TestBulkSelectionLeavesFailuresMarked`.
- [x] **011.4** Routing: action keys retargeted at the selection from any panel
  focus, with a pending bulk confirmation owning the keyboard.
  *Done when:* `A` then `y` with the output pane focused archives the selection,
  not the open task. ✓ 2026-08-16 — `TestBulkKeysWorkFromAnyPanel`,
  `TestEscClearsTheSelectionBeforeTheFilter`, and `TestBulkArchiveLive`, which
  drives the whole thing through the real §6 handlers.
- [x] **011.5** Registry, help and docs: `space`/`V` in `bindings.go` with
  probes, the spec §15 amendment, the TUI guide.
  *Done when:* `TestEveryPanelKeyIsHandled` covers both keys and the spec
  describes the selection as shipped. ✓ 2026-08-16 — probes added for
  `space` and `V`; spec §15 amended (view 1, *Bulk selection*, Keys, the esc
  stack) and `docs/guides/tui.md` grew *Acting on several tasks at once*.
