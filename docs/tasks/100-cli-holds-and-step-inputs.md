# 100 — Show admission holds and recorded step inputs in `vincent task show`

**Status:** ✅ done (4/4)
**Opened:** 2026-09-15

*Issue [#390](https://github.com/lezli01/vincent/issues/390).*

The issue says the CLI cannot see a task's admission hold or what a step was
given. That is half right. `vincent task show --json` already prints
`apiclient.TaskDetail` unchanged, which carries `queued_reason` and
`admit_not_before` (task [003](003-usage-limit-classification.md).6) and every
step run with task [088](088-step-details-tab.md)'s recorded fields. What was
missing is the **text** view: `task show` printed `blocked` but had no row for a
hold, and nothing a step run was handed. So this is client rendering only. The
daemon, the API, the store and the MCP server do not change.

It conflicts with no recorded decision. Task
[047](047-cli-logs-and-transcripts.md) decision 9 is kept: a read-only view on a command
line breaks no rule, and task 025/027's "TUI-and-API only" stance is about
writes. Decision 7 is kept: the renderer is new code rather than the TUI's
lipgloss panes, and it is ASCII. Task 003 decision 1 means a queued task has a
hold and never a `block_reason`, so the two rows cannot both appear. Task 088
decisions 2, 3, 5 and 9 stand as they are.

## Decisions

**1. One attempt is printed by `task show <id> --step RUN`, and only that attempt.** *(2026-09-15)*

`RUN` is a step_run id: the `RUN` column `task show` prints and the id `task
transcript --step` takes. With `--step` the command prints a header line (run
id, step id, attempt, state), then the four sections of the TUI's Step Details
tab in its order — input, resolution, control flow, outcome — under the tab's
rule for when a field appears: an agent step always has a prompt entry and a
command step a run entry, `check` and `if:` only when recorded. Without
`--step`, `task show` is unchanged apart from decision 3's row.

Beaten: appending the attempt below the task view, which buries the answer
under output nobody asked for; a separate subcommand, which splits one noun
across two commands; every attempt inline, which makes a retried task hundreds
of KiB.

**2. `--step RUN --json` prints the API's `StepRun` unchanged.** *(2026-09-15)*

It is the matching element of `GET /v1/tasks/{id}`'s `steps[]`, the shape `task
show --json` and MCP `task_steps` already use. There is no new wire shape; the
exact bytes are `--json | jq -r .rendered_prompt`. Beaten: a CLI-specific object
grouped by section, a second shape to keep in sync.

**3. The hold is one `hold` row in `task show`; `task ls` is unchanged.** *(2026-09-15)*

The row appears only when `apiclient.Task.Hold()` reports ok, and reads
`<queued_reason> until <admit_not_before>` with the time as local RFC3339, as
`doctor` and `daemon status` print instants. With no resume time it prints the
reason alone, the stance the TUI's `renderDetailState` takes. The reason is
printed generically because there are two producers, `usage_limit` and
`retry_backoff` (task 028). It sits where the `blocked` row does. Beaten: a hold
in `task ls`'s STATE column, which scripts parse and the issue did not ask for.

**4. Bodies are printed in full and indented, with truncation and the daemon's text marked.** *(2026-09-15)*

With `input_truncated` set, the input section opens by saying so (088 decision
2's 64 KiB ceiling). In an agent step's prompt, an ASCII
`--- appended by vincent: the previous attempt's failure ---` line separates the
workflow's text from the `<previous-attempt-failure>` block (088 decision 3). A
nil field prints `not recorded`, a non-nil empty string `rendered empty` — the
TUI's two wordings, which 088 keeps as different facts. A `for_each` list is
printed as its items, not raw JSON.

The TUI and the CLI must agree on where the appended block starts, so
`splitFailureTrailer` moved from `internal/tui` to
`apiclient.SplitFailureTrailer`, and both clients call it. The small value
formatters stay separate in each client, as 047 decision 7 allows.

## Work

- [x] **100.1 — `apiclient.SplitFailureTrailer`, moved from the TUI's Step Details tab, with a round trip through `workflow.AppendFailureBlock`.** ✓ 2026-09-15
- [x] **100.2 — `internal/cli`: the `hold` row (`taskHoldRows`).** ✓ 2026-09-15
- [x] **100.3 — `internal/cli`: `task show --step` and the ASCII renderer in `taskstep.go`.** Depends: 100.1. ✓ 2026-09-15
- [x] **100.4 — The CLI reference, the §12.1 amendment and `CHANGELOG.md`.** ✓ 2026-09-15

## What the tests prove

`internal/cli`'s renderer tests, over `apiclient.StepRun` values: a nil prompt
and an empty one print different text; a command step prints its run, shell and
working dir and no unrecorded `check`; the truncation notice; the divider present
on a retried prompt and absent otherwise; sourced values, a missing level and a
zero timeout; the guard, iteration, `for_each` item and list, and lane; and
ASCII-only output. `taskHoldRows` is table-tested over the ordinary queue, both
reasons and a hold without a resume time. Against a stub daemon: an unknown run
exits 1 with the message, `--step --json` decodes equal to `task show --json`'s
`steps[]` element, `--step` prints the attempt and not the task, and `task show`
prints the `hold` row. One real-daemon e2e runs a command step to `done` and
reads its rendered body, shell and working dir back through `--step`.
`internal/apiclient` round-trips the trailer split through
`workflow.AppendFailureBlock`, so the tag cannot drift.

No gate script: gates assert over curl, and this renders fields the API already
serves.
