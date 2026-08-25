# 028 — `retry_backoff`: pacing step retries through task 003's admission hold

**Status:** ✅ done (5/5)
**Opened:** 2026-08-25

A step's retries were immediate, and no configuration could make them wait.
`runStepWithRetries` counted the failure, checked the budget, checked for
cancellation, emitted `step.retrying`, and called `runAttempt` again on the next
iteration.

That is correct for exactly one class of failure — the deterministic kind, where
§8.4's failure block gives an agent a second shot at the same compile error. It
is wrong for every transient one: a network failure during cursor's
authenticated model probe, a `git index.lock` held by another process, a flaky
check command. For those the default `max_retries: 1` means two guaranteed
failures inside a few seconds and a blocked task, with the budget the user would
have wanted spent thirty seconds later already gone.

The wait primitive was already built. Task 003's migration `0006` added
`admit_not_before` and `queued_reason` to `tasks` as a deliberately *generic*
pair — 003 decision 1 names "a git backoff" as the intended next consumer — and
`holdForUsageLimit` is the working example. This task connects the retry loop to
it, and does nothing else: no migration, no new column, no store change, and
nothing new on the task wire — `admit_not_before` and `queued_reason` already
ship and already render generically, so no client had to learn the new reason.
The only client-visible addition is the field itself: `retry_backoff` joins the
workflow-definition DTO (`internal/api/workflowdef.go`,
`internal/apiclient/workflowdef.go`) and the workflow graph's step detail, the
way every other step field does.

## Decisions

### 1. The backoff applies to every failure that would be retried (2026-08-25)

No per-reason policy. [#88](https://github.com/lezli01/vincent/issues/88)
proposed exempting `nonzero_exit` on the grounds that "the agent has feedback to
act on". Rejected: an agent CLI that hits a transient upstream error and one
that leaves a compile error both exit non-zero and both classify `nonzero_exit`,
so the exemption would remove the knob's reach from precisely the failure the
issue opens with.

The field is opt-in and per-step already — a step that wants an immediate second
shot at its own output writes `retry_backoff: 0`, which beats a workflow-wide
default. `usage_limit` and `interrupted` are untouched because §7.2 already says
they are not failures; `agent_unauthenticated` is untouched because §7.2's
2026-08-14 amendment puts it under the normal budget and that position is
unchanged here; `condition_error` is untouched because a guard never becomes an
attempt.

**Beat:** a table of transient-vs-deterministic reasons. It would have to be
right about every adapter's wording forever, and it moves a policy decision out
of the workflow — the place that knows whether *this* step is flaky — and into
vincent.

### 2. The delay is fixed, not linear or exponential (2026-08-25)

`retry_backoff: 30s` means 30 s before every retry of that step.

This does not reopen the recorded no-backoff decision (§12.3,
`internal/config/config.go`, 003 decision 3). That decision beat *exponential*
backoff for `usage_limit_recheck_interval`, on the grounds that growth is
per-task state the row would have to carry and a second retry-ish concept beside
§7.2's. A fixed delay computed from resolved configuration at the moment of the
wait carries no per-task state and adds no column, and it is §7.2's own concept
rather than one beside it. A growth curve would also make the field name a lie.

### 3. Two resolution levels — step, then `workflow.defaults` (2026-08-25)

No `config.yaml` key, mirroring `max_retries`, which has no config-level default
either. Retry policy is a workflow's business, and `config.Defaults` is recorded
as timeouts only (PR V decision, `docs/history/v0-tasks.md`). The issue asked for
a config key; dropping it keeps the PR V decision intact rather than amending
it, and costs nothing a gate or a user needs, since workflow YAML reaches both
levels. `resolveRetryBackoff` therefore takes `(step, defaults)` and returns
zero when neither sets it — one line shorter than `resolveMaxRetries`, and no
`config.Config` argument.

Zero is the default and is legal to state explicitly; only a negative value is
refused, which is `max_retries`' rule.

### 4. The requeue reuses `taskstate.Interrupt` (2026-08-25)

`Interrupt` is the only `Running → Queued` edge, and adding a second action for
the identical edge would buy a truthful name at the cost of a new arm in every
exhaustive action list.

Its doc comment said "the attempt does not consume a retry", which is wrong for
this caller, and is amended to say that the *attempt row's state* decides that,
not the action: `finishStepRun` writes the row and `transition` moves the task,
and the two are independent. The pairing this task needs — a `failed` row and a
`running → queued` transition — is therefore permitted and is what ships. This
was the one thing [#88](https://github.com/lezli01/vincent/issues/88) flagged
for confirmation before implementing.

`holdForRetry` is `holdForUsageLimit`'s sibling and deliberately does **not**
call `recordUsageLimit`: a quota window closing is a per-adapter observation
(task 026), and a flaky step has nothing to say about any adapter's quota.

### 5. The backoff reaches every call site, not just top-level steps (2026-08-25)

`parallel` sub-steps and `loop` body steps are where a flaky check most often
lives, so a top-level-only version would silently do nothing where it is most
wanted. A group still waits for its in-flight siblings (`wg.Wait()`) before the
task requeues — the same shape a `usage_limit` inside a group already has, since
`collectGroup` runs after the wait. The fan-out join inherits it for free,
because the join is an ordinary attempt of an ordinary step.

Two exceptions, both because the attempt is not the task's to hold:

- `repair` (task 025) runs with `max_retries: 0`, so it never reaches the retry
  branch at all.
- The `on_conflict: agent` merge resolver pins `retry_backoff: 0`. Its attempts
  are the join's own, and a resolver that does not resolve leaves the conflict
  for a human — there is no failure there the engine could hold and re-admit.
  Pinned rather than left alone, because `defaults.retry_backoff` would
  otherwise reach it and silently spend half its budget on a wait nothing would
  honour.

### 6. Backoff is checked before `allow_failure`, everywhere (2026-08-25)

The trap this feature had to avoid. The engine's `StepFailed` arm checks
`allowFailure` first and advances the cursor, which is safe only while
`runStepWithRetries` returns `failed` exclusively when the budget is spent. With
a backoff it can return `failed` with budget **remaining**, and an
`allow_failure` step would then advance on its first failure instead of ever
retrying.

All three places that swallow a failure into a success test `backoffUntil`
first: the engine's step loop, `group.go`'s sub-step goroutine, and `loop.go`'s
`runBodyStep`.

`collectGroup` gets a matching third precedence tier — interrupted, then a
failure with its budget spent, then a backoff-pending failure. A spent sibling
must win: waiting on one whose budget is *already* gone only delays the block by
one hold, with nothing learned.

### 7. A backed-off wait is a minimum, not a schedule (2026-08-25)

The scheduler's safety-net tick is 5 s and re-admission competes for slots under
the §11 caps, so the observed wait is `retry_backoff` plus up to a tick, plus
queueing. Documented in §7.2, not engineered around: a timer per hold is what
003 already decided the tick exists to avoid.

## Tasks

- [x] **028.1** `internal/workflow`: `RetryBackoff` on `Step` and `Defaults`,
  non-negative validation on both, and the `rejectFields` set — refused on
  `condition`, `break`, `loop` and `include` exactly as `max_retries` is.
  `include.go` adds it to the callee-inherits-from list (§7.9). ✓ 2026-08-25
- [x] **028.2** `internal/taskrun`: `resolveRetryBackoff`,
  `ReasonRetryBackoff`, `stepOutcome.backoffUntil`, the requeue branch in
  `runStepWithRetries`, and `holdForRetry`. ✓ 2026-08-25
- [x] **028.3** The backoff check ahead of `allow_failure` in the step loop, in
  `group.go` and in `loop.go`; `collectGroup`'s third tier; the merge
  resolver's pin. `internal/taskstate`: the `Interrupt` comment amendment, and
  nothing else. ✓ 2026-08-25
- [x] **028.4** Tests: resolution per level; validation on a step, in
  `defaults` and on the four step types that refuse it; include inheritance;
  the requeue, the released slot, the budget preserved across it, a spent
  budget still blocking, `allow_failure` in all three places, the loop resuming
  mid-loop, `collectGroup`'s tier order, `usage_limit` unaffected, and zero
  behaving as today. ✓ 2026-08-25
- [x] **028.5** `scripts/m2-gate.sh` scenario 11 over curl; §7.2, §8.1, §8.2,
  §11, §12.3 and §18 amended, dated; `features.md`, `workflow-schema.md`,
  `task-lifecycle.md`, `troubleshooting.md`, `api.md`, `tui.md`,
  `guides/workflows.md` (§3.2's `defaults` table, §4's sub-step fields and
  §8.2) and the workflow-authoring skill's schema reference updated.
  ✓ 2026-08-25

## Noted, deliberately not folded in

`parallel` and `fan_out` steps accept `retry_backoff` and `max_retries` alike,
and a `parallel` group ignores both — it owns no attempt of its own. That
asymmetry predates this task (§8.2 rejects them on `loop` but not on
`parallel`), and making the two structure types agree is a schema change with
its own compatibility question. It wants its own issue.
