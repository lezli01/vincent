# 091 — `usage_limit_auto_continue`: an operator control over the quota wait

**Status:** ✅ done (5/5)
**Issue:** #348
**Amends:** §7.2 — the usage-limit bullet's "`usage_limit` is therefore a
`queued_reason`, never a `block_reason`" becomes conditional on the mode; §11 —
the admission-holds paragraph's "two producers", where `usage_limit` is one only
in the modes that hold; §12.3 — the new key beside
`usage_limit_recheck_interval`, and the generated `config.yaml` sample; §18 —
the "Agent stopped by a usage limit" row
**Keeps, without relitigating:** [003](003-usage-limit-classification.md)
decision 1 — a *queued* task still never carries a `block_reason`; what changes
is that a quota stop may produce a `blocked` task instead of a queued one, which
is a different thing from overloading the reason on a held row.
[003](003-usage-limit-classification.md) decision 3 and
[028](028-retry-backoff.md)'s beat — still no exponential backoff and still no
per-task hold state; a mode is resolved configuration read at the stop, exactly
as the interval is. [003](003-usage-limit-classification.md) decision 2 —
codex and cursor still recognize no quota wording, so this key is inert on them

Task 003 built the wait and nothing since built a way out of it.
`usage_limit_recheck_interval` tunes how long a task waits when the CLI named no
reset; it is validated positive, so it cannot say "do not wait", and
`max_task_cost_usd` defaults off and only stops a loop after the money is spent.

That gap matters because the classification can be wrong. An adapter recognizes
a quota wall by *wording* (§9.1), and the incident behind #348 was a claude run
that succeeded and was read as a quota stop anyway: the step was drafting text
about quota handling, quoted the marker list back in its final message, and
matched itself. It re-queued behind a fresh estimate, re-drafted the same text,
matched again — three attempts and roughly $2.85 before a human looked. The
misclassification itself is a separate fix; this work is the operator control
that would have stopped the loop at the first occurrence without patching the
daemon.

## Tasks

- [x] **091.1** `usage_limit_auto_continue` in `internal/config`: the field
  beside `UsageLimitRecheckInterval`, the `always` default, enum validation
  modelled on `log_level`'s, and the commented entry in the generated
  `config.yaml`.
- [x] **091.2** The engine: `usageLimitStop` computes the effective reset,
  records the per-adapter observation and answers hold-or-block;
  `holdForUsageLimit` writes only the hold it was told to write, and the block
  is the caller's `fail` with `ReasonUsageLimit`. `runRepair` takes the same
  decision and routes to `finishRepair` where it says not to wait.
- [x] **091.3** The clients: `GET`/`PATCH /v1/config`, the `apiclient` wire
  types, `vincent config get|set`, and a `kindEnum` key in the TUI daemon view.
- [x] **091.4** `scripts/m2-gate.sh` scenario 13: `never` against a CLI that
  named no reset blocks, `reported_only` against one that did still holds.
- [x] **091.5** The spec amendments above, the configuration, task-lifecycle,
  troubleshooting, agents and features pages, and the changelog.

## Decisions

### 1. A tri-state enum, not the boolean the issue proposed

*2026-09-08.* `always | reported_only | never`, default `always`.

The incident that motivated the issue had **no CLI-reported reset**: prose
matched the marker list and the hold was the 15-minute estimate.
`reported_only` blocks exactly that class of stop — the one where vincent is
guessing at the window — while keeping unattended recovery for the case the
feature was built for, a CLI that names when it reopens. A boolean cannot
express that split.

The shape is cheap: `log_level` is the precedent for a validated string enum in
`config.Validate`, `kindEnum` plus a `choices` var the precedent in
`internal/tui/configkeys.go`, and `cmd/fakeagent` already drives both legs
through `FAKEAGENT_USAGE_LIMIT_RESET`, so `reported_only` cost no new agent
scenario.

**Beat:** the boolean. It would have shipped a switch that is either "never
recover unattended" or "keep the behaviour that spent the money", with nothing
between them.

**Beat:** letting `usage_limit_recheck_interval: 0` mean off. Zero is already
invalid because it re-admits on the very next tick — the respawn loop task 003
exists to stop — and overloading it would make one key mean both "how long" and
"whether". A very large interval was beaten too: it leaves the task `queued`
indefinitely under a reason that says a wait is in progress, while holding a
queue position nobody was told about. `blocked` is the honest state for "this
will not move without you".

**Beat:** a consecutive-hold cap — block after N quota holds in a row on one
step. It bounds a runaway without giving up auto-continue, but it is per-task
state the row would have to carry, which is the objection decision 3 of task 003
already raised against exponential backoff. Worth its own issue if the enum
proves too blunt.

### 2. Blocking reuses `usage_limit` rather than minting a second reason

*2026-09-08.* §7.2 said `usage_limit` is "never a `block_reason`" and §11 named
it as one of two admission-hold producers. An off switch makes it both, so one
of those had to move: amend the two sentences, or block under a new reason.

Amending is the better answer and it is what landed. The reason names the same
condition either way — the account's window is spent — and
`internal/taskrun` and `internal/worktree` share one reason vocabulary
(T1.5/T1.6 decision). A second reason would make `usage_limit` mean "quota,
waited" in one place and something else mean "quota, blocked" in another, for
one condition, and every client that renders a reason would have to learn both.

### 3. The block is taken in the *interrupted* arm, out of `allow_failure`'s reach

*2026-09-08.* Both outcomes are decided in the `StepInterrupted` arm of the
engine's outcome switch, above the `StepFailed` arm where `allowFailure` is
consulted. §7.2's "`usage_limit` and `interrupted` are untouched by
`allow_failure`" therefore stays literally true in every mode: a workflow must
not be able to branch on "the account is out of quota" as though it were a test
result.

The attempt row is already `interrupted` from `finishStepRun`, so **no retry is
consumed** in any mode — the same shape `cost_limit` takes
([033](033-task-cost-cap.md)). The step did nothing wrong, and a human retrying
after the window reopens starts with a full budget. The cursor does not advance
either, so the retry re-runs the step it stopped at.

### 4. The per-adapter observation is recorded in every mode

*2026-09-08.* `recordUsageLimit` (task 026) runs before the hold-or-block
decision, including for the `now + usage_limit_recheck_interval` estimate with
`resets_at_reported: false`. That record is what puts the board badge and the
`agent.quota_changed` event in front of the operator, and it is precisely how
someone learns *why* a task just blocked. A config key must not be able to make
the board disagree with itself.

Consequence, accepted: `usage_limit_recheck_interval` keeps a meaning in every
mode — it is the estimate — even where it no longer times a wait. Under
`reported_only` that estimate is what separates a hold from a block.

### 5. A quota stop during an ad-hoc repair restores the reason the task was blocked with

*2026-09-08.* The issue's "`repair.go` reaches the same helper and needs no
separate branch" is not quite true. `finishRepair` restores `req.BlockReason`
deliberately, and the comment beside the cost cap in `runRepair` records the
reasoning: the reason the task was blocked with says more than the reason the
repair ran into.

So the quota arm of `runRepair` gets one branch — hold in the modes that hold,
`finishRepair` in the modes that block, request drained the way every finished
repair drains it. The switch still does its job there: the repair does not
re-run unattended.

### 6. A mode flip does not convert holds already in flight

*2026-09-08, recorded rather than fixed.* The mode is read at the moment of the
stop, the way the interval already is, so a hot reload (§12.3) reaches the next
quota stop rather than the next daemon restart. A task already sitting on a hold
keeps it, is re-admitted once, and meets the new mode at the *next* stop. One
wasted spawn — the same trade task 003 decision 1 recorded for pause-then-resume
dropping a hold.

Also recorded: **the default protects nobody by itself.** `always` is the
default, so a fresh installation still auto-continues. What makes a runaway
impossible is the classifier being right; this is the operator control, and it
is not a substitute for that.
