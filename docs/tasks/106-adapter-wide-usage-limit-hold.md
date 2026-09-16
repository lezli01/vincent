# 106 — Hold every task on an adapter once one of them hits its usage limit

**Status:** ✅ done (5/5)
**Opened:** 2026-09-16
**Issue:** [#399](https://github.com/lezli01/vincent/issues/399)
**Spec:** amends §6, §7.2, §9.6, §11, §12.3, §13.3, §14, §18

## Problem

A `usage_limit` stop held only the task that hit it. Task 026 already recorded
the wall per adapter, as an `observed` row in `agent_quota` with `resets_at` and
`resets_at_reported`, but nothing acted on that row. It was display only (§9.6:
"a near-exhausted agent is shown, never withheld"). So every other task on that
adapter still spawned a CLI process into a window vincent already knew was
closed. Task 003's out-of-scope note had set this *agent-wide hold* aside for
its own issue.

## What shipped

**Just before an agent process would start**, the task actor reads the observed
window for the adapter it has just resolved. If the window is still shut, and
the current `usage_limit_auto_continue` mode is one where a stop would wait, the
actor does not spawn. It re-queues the task with the same `usage_limit` hold a
real stop writes: `admit_not_before` is the observation's `resets_at`, and
`queued_reason` is `usage_limit`. It writes no step-run row and no transcript,
and consumes no retry. The scheduler is not changed.

The task row carries the ordinary hold, so the board cell, the detail header,
`vincent task show` and the notify envelope's `queued_reason` already say why
the task is waiting. No client learns a new concept. The notify envelope gains
`admit_not_before` so it can also say until when.

## Decisions

### 1. The hold is taken in the engine, just before the spawn, not predicted by the scheduler (2026-09-16)

The issue proposed that the scheduler hold the other tasks without writing
their rows. The engine was chosen instead, after weighing both designs:

- **A queued task has no stored adapter.** The adapter comes from resolving the
  step at the cursor against the snapshot (§8.6), and the answer changes with
  guards (§7.7), `parallel` lanes (§7.5), loop bodies (§7.8), a pending repair
  (the request's `agent`, task 025) and a follow-up's own workflow (task 027). A
  scheduler-side guess would be rough: it misses a command step that comes
  before an agent step, and it over-holds a guarded step that would be skipped.
  The engine has already resolved all of that when it reaches the spawn, so its
  answer is exact.
- **Task 081 recorded that the scheduler never parses the snapshot**, and it
  keeps its two imports (`config`, `store`). Predicting adapters would bend that
  decision. Holding in the engine leaves it alone.
- **No read-time derivation.** A scheduler hold would need the API to compute
  the same `queued_reason` at read time, and a second place to compute it is a
  second place for it to drift. It would also never fire notify, since there is
  no transition. It would need `scheduler.WakeOn` to return true for
  `agent.quota_changed`, amending task 026 decision 6 and its pinning test. None
  of that is needed here, and `WakeOn` stays as it is.

Cost, accepted: a held task still takes **one admission per window** with no
spawn. That is `queued → running → queued`, two `task.state_changed` events,
and a notify if `running` or `queued` is in `notify.on`. A task's first
admission also creates its worktree and container before it reaches the check,
which would have happened anyway. Any command steps before the agent step run
too. The cursor advances past them, so they do not run again after
re-admission. The issue's "tests in `internal/scheduler`" moved to
`internal/taskrun` for this reason.

The "only `internal/scheduler` does `queued → running`" invariant is untouched.
The actor re-queues with `taskstate.Interrupt`, the only `running → queued`
edge, exactly as `holdForUsageLimit` already did.

### 2. The hold applies only in the modes where the stop itself would wait (2026-09-16)

The check reads `usage_limit_auto_continue` when it runs, not a cached value, so
a hot reload reaches the next check (task 091 decision 6's rule):

| mode | the check holds when the adapter's observed window … |
|---|---|
| `always` | has `resets_at` in the future |
| `reported_only` | has `resets_at` in the future **and** `resets_at_reported` is true |
| `never` | never. Each task finds the limit itself, exactly as before |

When the check does not hold, the task spawns. If that run then stops on the
limit, the existing `usageLimitStop` path decides between holding and blocking,
unchanged.

Why: `never` and `reported_only` exist because the operator does not trust the
classifier (the #348 incident). Holding or blocking the whole adapter queue on
a match they distrust would spread one wrong match to every task. Letting the
next task spawn is also what retires a wrong observation: a successful run
clears it (task 026 decision 3). Blocking other tasks early under `never` was
considered and rejected for that reason.

### 3. Only observed windows trigger the hold, never reported quota (2026-09-16)

Only `agent_quota` rows with `source = observed` trigger the check. Those are
windows vincent saw close through a real `usage_limit` stop. Reported readings
(codex's `app-server`, claude's status-line push) stay display only. They live
only in the catalog cache (task 082 decision 4), a restart clears them, and a
window at 100% is not a proven stop. The check filters on `observed` even though
the table holds only observations today, so a future writer cannot change
admission behaviour by accident.

Consequence, recorded: codex and cursor recognize no quota wording (task 003
decision 2), so nothing ever records an observation for them. This hold never
applies to them, just as task 091 has no effect on them.

### 4. The early check reuses the stop's outcome shape, but records nothing (2026-09-16)

- The seam is `runAttempt` in `internal/taskrun/engine.go`: right after
  `resolveSelection` for a `StepAgent`, and **before** `openTranscript` and
  `CreateStepRunTakingOverride`. Nothing appears on the timeline, and an
  `edit + retry` override stays on the task for the attempt that really runs.
  Every agent spawn goes through here, including repairs (`RepairStepID`),
  follow-up rounds, `parallel` lanes and loop bodies. Fan-out lanes are their
  own tasks, and each gets its own check.
- It returns `stepOutcome{state: StepInterrupted, reason: ReasonUsageLimit,
  agentName, wall: <the observation>}`, where `wall` is a new field. That shape
  already travels through `collectGroup` (interrupted outranks everything) and
  the loop's default branch, and neither needed changes. It never reaches the
  cost-cap check, because interrupted outcomes are excluded there.
- The two interrupted branches that handle quota stops branch on
  `outcome.wall != nil`: `runSteps` in `engine.go`, and `runRepair` in
  `repair.go`. The repair branch leaves the repair request undrained, as its
  hold branch already did. Both call `holdForUsageLimit` with the observation's
  `resets_at` and **skip `usageLimitStop`**. The check saw nothing new.
  Re-recording would stamp a new `observed_at`, and could mark a vincent
  estimate as a CLI-reported reset (`resets_at_reported: true`), turning the
  board's `≈` into `→`. No `agent.quota_changed` event is emitted.
- A `parallel` group whose lanes use different adapters runs the open lanes and
  skips the walled lane without writing a row, and the collected outcome holds
  the task. On re-admission only the unfinished lane runs (§7.5).
- `queued_reason` stays `usage_limit`, with no second reason for the same
  condition (task 091 decision 2, T1.5/T1.6 decision). The transition's event
  payload gains `agent` on both hold paths, so the timeline says which window
  the task is waiting on. The engine logs a separate line ("adapter's usage
  window is still shut; not spawning"), so an operator can tell a wall that was
  hit from one that was avoided.
- **If the observation cannot be read, the step runs anyway.** A store error
  reading `agent_quota` is logged and the step spawns. A display table's read
  failure must not stall work; `recordUsageLimit` applies the same rule to its
  write.
- The quota lookup, `usageWall`, lives beside `recordUsageLimit` and
  `clearUsageLimit` in `internal/taskrun/quota.go`. That file's header, which
  said "Nothing here touches admission … a near-spent agent is displayed, never
  withheld", was rewritten to say what is now true.

### 5. Every held task is released when the window reopens; no single test run first (2026-09-16)

When `resets_at` passes, every held task becomes admissible under the normal
§11 caps. So re-checking after an *estimated* reset costs up to
`max_parallel_tasks` spawns, not one. That is still at most the cap per
re-check; before this task it was one spawn for every queued task on the
adapter. The alternative was to let one task try first and hold the rest until
it succeeds or hits the limit (a circuit breaker's half-open state). That needs
per-adapter "attempt in flight" state, which neither the scheduler nor the
actors have. Tasks 003, 028 and 091 all rejected adding that kind of state. It
gets its own issue if the cap-sized cost matters in practice.

Also recorded: when an observation is cleared early by evidence (a task already
running on that adapter succeeds), tasks already holding their own
`admit_not_before` still wait until that time and are not released early. That
wait is at most one `usage_limit_recheck_interval` for an estimate, and a
reported reset is presumably accurate anyway.

Also recorded: task 003 decision 1 accepted that pausing and then resuming a
held task makes it hit the limit again, at the cost of one spawn. While the
observation is live, that now costs no spawn: the early check catches it on
re-admission. §6's paragraph is amended to say so.

### 6. The notify envelope gains `admit_not_before` (2026-09-16)

The envelope already carried `queued_reason`, which says *why*. It did not say
*until when*, which a one-line notifier needs without calling the API back
(task 046 decision 5). `internal/notify.Envelope` now has a nullable
`admit_not_before` (RFC3339) beside `queued_reason`. The change is additive and
also covers `retry_backoff` holds. Chats (§11, task 063) are out of scope: a
chat turn is never queued, and sending one is a human action that means go.

## Deviations from the issue

1. **The hold is taken in `internal/taskrun`, not `internal/scheduler`**, and it
   writes the task row rather than suppressing admission. See decision 1.
2. **Tests live in `internal/taskrun`**, not `internal/scheduler`, for the same
   reason.
3. **Added, beyond the issue:** `admit_not_before` on the notify envelope
   (decision 6).

## Tasks

- [x] **106.1** `internal/taskrun`: `usageWall` in `quota.go` (mode read live,
  `observed` rows only, a read error logged and spawned past), the check in
  `runAttempt` ahead of the transcript and the row, `stepOutcome.wall`, the
  `wall` branches in `runSteps` and `runRepair`, and `agent` on the hold
  payload. ✓ 2026-09-16
- [x] **106.2** `internal/taskrun/quotahold_test.go`, driven by a clock shared
  by the runner and the scheduler: a walled `always` spawn holds with no row,
  no transcript, no retry and an unchanged observation with no
  `agent.quota_changed`; an expired observation spawns; the mode table; another
  adapter's wall does not hold; a command step ahead of the wall runs once; a
  `parallel` group runs only its open lane, then only its walled one; a repair
  and a follow-up round hold with their requests undrained; an `edit + retry`
  override survives the hold and is drained onto the real attempt; pause then
  resume holds again without spawning; an unreadable observation is logged and
  spawns. ✓ 2026-09-16
- [x] **106.3** `internal/notify`: `Envelope.AdmitNotBefore`, tested for a held
  transition and as JSON `null` otherwise. ✓ 2026-09-16
- [x] **106.4** `scripts/m2-gate.sh` scenario 14: task B, created on a claude
  adapter task A walled, is held with zero step runs and an `admit_not_before`
  equal to `/v1/agents`' `resets_at`, and both finish unattended; under `never`,
  B spawns and blocks on the limit itself. Scenarios 5 and 13 re-run green.
  ✓ 2026-09-16
- [x] **106.5** §6, §7.2, §9.6, §11, §12.3, §13.3, §14 and §18 amended, dated;
  task 003's out-of-scope note and task 026 decision 3 point here; the
  features, TUI, agents, troubleshooting, task-lifecycle and configuration
  pages and `CHANGELOG.md` updated. ✓ 2026-09-16

## Verification

- `go run mage.go test` green.
- `go test -race` on `internal/taskrun`, `internal/notify` and
  `internal/scheduler` green; the full `-race` suite is left to CI.
- `go tool golangci-lint` built for the host and run with `GOOS=windows`,
  `darwin` and `linux`: green.
- `VINCENT_GATE_SCENARIO=5`, `13` and `14 ./scripts/m2-gate.sh`: green on macOS.
