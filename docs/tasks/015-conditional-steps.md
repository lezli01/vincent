# 015 — Conditions between steps (`if:`, `type: condition`, `allow_failure:`)

**Status:** ✅ done (10/10) · **Opened:** 2026-08-18

A workflow may now decide, at run time, what to do next:

```yaml
steps:
  - id: probe
    type: command
    run: git diff --quiet HEAD~1
    allow_failure: true              # a nonzero exit is data, not a block

  - id: nothing-to-do
    type: condition                  # false ends the run; the task is `done`
    if: '{{ ne (index .Steps "probe").ExitCode 0 }}'

  - id: changelog
    type: agent
    if: '{{ eq (index .Task.Fields "changelog") "yes" }}'   # skip, then carry on
    prompt: Update CHANGELOG.md.

  - id: build
    type: fan_out
    lanes:
      - { id: api,     workflow: implement-module, fields: { module: api } }
      - { id: windows, workflow: implement-module, if: '{{ eq .Host.OS "windows" }}' }
```

Three fields, three jobs, deliberately not one:

- **`if:` on a step** — skip this step and carry on. Records a `skipped` row
  with `skip_reason: condition`, which stays visible in `.Steps`.
- **`if:` on a fan-out lane or a `parallel` sub-step** — do not start this
  member. The other members still run and the join still happens.
- **`type: condition`** — a step whose entire body is the guard. False ends
  the sequence: no later step runs and the task is `done`.
- **`allow_failure:`** on `agent` and `command` steps — the failures the step
  itself produced advance instead of blocking, which is what gives a guard
  something to read.

## The problem

Every workflow in vincent is a straight line. §7 opens with "steps execute
strictly in order", and the engine is literally
`for index := task.CurrentStep; index < len(wf.Steps); index++`. A workflow
therefore does the same amount of work whatever it finds: the review step runs
on a one-line change, the fan-out spawns all six lanes when two of them have
nothing to build, and a run that discovers at step 2 that there is nothing to
do still walks through steps 3 to 6 to reach `done`.

The workaround is to split one workflow into several and make the *human*
choose at creation, which moves the decision to the one point in the lifecycle
where the facts that decide it do not exist yet.

§8.1.1 already recorded this gap from the other side. A per-step `platforms:`
was deferred because "it needs an answer to *what does a skipped step do to
`.Steps` and to the task's success*, which is a lifecycle question, not a
schema one." That is this task's central question, and answering it settles
both (decision 8).

§20 keeps "branching and conditionals" in future work, promoted out of it by
this task the way cursor and fan-out were.

## Decisions

### 1. A guard is a field, not a control-flow step type

*2026-08-18.* `if:` is a field alongside `max_retries` and `timeout`, on every
step and on every lane. There is no `branch`/`switch` type with nested
`then:`/`else:` bodies.

**Beat:** control-flow step types. They are more expressive — mutually
exclusive arms, grouped bodies — and they cost the two invariants the engine is
built on: "steps execute strictly in order" (§7) and a single integer
`current_step` cursor. `parallel` already bends the first and paid for it with
a rule (sub-steps share the group's `step_index`, §7.5); a nested tree would
bend both. Every use case that opened this task — early finish, lane
subsetting — is expressible as guards. A `branch` type is a later question with
a known trigger: the first workflow that cannot be written flat.

### 2. Stopping is a step type, not a second guard field

*2026-08-18.* Skip-and-continue and stop-the-run are different behaviours, and
they are spelled differently: `if:` is the guard, `type: condition` is the
terminator.

**Beat:** one field with prefix semantics — `continue_if:`, where a false guard
on any step ends the run. It is fewer concepts, and it makes "finish early"
fall out with no new type. It was rejected on how it reads: every CI system in
use — GitHub Actions, GitLab, Woodpecker — spells skip-and-continue `if:`, so a
guard field with stop semantics is a false friend whose failure mode is a task
reaching **`done`** having silently done half its work. Naming it `continue_if:`
to dodge that was the tell: the design was being bent around a name.

Putting the consequence in the *type* fixes it. `if:` keeps the meaning it has
everywhere else, and "the workflow ends here" is a line a reader can point at
rather than a modifier they have to notice.

**Beat also:** both guard fields at once (`if:` and `skip_if:`). Two fields
with near-identical syntax and opposite control flow read fine in a spec and
confuse every author afterward. If a workflow turns up that genuinely needs
skip-and-continue *and* stop in one guard position, it earns the second field
then.

### 3. `if:` means skip on a sequence and subset on a set

*2026-08-18.* One word, one meaning — "this member does not run" — whose
consequence follows from what it is attached to. On a step list (a sequence)
that is skip-and-continue. On a lane list or a `parallel` group's sub-step list
(both sets, with no ordering and no "later") it is subsetting: the other
members run, and the group or join proceeds without the absent one.

This is why `condition` steps are rejected inside a `parallel` group
(decision 7): a group has no sequence to end.

### 4. Guards are Go templates, strictly boolean

*2026-08-18.* An `if:` is rendered with the §8.4 `RenderContext` — the same
context, the same `missingkey=error`, the same parse-at-load validation as
`prompt`, `run` and `check`. The rendered string, trimmed, must be exactly
`true` or `false`; anything else fails the step with `condition_error`.

**Beat:** an expression language (`expr-lang/expr`, CEL). `steps.probe.exit_code
== 0` reads far better than `{{ ne (index .Steps "probe").ExitCode 0 }}`, and it
is typed and statically analysable. It is also a second language inside one YAML
file, a new dependency, and a second context shape to keep in sync with §8.4
forever. One language in the file wins; the verbosity is the price.

**Beat:** loose truthiness (`"yes"`, `"1"`, non-empty). Strictness is this
schema's posture everywhere else — strict YAML decoding, `missingkey=error`,
`macos` rejected as a typo of `darwin` (§8.1.1). And loose truthiness has a
specific trap here: `{{ .Missing }}` renders `false` under some spellings and
the string `<no value>` under others, both of which a permissive rule would
happily accept as a decision.

### 5. `allow_failure:` ships with the guards, not after them

*2026-08-18.* Without it, a guard can read almost nothing. §7.1 blocks the task
on a nonzero command exit, so a step that *succeeded* always has
`ExitCode: 0`, and `.Steps` never contained a failed step at all
(`engine.go:451`). Guards would have been able to branch on task fields typed by
a human at creation, and on nothing a run discovered — which is the half of the
feature worth having.

`allow_failure: true` converts only **the outcomes the step itself produced**:
`nonzero_exit`, `check_failed`, `agent_error`, `timeout`, `transcript_limit`.
Everything else is vincent failing to *run* the step —
`agent_unavailable`, `agent_unauthenticated`, `restricted_unsupported`,
`input_unsupported`, `platform_unsupported`, `invalid_snapshot`,
`template_error`, `condition_error` — and swallowing those would let a workflow
branch on "the CLI is not installed" as though it were a test result.
`usage_limit` and `interrupted` are untouched because §7.2 already says they are
not failures.

It is **orthogonal to the retry budget**: the step retries as it always did, and
`allow_failure` decides only what happens when the budget is spent. An author
writing a probe sets `max_retries: 0`; folding that in would make one field mean
two things.

There is no `defaults: allow_failure:`. A workflow-wide "failures do not block"
turns §7.2 off by accident, which is not a thing an author should be able to do
in one line at the top of a file.

### 6. `.Steps` gains failed steps the engine advanced past

*2026-08-18.* The §8.4 filter widens from
`succeeded | approved | skipped` to also admit `failed` — but only for a step
whose index is **behind** the current one.

A step's own failed attempt stays out of `.Steps["itself"]` mid-retry, because
`.LastFailure` is already the documented channel for that and two spellings of
one fact is how a template context rots. `interrupted` stays out: §7.2 says it
is not an outcome, and a guard branching on "the daemon restarted" is a bug
waiting to be written. `rejected` stays out by construction — a rejected gate
does not advance.

### 7. A `condition` step evaluates a template and nothing else

*2026-08-18.* It carries `id`, `name` and a required `if:`. It rejects every
other field, including `timeout`, `max_retries` and `allow_failure`, because it
starts no process: it cannot time out, cannot be interrupted, has nothing to
retry, and produces no transcript.

**Beat:** letting it carry `run:`, exit 0 meaning continue. The natural phrasing
of early finish really is a shell test — `git diff --quiet`. But that composes
exactly, in two lines, out of pieces that already exist (a `command` step with
`allow_failure`, then a guard reading its `ExitCode`), and the composition keeps
the command's output in a transcript where a human can read it. A
condition step that spawned processes would need `timeout`, `shell`, `env`, a
retry budget and a transcript — a `command` step wearing a hat.

`if:` on a `condition` step is its condition, and may not also act as a
skip-guard: "skip the check that decides whether to continue" has two readings
and neither is worth having.

**Where it is legal:** top-level steps, and the steps of a lane's own workflow.
Rejected inside a `parallel` group, joining `manual` and `on_input: require` on
§7.5's rejection list and for the same reason — a group is a set, so there is no
"stop the rest" to mean.

### 8. A false condition stops as `done`, with no policy field

*2026-08-18.* There is no `on_false: finish | block | fail`. Stopping always
finishes the task successfully.

**Beat:** the policy field. "Stop and block for a human" is worth having and
already exists — it is a `command` step that exits nonzero (§7.1, §7.2). The
gap in the vocabulary is *stop and succeed*, and that is the whole of what this
type fills. A policy field would give vincent two spellings of one behaviour,
which is what §7.6's `merge` shape was careful to avoid.

The cursor advances to `len(steps)` so `r.complete()` fires on the existing
path and a finished task does not read as mid-run to every client. The deciding
step gets one row; the steps after it get none, because they were never
considered and inventing rows for them would make
`GET /v1/tasks/{id}/steps` claim a decision the engine never made.

### 9. `stopped` is a new step-run state; `skip_reason` is a new column

*2026-08-18.* Two different events, recorded two different ways.

A `condition` step that passes records `succeeded`. One that fails records
**`stopped`** — a new `StepRunState`. No existing value fits: `failed`
contradicts a `done` task, `skipped` is false because the step did evaluate, and
`succeeded` hides the single most important fact about the run. The churn is
real (`store/models.go`, the steps DTO, the TUI detail renderer) and is the
price of the detail view being able to say where a run ended and why.

An ordinary step skipped by its guard records `skipped` — the existing state,
already shared with the human `skip` action — plus a new
`step_runs.skip_reason` column carrying `condition`. Overloading
`failure_reason` on a row that did not fail was rejected: this schema has been
careful that one column means one thing (`usage_limit` is a `queued_reason` and
never a `block_reason`, on exactly this principle).

### 10. Guards are re-evaluated every time, never sticky

*2026-08-18.* A guard is part of rendering: it is evaluated fresh on every
attempt, on a human `retry`, and after crash recovery re-runs an `interrupted`
step. No verdict is persisted and consulted later.

The surprising case is real — a human retries a blocked step and the guard,
now false, skips it — and it is also *correct*: if the guard is false now,
running the step now would be wrong. The alternative is a persisted decision
cache that §12.4 recovery would then have to reason about, to preserve a verdict
computed against facts that have since changed. The mitigation is visibility
(the skipped row records why), not stickiness.

### 11. Lane guards are evaluated at run time, and the creation-time limits count conservatively

*2026-08-18.* A lane's `if:` is evaluated when the `fan_out` step runs, in the
parent's context, so a lane can depend on what an earlier step found — which is
the use case ("fan out fixups only for the modules that failed").

That breaks the premise §7.6 leans on: the tree's shape is no longer static once
lanes are conditional. So `fan_out.max_depth` and `fan_out.max_tasks` keep
counting **every** lane, guarded ones included. A tree that could never spawn 64
tasks may still be refused at creation. That is an over-approximation and it is
stated in §7.6 rather than left to be discovered, because the alternative —
evaluating lane guards at creation against `.Task.Fields` only — forecloses the
interesting half of the feature to make a limit exact that nobody is near.

**All lanes guarded off** is a no-op success, not an error. The step is reached,
chooses nothing, and advances; a workflow whose conditions all said "not this
time" decided correctly.

**A lane that stops early merges normally.** §7.6's `lane_failed` is about a lane
that settles `blocked` or `aborted`; a child that hit a false `condition` at
step 2 of 5 settles `done`, and `done` is `done`. §7.6's phrase "settles without
finishing" needed one clarifying sentence, since an early stop *is* finishing.

### 12. `.Host` joins the template context; `.Now` does not

*2026-08-18.* `.Host{OS, Arch}` — the daemon's own GOOS/GOARCH, since the daemon
is what runs the steps (§8.1.1).

It costs one struct and it closes §8.1.1's deferral without new schema: the
per-step `platforms:` deferred there is now `if: '{{ ne .Host.OS "windows" }}'`,
using a guard whose skip semantics this task defines. The whole-workflow
`platforms:` stays as it is — it gates *offering* a workflow, which a run-time
guard cannot do.

`.Now` was rejected. A guard reading wall-clock makes a run non-reproducible,
which is the property §7.6 worked to preserve when it chose declared lane order
over completion order, and nothing this task exists for needs it.

### 13. No new event type

*2026-08-18.* §13.3 records that `step.started`, `step.finished` and
`step.retrying` "were listed here through M2 but were never emitted" — a step's
lifecycle is reconstructed from `GET /v1/tasks/{id}/steps`, and `task.step_advanced`
already fires when the cursor moves. A skip moves the cursor and an early stop
moves it to the end, so both are already visible on the stream. Adding
`step.skipped` would be the first step-lifecycle event vincent ever emitted, to
carry something a client can already see.

### 14. A guard error blocks without consuming the retry budget

*2026-08-18.* `condition_error` is the one failure in the §18 vocabulary that
does not run §7.2's retry budget. It blocks on the first evaluation.

This **reverses the answer this task was designed with**, which was to run the
ordinary budget for uniformity, on the `agent_unauthenticated` precedent
(§7.2: short-circuiting "would make it the only reason in vincent that bypasses
this section — to save one process spawn"). Implementation showed the two cases
are not the same shape. `agent_unauthenticated` is an *attempt* that failed:
there is a row, a transcript and an attempt number, and the budget is machinery
that already exists around it. A guard is evaluated **before** the step becomes
an attempt — it decides whether there is one — so retrying it would mean
inventing attempt rows for a step that never ran, purely to have something to
count. Decision 8 refuses exactly that ("inventing rows for them would make
`GET /v1/tasks/{id}/steps` claim a decision the engine never made").

And the retries could not work. Re-rendering an unchanged template against an
unchanged context cannot answer differently, and unlike a spawn it cannot fail
for an environmental reason that clears. The second try that can actually
succeed is the human's, after they fix the workflow — which decision 10 already
guarantees re-asks the question.

One `failed` row is written for the guard, carrying `condition_error`, so the
block is diagnosable in the detail view rather than being a state change with
no step behind it.

## Tasks

- [x] **015.1 — Schema and validation.** ✓ 2026-08-18 `If` and `AllowFailure` on `Step`, `If`
  on `Lane`, `StepCondition` in the type set. Guard templates parse-checked at
  load like every other template field. `rejectFields` extended so a `condition`
  step refuses everything but `id`/`name`/`if`, and so `allow_failure` is
  rejected on `manual`, `parallel`, `fan_out` and `condition`. A trailing
  `condition` step is a **warning**, not an error (it can only mean "or don't"),
  riding the existing `warnings[]`. Spec §8.2.
- [x] **015.2 — Evaluation.** ✓ 2026-08-18 `.Host` in `RenderContext`; an `Evaluate` that
  renders a guard and demands exactly `true`/`false`; `ReasonConditionError`
  under §7.2's ordinary retry budget (uniformity, per the `agent_unauthenticated`
  precedent); `.Steps` widened per decision 6. Spec §8.4, §18.
- [x] **015.3 — Store.** ✓ 2026-08-18 Migration `0008`: `step_runs.skip_reason`. `StepStopped`
  in `StepRunState`. Spec §14.
- [x] **015.4 — Engine.** ✓ 2026-08-18 Depends: 015.1, 015.2, 015.3. Guard evaluation before a
  step runs; the skipped row; the `condition` executor and its `stopped` row;
  `current_step` → `len(steps)` on a stop; `allow_failure` converting the
  decision-5 reasons into an advance. New spec §7.7, amended §7.2.
- [x] **015.5 — Parallel groups.** ✓ 2026-08-18 Depends: 015.4. `if:` on sub-steps (subset
  semantics, evaluated once at group start, so siblings are invisible to each
  other); `condition` rejected as a sub-step. Spec §7.5.
- [x] **015.6 — Fan-out lanes.** ✓ 2026-08-18 Depends: 015.4. Lane `if:` at run time; the
  zero-lane no-op; conservative creation-time counting; the early-stopped-lane
  clarification. Spec §7.6.
- [x] **015.7 — API.** ✓ 2026-08-18 Depends: 015.3. `skip_reason` and the `stopped` state
  through `internal/api` and `internal/apiclient`. Spec §13.2.
- [x] **015.8 — TUI.** ✓ 2026-08-18 Depends: 015.7. Skipped and stopped rows in the detail
  view, both on the existing no-transcript path.
- [x] **015.9 — Gate.** ✓ 2026-08-18 Depends: 015.4, 015.5, 015.6. New `scripts/m7-gate.sh`,
  command steps only, committed executable via `git update-index --chmod=+x`.
- [x] **015.10 — Docs.** ✓ 2026-08-18 Depends: all. User-facing schema, guides and
  block-reason pages; §20 promotion; CLAUDE.md's gate list.

## Verification

*2026-08-18.* All 10 subtasks delivered.

- `go run mage.go build test testrace lint`, plus `golangci-lint` cross-built
  for `windows`, `darwin` and `linux` — a host-only lint hides findings in
  build-tagged files, and this change touches none of them, which is worth
  having proved rather than assumed.
- Gates: `m1`, `m2`, `m5` and `m6` re-run to prove no regression; the new `m7`
  passes all seven scenarios.
- `m7` is **not wired into CI**, for the reason `m6` is not: the push token
  used for this work has no `workflow` scope, so a branch touching
  `.github/workflows` is rejected outright. The addition is two lines in
  `ci.yml`'s `gates` job, beside the existing M1/M2/M5 steps:

  ```yaml
      - name: M7 gate (conditions)
        shell: bash
        run: ./scripts/m7-gate.sh
  ```

  `CLAUDE.md`'s command list now names both `m6` and `m7` and says they are
  hand-run, so the gap is documented rather than silently carried.

### `m7` is portable where `m6` is not

`m6`'s `run:` bodies use `touch`, `seq` and `sleep 0.1`, which pwsh does not
speak — it is a POSIX-only gate whose header claims three platforms, and never
having been wired into CI is what let that stand. `m7`'s bodies are restricted
to `exit N` and `git …`, which `/bin/sh` and `pwsh` both accept, and its lanes
commit with `--allow-empty` rather than writing a file, because `printf >` and
`Set-Content` share no spelling. It should pass on all three platforms the day
someone can wire it in.

### One decision reversed by implementation

Decision 14 (`condition_error` does not consume the retry budget) reverses the
answer this task was designed with. The reasoning is in that decision; the
short version is that a guard is evaluated before the step becomes an attempt,
so honouring the budget would have meant inventing attempt rows for a step that
never ran — which decision 8 refuses on its own grounds.

### A pre-existing flake, observed and left alone

`TestFanOutBlocksWhenALaneDidNotFinish` (task 014) fails roughly 1 run in 30:
it cancels a lane it expects to still be `queued`, and the scheduler sometimes
admits that lane first. Measured at 1/30 on `origin/master` and at the same
order on this branch, so it is not this task's doing. Left unfixed
deliberately — a timing fix to somebody else's test does not belong in a diff
about conditions — and recorded here so the next person to see it red does not
re-diagnose it.
