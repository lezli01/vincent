# 036 — A step-authored status message, live and terminal

**Status:** ✅ done (7/7)
**Issue:** [#199](https://github.com/lezli01/vincent/issues/199)
**Spec:** amends §5.4, §7.2, §8.4 (a recorded non-change), §8.5, §9, §12.1,
§13.2, §13.3, §14, §15

## Problem

A step could not say anything about itself. A reader had two channels and
neither answered "what is this doing" or "why did that fail":

- `failure_reason` is a **closed enum** of daemon-authored constants
  (`timeout`, `nonzero_exit`, `check_failed`, `merge_conflict`, …). It says
  which *category* of thing went wrong, never which file, which test, which
  assertion. `nonzero_exit` on a forty-minute agent step is almost no
  information.
- `step_runs.result_summary` holds the agent's final result text or the last
  200 lines of command stdout. It was stored, served on the API DTO, and read
  by `.Steps.<id>.Result` and the repair prompt — and rendered **nowhere** in
  the TUI.

So a running task told you only that it was running, and a blocked task told
you only the category of its failure. Finding out more meant opening the
transcript, which is the durable output copy rather than a summary.

## What shipped

One nullable free-text field on `step_runs`, the **status message**: a short
line the running step writes about itself, in its own words. It has two
readings and is one field, not two features — while the step runs it is the
live answer to "what is this doing", and the last value written before the
attempt ends stays on the finished row as its self-report.

It does **not** replace or widen `failure_reason`. That enum stays a closed,
shared vocabulary between `internal/worktree` and `internal/taskrun/engine.go`
(T1.5/T1.6 decision), and the status message is never rendered as a cause of
failure.

## Decisions

**1. The producer is the API, not a sentinel line in the step's output.**
*2026-08-26.* A step sets its status by calling the daemon —
`POST /v1/tasks/{id}/steps/{step_id}/status`, wrapped in a new
`vincent status "<text>"` subcommand that resolves task and step from its
environment.

The sentinel design the issue proposed lost on three counts. It forces a
strip-or-keep choice over the transcript and `result_summary` that has no good
answer. It cannot see the obvious agent spelling, because an agent that runs
`echo '::vincent:status:: …'` through its Bash tool produces an
`agent.EventToolUse`/`EventToolResult`, not the `EventOutput` that
`Runner.publishOutput` would be parsing. And it makes every step's stdout a
control channel, so any program that happens to print the marker changes daemon
state. The API call has none of those properties, works identically for `agent`
and `command` steps, and reuses auth, transport and the error envelope that
already exist.

**2. Agent steps gain the §8.5 `VINCENT_*` environment.** *2026-08-26.* This is
decision 1's prerequisite and was a real gap, not a chore:
`internal/taskrun/steps.go` passed `Env: r.childEnv()` to `agent.RunSpec`, so an
agent step saw the resolved base environment and none of the run facts, while
`commandEnv` added them for command and check steps only. `vincent status` needs
`VINCENT_TASK_ID` and `VINCENT_STEP_ID` to address itself. Agent steps now get
the same block by the same precedence rule; `env:` stays a command-step field,
so an agent step has no third layer.

**3. Scope is `agent` and `command` steps.** *2026-08-26.* Of the issue's three
ways out this is option 1, deliberately narrowed. `manual`, `parallel`,
`fan_out`, `condition`, `loop` and `break` write `step_run` rows but run no
process, so they have no voice and their status stays empty; `include` is out by
construction (§7.9).

The alternatives were rejected for one reason each. Synthesising daemon text for
the process-less types (option 3) puts daemon-authored and step-authored strings
in one field, which is exactly the muddle this feature exists to escape from. A
workflow-authored `status:` template (option 2) reintroduces as a second
mechanism the design the issue already rejected as primary, and can only restate
what the author knew before the run. "Every step type carries a status" is
therefore **not** met, knowingly. The field means "what the step said about
itself"; a step with nothing to say says nothing.

**4. No automatic prompt injection.** *2026-08-26.* The daemon does not append a
protocol instruction to rendered agent prompts. §8.4's automatic append stays
reserved for the `<previous-attempt-failure>` block. The protocol is documented
instead — in `skills/vincent-workflows/SKILL.md`, `docs/guides/workflows.md` and
`docs/reference/cli.md` — and a workflow author who wants live status writes the
instruction into their own prompt. This costs nothing in tokens for the
workflows that do not care, at the price of a low hit rate until authors adopt
it. That price is what makes decision 8's `result_summary` rendering part of
this task rather than a follow-up.

**5. Durable, deduped, rate-capped.** *2026-08-26.* The status lands on §13.3's
durable side, as the issue requires: a client that blinks must be able to
recover it via `Last-Event-ID`. A new durable event type `task.status_changed`
carries `{task_id, step_id, message}`.

Two bounds keep it from flooding the `events` table. A write whose message is
byte-identical to the stored value is dropped without an event — the rule
`agent.quota_changed` already records. And a minimum interval per step run
(**1 s**) coalesces anything faster to the latest value rather than rejecting
it; the throttle is leading-edge, so the first write after a quiet period is
always immediate and the live reading is never delayed. The message itself is
capped at **256 bytes**, truncated rather than refused, forced to a single line,
with control characters stripped. `scheduler.WakeOn` is **false** for the new
type: nothing about admission changes when a step describes itself.

*Amended during implementation.* A coalesced write is persisted **by row id**,
not re-resolved as "the running step at this id". The first shape lost the last
thing a step said whenever the step finished inside the floor — which is
precisely the message the terminal reading exists for. The step wrote it while
it *was* running; the floor is the daemon's choice about when to persist it, not
a licence to drop it. `store.SetStepRunStatusByRun` is that write, and nothing
but the throttle may call it — the endpoint's own 409 lives in
`SetStepRunStatus`.

**6. Terminal reading is neutral.** *2026-08-26.* The last value survives on the
finished row — that is the half of the issue that answers "why did that fail" —
but no client ever labels it as a failure cause. A step killed on `timeout`
after forty minutes may be carrying a message it set thirty-five minutes
earlier, and rendering that beside `failure_reason` would present a stale
self-report as the daemon's verdict. It renders as the step's last status,
visually distinct from the `styleBad` failure reason on the attempt line. No
`status_updated_at` column and no clearing rule.

The mechanism that makes survival free is that `UpdateStepRun` does **not**
carry `status_message`. The actor is the sole writer of every other column of
its rows; this is the one column written from another goroutine, so an update
with the actor's stale struct would erase it. Leaving the column out of the SET
list makes recovery's `terminalizeOpenStepRuns` keep it too, with no extra rule.

**7. Human-facing only.** *2026-08-26.* The status reaches the TUI, the CLI and
the API and stops there. It is not added to `.Steps` (§8.4) and not added to the
`<previous-attempt-failure>` retry block (§7.2). Two reasons: the blast radius
stays on display surfaces, and free text an agent chose at run time does not
become something an `if:` guard can branch on. This also sidesteps a name
collision — `.Steps.<id>.Status` already means the run *state*, so exposing the
message there would need a second, confusable key.

**8. Two adjacent gaps close in the same pass.** *2026-08-26.* §5.4's normative
field list gains both the new field *and* `result_summary`, which it had never
listed. And `result_summary` is finally rendered in the TUI detail view: it is
stored, served, read by `.Steps.<id>.Result` and by the repair prompt, and was
shown nowhere. Given decision 4, most steps will emit no status for a while, and
the stored summary is what improves the blocked case for all of them meanwhile.
It renders as a dim continuation line under an attempt that did **not** succeed
— under every attempt it would roughly double a healthy timeline's height to
restate what the output pane already shows for the one attempt the reader
selected.

**9. The board column is shed first, and has a gate of its own.**
*2026-08-26.* `renderBoardState` records that a hold's *reason* is deliberately
kept out of the state cell: "it does not fit `widthState`, and widening the
column for a rare state would cost every board the columns that get shed first.
The detail header, which has the width, names it." That decision is **not**
overturned. The status gets its own column, shed *before* `cost`, `stepName`,
`workflow` and `project` under width pressure.

The shedding ladder alone turned out to be the wrong gate. `minTitle` is 16, so
a 120-column board — the common case — would have admitted the status and taken
its title from 50 cells to 20: legal, and useless. `minTitleWithStatus` (64) is
the column's own bar. It is also set high enough that the column cannot eat the
width a *grouped* board frees by dropping PROJECT and WORKFLOW, which
`columnsFor` records as going to the title; at every width a grouped board's
title stays strictly wider than a flat board's, and a test asserts it.

**10. The list row denormalizes the *newest* row's status.** *2026-08-26.* The
board reads `GET /v1/tasks` and never fetches step rows, so `status_message`
rides the list DTO the way `step_name` and `cost_usd` do — one extra query for
the whole page, never one per row. It is the status of the newest `step_runs`
row, not a search for the newest non-empty message: a step that spoke and
finished must not have its line linger beside the next step, which is silently
doing something else.

## Tasks

- [x] **036.1** Store: migration `0014_step_status.sql`, `StatusMessage` on
  `store.StepRun` threaded through the typed CRUD (and deliberately *not*
  through `UpdateStepRun`), `SetStepRunStatus` /`SetStepRunStatusByRun` with the
  dedup rule and the `task.status_changed` event in one transaction,
  `RunningStepRunID`, `LatestStepStatuses`. ✓ 2026-08-26
- [x] **036.2** Engine: `Runner.SetStepStatus`, `NormalizeStatusMessage`, the
  per-run coalescing throttle, and the §8.5 environment on an agent step's
  `RunSpec.Env`. ✓ 2026-08-26
- [x] **036.3** API + apiclient: the route, `status_message` on the step-run DTO
  and the list row, `Client.SetStepStatus`, and the new event type in the client
  vocabulary. ✓ 2026-08-26
- [x] **036.4** CLI: `vincent status`, and a `STATUS` column on
  `vincent task show`. ✓ 2026-08-26
- [x] **036.5** TUI: the status on the attempt line in its own style, the
  `result_summary` continuation line, and the sheddable board column. ✓
  2026-08-26
- [x] **036.6** Tests: store, engine, recovery, API/apiclient live, CLI e2e and
  TUI, plus `cmd/fakeagent`'s `set-status` scenario. ✓ 2026-08-26
- [x] **036.7** Docs: spec amendments, `docs/reference/api.md`,
  `docs/reference/cli.md`, `docs/reference/task-lifecycle.md`,
  `docs/reference/workflow-schema.md`, `docs/guides/workflows.md`,
  `docs/guides/tui.md`, `docs/features.md`,
  `skills/vincent-workflows/SKILL.md`, and `m2-gate.sh` scenario 12. ✓
  2026-08-26

## Verification

- `go test ./...` and `go test -race ./internal/taskrun ./internal/store` green.
- `go run mage.go lint` clean, and `golangci-lint` cross-built for `windows`,
  `darwin` and `linux` clean.
- `./scripts/m2-gate.sh` green, all twelve scenarios. Scenario 12 asserts over
  curl that a running step's status is visible on the step row and on
  `GET /v1/tasks`, and that the last value the step set survives on the finished
  row.
- `docs/assets/tui-*.png` were **not** re-captured. The board's shape is
  unchanged at the widths `scripts/screenshots.sh` renders — the status column
  is not admitted below a 64-cell title — and the detail panel's new fields need
  a task whose step actually set a status, which the seeding script does not
  produce. Re-capturing would have produced byte-identical board shots and a
  detail shot no more informative than the current one.
