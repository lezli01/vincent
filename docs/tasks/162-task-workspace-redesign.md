# 162 — Task workspace redesign: a steps rail, plain tabs, an Output feed, a retouched Diff and an Overview of cards

**Status:** ⬜ not started (0/9) · **Opened:** 2026-11-20

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

- [ ] **162.1** Steps rail: one row per step (state glyph, name, right-aligned
  duration) on a connecting line; a dim second row only when the step has
  something to say (retry count, gate, agent · cost, a block reason); attempts
  listed only under the selected step and only when there is more than one;
  fan-out lanes as a `├─`/`└─` tree with their task number and state, earlier
  rounds folded to one line; a loop as one pip per iteration; a condition's
  `if:`. `↑/↓` move between steps, `←/→` between the selected step's attempts.
  Shown on every tab.
- [ ] **162.2** Tabs: Overview, Output, Diff, Workflow, Pull request (the last
  only when a pull request is linked), cycled with `tab`/`⇧tab`; the digit
  bindings and the tab strip's digits removed; Step details and Details folded
  away (decision 3). A tab may carry a marker: the diff's size, a red `!` on
  Overview when the task needs a human.
- [ ] **162.3** Linked issue: the task header names the issue on every tab
  (`◇ issue #N title  state  i open`), or a dim "no linked issue"; `i` (a new
  keymap op on the task surface) opens the issue view, and `esc` returns to the
  task on the tab it left. Drop the right-hand Details pane; its facts move to
  the header and Overview (decision 1).
- [ ] **162.4** Output feed: one continuous feed for the whole task. A divider
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
- [ ] **162.5** Per-step commit range (store + engine): a migration adding the
  HEAD a step run started and finished at; the taskrun actor journals both
  (crash-first: the start before the agent runs, the end before the step run is
  finalized); recovery leaves an interrupted run's end empty. Exposed on the
  step-run response.
- [ ] **162.6** Diff by step (API + TUI): `GET /v1/tasks/{id}/diff?step_run=`
  diffs the run's start..end range (409 with a reason when a range is not
  recorded — rows from before 162.5, or a run that never finished). The tab
  gains a summary line naming the base and the scope, a file list with
  M/A/D/R letters and change bars beside the hunks (falling back to inline
  folds when narrow), line numbers, tinted added and removed lines that keep
  their `+`/`−`, hunk headers carrying the function context; `d` toggles whole
  task and the step selected in the rail.
- [ ] **162.7** Overview of cards: a status card on top chosen by the existing
  three frames (progress bar and step names; a red attention card with the
  reason, the already-fetched evidence lines and each action with what it does;
  an outcome card with diff size, commits and the pull request); then Now beside
  The task; then Linked (issue, pull request, chats, branch and base, each row
  openable with `enter`) beside Usage (elapsed, tokens, cost, workflow and
  origin, worktree).
- [ ] **162.8** Docs and spec: §15's task workspace, key and tab text; §13.2
  for the diff's `step_run` and the step-run fields; §5 for the new columns;
  `docs/reference/` key tables (from `internal/tui/bindings.go`), API and the
  task guide; `CHANGELOG.md`; an assertion for the per-step diff in a gate.
- [ ] **162.9** Re-capture every `docs/assets/tui-task-*.png`, `tui-diff.png`,
  `tui-lane-*.png` and `tui-task-steps-lanes.png` with
  `scripts/screenshots.sh`, and drop any shot whose tab no longer exists.

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
   work (162.5, 162.6). Step runs do not record a commit today, so a run from
   before the migration has no range and says so rather than guessing one.
7. **One pull request**, mentioning "closes 162".
