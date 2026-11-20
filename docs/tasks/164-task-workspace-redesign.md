# 164 — Task workspace redesign: a steps rail, plain tabs, an Output feed, a retouched Diff and an Overview of cards

**Status:** 🔄 in progress (8/9) · **Opened:** 2026-11-20

The task workspace has the shape the chat and issue views had before their
redesigns: a steps pane that spends three rows per attempt and wraps output
excerpts into them, numbered tabs, an Output tab that is the transcript's
records printed in order (`step_started`, tool payloads verbatim, the same test
log twice), a flat Diff, an Overview that is one sentence and two facts in an
empty pane, and a third Details column that repeats the header. A task's linked
issue is named in the Overview and cannot be reached from it.

This reworks the workspace in place, as one pull request. The mocks the
decisions below were taken against are kept as a private artifact by the
author; the decisions here are the record.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **164.1** Steps rail: one row per step (state glyph, name, right-aligned
  duration) on a connecting line; a dim second row only when the step has
  something to say (retry count, gate, agent · cost, a block reason); attempts
  listed only under the selected step and only when there is more than one;
  fan-out lanes as a `├─`/`└─` tree with their task number and state, earlier
  rounds folded to one line; a loop as one pip per iteration; a condition's
  `if:`. `↑/↓` move between steps, `←/→` between the selected step's attempts.
  Shown on every tab.
- [x] **164.2** Tabs: Overview, Output, Diff, Workflow, Pull request (the last
  only when a pull request is linked), cycled with `tab`/`⇧tab`; the digit
  bindings and the tab strip's digits removed; Step details and Details folded
  away (decision 3). A tab may carry a marker: the diff's size, a red `!` on
  Overview when the task needs a human.
- [x] **164.3** Linked issue: the task header names the issue on every tab
  (`◇ issue #N title  state  i open`), or a dim "no linked issue"; `i` (a new
  keymap op on the task surface) opens the issue view, and `esc` returns to the
  task on the tab it left. Drop the right-hand Details pane; its facts move to
  the header and Overview (decision 1).
- [x] **164.4** Output feed: one continuous feed for the whole task. A divider
  per step (name, agent or `command`, attempt, start time); finished steps fold
  to one summary line; assistant prose without a gutter; each tool call one row
  — glyph, verb (read/search blue, edit purple, run yellow), target,
  right-aligned result and duration — with its output folded and `enter` to
  unfold; a failed run unfolded one level; the agent's plan as one checklist row
  updated in place; command output behind a margin bar, the exit as its own
  line. A pinned bottom line says what is running now with elapsed, tokens and
  cost, and `▲ N new below` while not following. Selecting a step in the rail
  scrolls to its divider. `v` keeps the existing levels, default normal;
  `ctrl+o` raw stays.
- [x] **164.5** Per-step commit range (store + engine): a migration adding the
  HEAD a step run started and finished at; the taskrun actor journals both
  (crash-first: the start before the agent runs, the end before the step run is
  finalized); recovery leaves an interrupted run's end empty. Exposed on the
  step-run response.
- [x] **164.6** Diff by step (API + TUI): `GET /v1/tasks/{id}/diff?step_run=`
  diffs the run's start..end range (409 with a reason when a range is not
  recorded — rows from before 164.5, or a run that never finished). The tab
  gains a summary line naming the base and the scope, a file list with
  M/A/D/R letters and change bars beside the hunks (falling back to inline
  folds when narrow), line numbers, tinted added and removed lines that keep
  their `+`/`−`, hunk headers carrying the function context; `d` toggles whole
  task and the step selected in the rail.
- [x] **164.7** Overview of cards: a status card on top chosen by the existing
  three frames (progress bar and step names; a red attention card with the
  reason, the already-fetched evidence lines and each action with what it does;
  an outcome card with diff size, commits and the pull request); then Now beside
  The task; then Linked (issue, pull request, chats, branch and base, each row
  openable with `enter`) beside Usage (elapsed, tokens, cost, workflow and
  origin, worktree).
- [x] **164.8** Docs and spec: §15's task workspace, key and tab text; §13.2
  for the diff's `step_run` and the step-run fields; §5 for the new columns;
  `docs/reference/` key tables (from `internal/tui/bindings.go`), API and the
  task guide; `CHANGELOG.md`; an assertion for the per-step diff in a gate.
- [~] **164.9** Re-capture every `docs/assets/tui-task-*.png`, `tui-diff.png`,
  `tui-lane-*.png` and `tui-task-steps-lanes.png` with
  `scripts/screenshots.sh`, and drop any shot whose tab no longer exists.

Tasks 164.1–164.8 done ✓ 2026-11-20; the per-step range landed as migration
0064 (master's 0062 and this branch's lane migration, renumbered 0063, came
first).

## Decisions

Confirmed by the author on 2026-11-20, against the mocks.

1. **The Details pane is dropped.** Workflow, branch and base, usage and links
   move to the header line and Overview's Linked and Usage cards. Output and
   Diff get the width. Keeping it on Overview only was declined.
2. **`i` opens the linked issue** from any tab of the workspace; `esc` comes
   back to the same tab. With no issue the header and the Linked card say "no
   linked issue", dim, and offer nothing — linking an issue after creation is
   not part of this work.
3. **Five tabs, no digits.** Overview, Output, Diff, Workflow, Pull request
   (only when linked), cycled with `tab`/`⇧tab`. Step details (what a step was
   given) is reached with `enter` on a rail step; the Details tab's facts are
   Overview's. Removing the digits is a §15 keymap change and is documented as
   one.
4. **Output is one continuous feed**, not the selected attempt's transcript:
   it is what makes a running task followable. The rail selection scrolls the
   feed rather than filtering it. A per-step view and a toggle were declined.
5. **The default level is normal**: prose and one-line actions, tool output
   folded, a failure unfolded one level.
6. **Diff by step is in scope now**, which makes this the one part with daemon
   work (164.5, 164.6). Step runs do not record a commit today, so a run from
   before the migration has no range and says so rather than guessing one.
7. **One pull request**, mentioning "closes 164".

## Where the build departed from the mocks (2026-11-20)

Found while building; each is the smaller of two options, recorded so it can
be revisited rather than rediscovered.

8. **`i` is a fixed key, not a rebindable term.** §15's clause checker holds a
   vocabulary term to one meaning on every surface, and `i` already means
   other things on the issue, workflow, triggers, daemon and chat screens. It
   is recorded in `keymap/fixed.go` on each workspace tab, so a `tui.keys`
   binding to `i` shadows it with a warning. The Pull request tab keeps its
   own `i` (comment), the one tab where `i` does not open the issue.
9. **The rail is moved from Output, not focused.** The keyboard stays in the
   middle pane; `←`/`→` on Output walk the attempts, and the rail and the
   feed follow, as the Output tab's attempt keys always did. A click on a
   rail row selects it. A rail with its own focus (`↑`/`↓` there, as mocked)
   would need a focus model the workspace does not have.
10. **Step details opens with `enter` on Output**, the attempt being read,
    rather than on a rail row, for decision 9's reason. Task Details is the
    Overview's lower half, a linear document rather than the sidebar
    inspector, and its `o`/`P` pull-request keys moved to the Overview.
11. **Below 130 columns Steps is still a tab**, after Overview: the narrow
    layout has no room for the rail.
12. **The feed folds every other attempt from its step-run row.** No other
    transcript is fetched, so a fold says what the row says (state, agent,
    time, cost, reason, the start of the result), and its tool calls appear
    only when it is the open attempt. The per-tool row format is the chat
    workspace's renderer, unchanged; only `step_started`/`step_finished`, the
    script line and the command-output bar are new.
13. **The Diff has no side file list and no tinted backgrounds.** The folded
    tree already is the file list, and a background tint is a fixed colour,
    which task 150 decision 9 rules out for a palette that follows the
    terminal's theme. Status letters, change bars, line numbers and
    hunk rules carry the retouch.
