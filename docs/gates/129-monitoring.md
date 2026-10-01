# Task 129 walkthrough — monitoring in three questions

**Acceptance (task 129.19):** from the board, each of the three monitoring
questions — what is happening now, where and why did it fail, what did it
deliver — is answered in one or two keys, legibly, in a real terminal, at 80
columns and without colour as well as at full size.

This walkthrough has **no script**, deliberately, for the reason
[088](088-step-details-tab.md)'s and M3's have none: what is judged is whether
a surface reads, not what the API returns. The behaviour underneath is
asserted by `internal/tui`'s tests — the Overview's frames, the failure and
outcome cards, `N`, `H` and the attempt strip — and the guide's key tables are
held to the binding registry by `TestGuideKeyTablesMatchRegistry`.

The journeys are written up in the guide's
[Monitoring in three questions](../guides/tui.md#monitoring-in-three-questions);
the three Overview pictures there are `scripts/screenshots.sh`'s
`tui-task-overview-running`, `tui-task-overview-blocked` and
`tui-task-overview-done` tapes.

## Setup

The screenshot seed is the workload: a running soak, a blocked task, a done
task with a linked pull request, a blocked loop and a two-round fan-out, each
in its own throwaway config and data dirs under `/tmp/vincent-demo`. It is
macOS/Linux-only, like the script.

```sh
./scripts/screenshots.sh seed
export VINCENT_CONFIG_DIR=/tmp/vincent-demo/config VINCENT_DATA_DIR=/tmp/vincent-demo/data
/tmp/vincent-demo/bin/vincent
```

The seed has no fan-out with a *blocked* lane, which leg 8 needs. Save this as
`/tmp/vincent-demo/config/workflows/walk-lanes.yaml` and create one task on it
from New task (`n`) on any project:

```yaml
name: walk-lanes
description: one lane that passes and one that blocks
steps:
  - id: lanes
    type: fan_out
    lanes:
      - id: good
        steps:
          - {id: pass, type: command, run: exit 0}
      - id: bad
        steps:
          - {id: fail, type: command, run: exit 1}
```

`./scripts/screenshots.sh clean` stops the daemon and removes the tree.

## Legs

| # | Do | Expect |
|---|---|---|
| 1 | Read the board | Each row's state cell and `STATUS` say what the task is doing; the fan-out parent reads `waiting on lanes (…)` with its lanes counted by glyph |
| 2 | `/harden`, `tab`, `enter` | The soak opens on **Overview**: its current step and attempt. The header's breadcrumb reads `Board › #id › Overview`, and the dim line under it follows what the attempt prints |
| 3 | `3`, then scroll up, then `f` | Output's attempt strip shows the spinner, elapsed time and `▼ following`; scrolling away turns it to `⏸ paused · N new`, and `f` follows again. `v` changes the level and the strip names it when it is not `normal`; `ctrl+o` shows `raw` |
| 4 | `esc`, then `!` | The board's cursor moves to the next task that needs you, and again on each press |
| 5 | `H`, then `H` | The first press leaves only the tasks that need you, the fan-out parent whose lane is blocked among them, and the panel title says so; the second shows every task again with the cursor on the same task |
| 6 | `enter` on the checksum task (`/signed checksums`) | A **failure card**: `✗`, the step and attempt, the reason in words with its code dim beside it, the evidence tail of the failing attempt, and actions starting `r`, `E`, then `R` and `T` |
| 7 | `3` from that card | Output opens on the failing attempt where it failed — at the check's first line for `check_failed`, at the end otherwise — and is not following |
| 8 | On the `walk-lanes` parent, press `N` repeatedly | `N` opens the `bad` lane at its failure, keeps walking that lane's failures, and wraps; `esc` returns to the parent. On the loop task (`/tenant_id`) `N` walks each failed iteration in the order it ran and wraps |
| 9 | `enter` on the design-tokens task (`/design tokens`) | An **outcome card**: the result with where it came from, changes, commits and `⇡ #412` with its checks |
| 10 | `w`, then `esc` back to Overview, `4`, then `7` | `w` opens the result's attempt in Output at its end; `4` opens Diff; `7` opens Pull Request |
| 11 | Resize the terminal to 80 columns and repeat legs 1, 6 and 9 | Nothing needed to answer a question is cut: the failure card keeps its first line and action keys, the outcome card shrinks first, and the tab strip's second group shortens before the first |
| 12 | Relaunch with `NO_COLOR=1` and repeat legs 1, 6 and 9 | Every state, failure and outcome still reads from its glyph and word alone |

## Runs

| Date | Version | Platform | By | Result |
|---|---|---|---|---|
| — | — | — | — | not yet walked |

Add a row per walk. A surface that has never been walked on a platform is not
known to read correctly there.
