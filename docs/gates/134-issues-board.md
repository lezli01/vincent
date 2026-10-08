# Task 134 walkthrough — the issues board

**Acceptance (task 134.19):** the issues view reads as a board of four lanes —
`open`, `in progress`, `hand-off` and `done` — with `done` hidden until asked
for, legibly at the 80×20 floor, following lane moves with no keypress; and
`a` on an issue whose main worktree is busy offers the side worktree.

This walkthrough has **no script**, for the reason M3's,
[017](017-workflow-graph.md)'s and [129](129-monitoring.md)'s have none: what
is judged is whether a screen reads, not what the API returns. The behaviour
underneath is asserted elsewhere — the lane derivation by `internal/store`'s
table test against `issuestate.LaneOf`, the sections, folds and `s` toggle by
`internal/tui`'s tests, the occupant rule and both merge-back modes end to end
by [`134-gate.sh`](134-issue-worktrees.md) — and the guide's issues key table
is held to the binding registry by `TestGuideKeyTablesMatchRegistry`.

The pictures are `scripts/screenshots.sh`'s `tui-issues` (the board, `done`
shown) and `tui-issue` (an issue's detail with its Main worktree section).

## Setup

The screenshot seed is the workload. platform-infra has an issue in every
lane, and none of it adds a task — three tasks the seed already had are
linked to issues, so no task id moves:

| Lane | Issue | Why |
|---|---|---|
| `open` | #4, #5 | no task |
| `in progress` `!` | #9 | its main task, the loop, is `blocked` |
| `in progress` | #10 | its main task, the postmortem, is `paused` |
| `hand-off` | #11 | its only task ended `done` and was archived |
| `done` | #12 | closed as completed |

```sh
./scripts/screenshots.sh seed
export VINCENT_CONFIG_DIR=/tmp/vincent-demo/config VINCENT_DATA_DIR=/tmp/vincent-demo/data
/tmp/vincent-demo/bin/vincent --project platform-infra
```

`./scripts/screenshots.sh clean` stops the daemon and removes the tree.

## Legs

| # | Do | Expect |
|---|---|---|
| 1 | `vincent issue ls --project <platform-infra's id>` in a second shell | `LANE` reads as the table above |
| 2 | In an 80×20 terminal, `:issues` | Three sections, `open`, `in progress` (with `! 1`), `hand-off`, each with its count; the totals line ends `· done hidden (s)`; no closed row; titles truncate before the counts do |
| 3 | `s` | A fourth `done` section, last, holding #12 as `closed · completed`; the totals line ends `· 1 done` |
| 4 | `C`, then `O` | Every section folds to its header with its count and badge, the cursor resting on a header; `O` unfolds them |
| 5 | In the second shell, `vincent issue close 4` | With no keypress, #4 leaves `open` for `done`, and the totals move to `1 open … 2 done`. `vincent issue reopen 4` puts it back |
| 6 | `/tenant_id`, `enter`, `a` | The new-task form, `worktree` preselected `separate`, a note naming the occupant (`main worktree busy with #7`), and a `merge back` row |
| 7 | On the web project, `:issues`, `/flickers`, `enter`, `enter` | The detail's `lane` fact reads `in progress !`, and the Main worktree section names the branch and its occupant at `awaiting_gate`; the Source section is in full |

## Runs

| Date | Terminal | Result | By |
|---|---|---|---|
| 2026-10-08 | VHS 0.11.0 on macOS (darwin/arm64), 80×20 via `stty` for legs 2–5 | PASS, all seven legs. | task 134.19 |
