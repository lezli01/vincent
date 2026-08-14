# 003 — Classify agent usage limits and auth expiry

**Status:** ✅ done (7/7) · **Opened:** 2026-08-14 · Closes
[#80](https://github.com/lezli01/vincent/issues/80)

Two new reasons in the shared failure vocabulary — `usage_limit` and
`agent_unauthenticated` — plus the machinery that makes `usage_limit` mean
something the daemon can act on rather than a label on a `blocked` task.

## The problem

`classifyAgent` collapsed every adapter outcome it did not already special-case
into `nonzero_exit` (exit ≠ 0) or `agent_error` (`IsError`). A quota-exhausted
run and a genuinely failed run were the same row, the same board cell, the same
block reason.

That is worse than one bad error message. `runStepWithRetries` has no delay
between attempts, so a quota-walled step spent its whole `max_retries` budget in
seconds; and since the scheduler admits up to `max_parallel_tasks` concurrently,
every running task on that adapter hit the same wall in the same minute. The
board went `blocked` with a reason that sent the reader to a transcript about a
task that was fine.

The two halves have different spec status, and it matters:

- **Quota was an unconsidered case.** §18's edge-case table is otherwise unusually
  thorough — dirty worktrees, manually deleted directories, DST, gigabyte output,
  ref-hierarchy conflicts — and "the agent ran out of quota" was not among them.
  Nothing in the spec, `docs/history/v0-tasks.md` or `docs/tasks/` recorded a
  decision about usage limits, retry backoff, or auto-recovery from a rate limit.
- **Auth expiry at run time was a *stated position*.** `docs/spec.md`'s §18 auth
  row already said that where an adapter cannot tell whether the CLI is
  authenticated (`logged_in: null`), "the step runs and fails with the CLI's auth
  error". Decision 4 below leaves that substantively intact and amends only the
  name of the reason.

## Decisions

### 1. The admission hold is a generic pair of columns on `tasks`

*2026-08-14.* Migration `0006` adds `admit_not_before TEXT` and
`queued_reason TEXT`. The scheduler reads one timestamp and clients render one
reason string, so the next wait-shaped case — the agent-wide hold this task
leaves out of scope, a git backoff — costs no second migration and no second
branch in the admission walk.

**Beat:** overloading `block_reason`. §14 says it is "set while `state='blocked'`",
and clients key off the API's `block_reason` field to mean exactly that; a queued
task carrying one would break them. Also beat: a `usage_limit_until` column, which
would have to be renamed or duplicated the first time anything else waits.

**Clearing is not the caller's job.** `TransitionTask` NULLs both columns on any
transition whose *from*-state is `queued` — the same construction that makes
"`pending_input_json` non-null iff `awaiting_input`" hold. So a hold cannot
outlive the queued period it belongs to: admission, parking and cancel all clear
it, and no caller has to remember.

One consequence, recorded rather than fixed: pausing a held task and then
resuming it drops the hold, so the task is admitted at once and re-discovers the
wall. That is one wasted spawn in exchange for "a human action means go", which
is the rule §6 already applies to every other pending flag.

### 2. The adapter's verdict travels on `agent.RunResult`

*2026-08-14.* `RunResult` gains `Failure *agent.Failure{Kind FailureKind;
RetryAfter *time.Time}`, populated inside each adapter's `Wait()`.

**Beat:** a new `Adapter` method, `ClassifyFailure(res, stderr)`. The engine
could not feed that signature today — it never sees stderr, only the handle
does — so the material would have to be plumbed out of the adapter purely to be
handed back in. `Wait()` is where the terminal result and the 64 KB stderr tail
already are.

`FailureKind` is an adapter-side enum (`usage_limit`, `unauthenticated`), **not**
a reason string. `internal/agent` must own no entry in the `block_reason`
vocabulary — that vocabulary is `internal/taskrun` + `internal/worktree`
(T1.5/T1.6 decision) — and the engine does the kind → reason mapping, so there is
no third source of truth for a reason value.

**Claude ships patterns; codex and cursor recognize nothing.** The wordings are
not fixture-verified: capturing a genuine quota exhaustion means burning a real
five-hour window, the same impracticality `internal/agent/cursor/cursor.go`
records for the logged-out wording. The precedent set there applies — a layered,
conservative parse that falls through to today's reason when unsure. Extending it
to codex and cursor without a fixture would be a guess, and a wrong guess parks a
genuinely failed task in a wait it never recovers from. `cmd/fakeagent` carries
the scenarios in all three dialects anyway, and the codex/cursor legs assert that
today's behaviour is unchanged — so adding patterns later is a `classify` call
beside an existing test.

**A reset timestamp is parsed or nil, never guessed.** Anything unparseable, in
the past, or beyond a seven-day horizon leaves `RetryAfter` nil and the interval
takes over. A past timestamp would admit on the next tick — the exact respawn
loop this task exists to stop.

### 3. Re-check interval is a config knob, default 15 minutes

*2026-08-14.* `usage_limit_recheck_interval`, used only when the CLI reported no
reset timestamp. 15 m bounds a five-hour window at roughly twenty wasted spawns;
a user who knows their plan can tighten or widen it. Validated positive: zero
would re-admit on the very next tick.

**Beat:** exponential backoff. That is per-task state the row would have to carry
and a second retry-ish concept beside §7.2's, for a case where the wait length is
knowable from the plan.

**Beat:** a hardcoded constant. The gate needs a two-second interval to drive the
whole path end to end, and a test-only override hatch is what the PR V decision
rejected in favour of a real config field.

### 4. The auth half classifies, and changes nothing else

*2026-08-14.* §18's auth row stays true: the step still runs, the attempt still
fails, the normal §7.2 budget still applies, and the task still ends up `blocked`.
The row is amended only to name the reason `agent_unauthenticated` instead of
"the CLI's auth error".

**Beat:** a pre-flight refusal on `logged_in: false`, and short-circuiting the
retry budget. Either would make this the first reason in vincent to bypass §7.2,
and what it saves is one extra process spawn at the default `max_retries: 1`.

### 5. The hold is evaluated in the walk, not in `ListAdmissible`'s SQL

*2026-08-14.* The issue put it in SQL. The walk parks a task whose pause was
requested while it ran, and that check has to keep running for held tasks — if
SQL hid them, a `pause` on a quota-held task would silently not take effect until
the hold expired, which is exactly the "showing queued while the human asked for
paused" lie the existing comment calls out. Order in the walk: pause, then the
hold, then the caps.

No timer is needed: `tickInterval` is already 5 s, so a hold is picked up within
5 s of expiring. That gives the tick's doc comment a second exception worth
noting — nothing commits a state change when a hold lapses, so the tick is the
only thing that notices.

## Tasks

- [x] **003.1** — `agent.Failure`/`FailureKind` on `RunResult`; claude's layered
  parse, with codex and cursor documented as classifying nothing. ✓ 2026-08-14
- [x] **003.2** — Migration `0006`, `store.Task`/`TaskChange` fields, and the
  "any transition out of `queued` clears the hold" invariant. ✓ 2026-08-14
- [x] **003.3** — Engine: `ReasonUsageLimit`/`ReasonAgentUnauthenticated`, the
  `classifyAgent` branch, and `holdForUsageLimit`. ✓ 2026-08-14
- [x] **003.4** — Scheduler: the hold in the walk, injected clock, pause-first
  ordering. ✓ 2026-08-14
- [x] **003.5** — `usage_limit_recheck_interval` in config, `GET /v1/config`, the
  generated config file and the daemon view. ✓ 2026-08-14
- [x] **003.6** — `admit_not_before`/`queued_reason` through the API and client;
  the board cell and detail header. ✓ 2026-08-14
- [x] **003.7** — `cmd/fakeagent` scenarios in all three dialects, per-package
  tests, and `scripts/m2-gate.sh` scenario 5 driving the whole path over curl.
  ✓ 2026-08-14

## Out of scope

**The agent-wide hold** — one task's `usage_limit` suppressing admission of every
*other* queued task on that adapter — stays out, as the issue asks. Each task
discovers the wall independently, costing N process spawns per window. It wants
its own issue, and the generic `admit_not_before` column chosen above is what it
will build on.

**Crash recovery needs no change.** The hold is a column on the task row, so a
crash during a wait leaves a queued task with its hold intact; the startup sweep
only finalizes `running` step runs.

## Verification

- `go test ./...` and `go test -race ./...` green.
- `go tool golangci-lint run ./...` green for `GOOS=linux`, `darwin` and
  `windows` (the CLAUDE.md cross-platform lint).
- `VINCENT_GATE_SCENARIO=5 ./scripts/m2-gate.sh` — held task frees its slot, the
  second task runs, the first recovers unattended, all over curl.
