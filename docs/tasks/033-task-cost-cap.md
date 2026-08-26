# 033 — `max_task_cost_usd`: a per-task cost cap enforced at attempt boundaries

**Status:** ✅ done (4/4)
**Opened:** 2026-08-26

Cost was measured and never acted on. `step_runs.cost_usd` is written from the
adapter's terminal `result` line (`internal/taskrun/steps.go`), rolled up across
every attempt by `store.TaskRollups`, and rendered on the board and detail views
— and nothing in the engine, the scheduler or the store ever read it to decide
anything (§17).

That left a gap between the two guards that do exist. `agent_timeout` bounds one
attempt's wall clock; `transcript_max_bytes` bounds its bytes on disk. An agent
that loops *productively* — each turn quick, each turn's output modest — trips
neither, and can spend its full `agent_timeout` per attempt across the whole
retry budget with nothing but a human noticing to stop it.

This adds one configured ceiling on one task's rolled-up spend, checked by the
engine at every attempt boundary, that blocks the task with a new `cost_limit`
reason. It is deliberately small: no migration, no `tasks` column, no
`taskstate` edge, no workflow field, and no API, TUI or CLI surface for a budget
— `r.fail(task, ReasonCostLimit, …)` is `taskstate.Fail`, the same
`Running → Blocked` every other block takes.

Opened from [#97](https://github.com/lezli01/vincent/issues/97), whose proposed
key (`max_cost_usd`, described as a "global spend cap") and open question about
`resume` are both answered by decisions 1 and 4 below.

## Decisions

### 1. The key is `max_task_cost_usd`, and the cap is per task (2026-08-26)

The issue proposed `max_cost_usd` and called it a global spend cap. That is not
what the enforcement does. `TaskRollups` is keyed by task id, and a `fan_out`
lane is its own task row (014 decision 1: a lane is an ordinary task, four
columns are the whole difference). A cap of `$5` on a twenty-lane fan-out
therefore permits `$100` across the tree, and the parent's own rollup never sees
a lane's spend.

A daemon-global promise the code does not keep is worse than an honest per-task
one, so the key was renamed rather than the enforcement widened. The
multiplication is documented — §12.3, the config reference and the
troubleshooting page all say it in as many words — rather than worked around.

**Beat:** a per-tree cap. It needs a recursive rollup over `parent_task_id` and
a rule for which task blocks when the tree total trips, and neither is a
question this issue asked. Layerable later against the same enforcement point.

### 2. The check fires at every attempt boundary, not at top-level step boundaries (2026-08-26)

It lives in `runStepWithRetries`, immediately after the attempt's row is
finished by `runAttempt`, and its verdict propagates outward through the group
and loop collectors on `stepOutcome.costExceeded`.

`runSteps` walks top-level positions only: a `loop` is one position whose body
attempts belong to `runIteration`, and a `parallel` group's sub-steps run
concurrently and are reduced by `collectGroup`. Checking only there would let a
fifty-iteration loop, or a step's whole `max_retries` budget, run before the cap
was consulted once — the overshoot would be a whole loop rather than the "at
most one attempt" the cap promises.

`costExceeded` inherits `backoffUntil`'s standing rule verbatim: **every branch
that turns an outcome into something else must test it first.** That is
`collectGroup`'s new first tier, the `allow_failure` arm in `group.go`, and
`runBodyStep`'s early return in `loop.go` — which also covers the branch that
*discards* a successful body step's outcome, the one that makes the cap fire
mid-loop rather than after it.

**Beat:** the top-level check the issue implied. Cheaper by a few lines and
wrong by up to a whole loop.

### 3. A cost verdict beats a step failure at the same boundary (2026-08-26)

When an attempt fails with retries remaining *and* the rollup is over the cap,
the cap wins: the task blocks `cost_limit` and the due retry never runs, because
retrying spends more money to arrive at the same wall. This also pre-empts 028's
`retry_backoff` re-queue — a paced retry is still a retry, and holding the task
so it can spend again is a hold that learns nothing.

The failed row keeps its own state and its own failure reason, so the timeline
still says what broke while `block_reason` says why nothing further was tried.

Two exceptions, both deliberate. An **interrupted** attempt is not a boundary:
the daemon is stopping or the task was canceled, that attempt consumed no retry,
and the task is re-admitted — where the next attempt asks the same question of
the same rollup. `collectGroup` keeps interruption ahead of the cap for exactly
that reason. And a **repair** run ends `blocked` with the reason the task was
blocked with, which says more than `cost_limit` would; the next ordinary retry
is where the cap speaks.

### 4. `retry` from a `cost_limit` block makes one attempt of progress, then re-blocks (2026-08-26)

The issue's open question asked whether `resume` should grant a one-step
exemption. The premise is wrong: `resume` is valid only from `paused`
(`internal/taskstate`: `Resume: {Paused: {To: Queued}}`); from `blocked` the
human actions are `retry`, `repair`, `skip` and `cancel`.

Because the check runs only *after* an attempt finishes, a retry always advances
by one attempt before re-blocking. That is idempotent, loses no work, adds no
durable state, and the human pressing retry is the deliberate act that
authorises that spend. The task's step cursor deliberately does not advance on a
cost block — a blocked task stays at the step it is on, which is what lets a
group resume its unfinished sub-steps and a loop resume its iteration from the
rows.

The documented remedy stays "raise the cap and retry"; the
retry-without-raising path is documented as costing one more attempt each time.

**Beat:** refusing the action with a `409`, which needs a second kind of
"invalid action" beside the FSM's; and a one-step exemption flag, which is a new
`tasks` column with a clearing rule to get wrong.

### 5. The adapter gap is stated, never emulated (2026-08-26)

Only claude reports cost. Codex (§9.3) and cursor (§9.7) leave `CostUSD` nil, so
on those adapters the cap is inert — ignored at run time, exactly the way every
other capability an adapter lacks is handled.

The check is guarded by `store.TaskRollup.HasCost` **as well as** `> cap`, so a
task whose attempts all reported nothing is untouched by construction rather
than by arithmetic. `HasCost` is what keeps "unreported" and "genuinely free"
different facts, and the TUI's `costCapLine` renders an unset cap as `off` for
the same reason `formatCost` renders an unreported cost as `—`: `$0.00` is a
claim the daemon never made.

**Beat:** estimating cost from token counts, which all three adapters do report.
A per-model price table inside vincent would be wrong the week a price changed,
and silently.

### 6. Enforcement is at boundaries only, so the crossing attempt completes (2026-08-26)

Unchanged from the issue as written and reaffirmed. Cost arrives on the terminal
`result` line and nowhere else — `parseResult` in `internal/agent/claude/stream.go`
is its sole reader — so there is no mid-run usage signal to poll. Mid-run
enforcement means new per-adapter stream parsing plus engine polling, which is a
much larger change and the natural follow-up if boundary checking proves too
coarse. A user setting the cap should expect to overshoot it by at most one
attempt; `agent_timeout` still bounds that attempt.

## Tasks

- [x] **033.1** `internal/config`: `MaxTaskCostUSD float64` (`max_task_cost_usd`)
  at the top level beside `TranscriptMaxBytes`, zero by default, non-negative
  validation in the neighbours' shape, and the commented key in
  `bootstrap.go`. Hot reload needed no work — the engine reads
  `r.deps.Config()` per check. ✓ 2026-08-26
- [x] **033.2** `internal/taskrun`: `ReasonCostLimit`, `stepOutcome.costExceeded`,
  `overCostCap` after the attempt in `runStepWithRetries`, the block ahead of
  the outcome switch in `runSteps`, and the propagation branches in `group.go`
  (`collectGroup`'s first tier and the `allow_failure` arm) and `loop.go`
  (`runBodyStep`'s early return). ✓ 2026-08-26
- [x] **033.3** Surfaces: `max_task_cost_usd` on the `GET /v1/config` DTO and
  `apiclient.Config`, and `costCapLine` on the TUI's daemon view — `off` for
  zero, never `$0.00`. ✓ 2026-08-26
- [x] **033.4** Tests and documentation: engine tests against `cmd/fakeagent`
  (block, off/generous/equal, no retry consumed and a retry buying exactly one
  attempt, the cap beating a failure with budget left with no `retry_backoff`
  hold, firing mid-loop, inert on codex and cursor, and a raised cap reaching a
  running daemon); config load, rejection and default cases; API and TUI
  surface tests. §12.3, §17 and §18 amended, dated; `configuration.md`,
  `task-lifecycle.md`, `troubleshooting.md` and `guides/agents.md` updated.
  ✓ 2026-08-26

## Noted, deliberately not folded in

- **A token cap.** All three adapters report input/output tokens, so a token
  budget would work uniformly where a cost cap does not. It is not the primary
  knob because the thing being protected is money, and a token budget puts a
  per-model conversion between the user and it. Worth revisiting as a second key
  if the codex/cursor gap turns out to matter in practice.
- **No gate script.** This is engine behaviour with a config input, which
  `internal/taskrun`'s tests prove directly, and there is no human judgement in
  it of the kind M3 and 017 have.
