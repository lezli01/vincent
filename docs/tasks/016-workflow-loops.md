# 016 — Loops in workflows (`type: loop`, `type: break`)

**Status:** ✅ done (10/10) · **Opened:** 2026-08-18 · **Closed:** 2026-08-18

A workflow may now repeat a body of steps — a fixed number of times, or once
per item in a list:

```yaml
steps:
  # Fix until green: probe, break, repair. Bounded by construction.
  - id: green
    type: loop
    count: 5
    steps:
      - { id: suite, type: command, run: go test ./..., allow_failure: true, max_retries: 0 }
      - { id: passed, type: break, if: '{{ eq (index .Steps "suite").ExitCode 0 }}' }
      - id: repair
        type: agent
        prompt: |
          The suite is red:

          {{ (index .Steps "suite").Result }}

          Fix the underlying cause. Do not weaken, skip or delete a test.

  # Once per changed file, discovered at run time.
  - id: changed
    type: command
    run: git diff --name-only {{.Task.BaseBranch}}...HEAD -- '*.go' | grep -v _test.go
    allow_failure: true

  - id: review-each
    type: loop
    for_each: '{{ .Steps.changed.Result }}'
    max_iterations: 25
    steps:
      - { id: read, type: agent, prompt: 'Review {{ .Loop.Item }} against CLAUDE.md.' }
```

Two step types and one context variable:

- **`type: loop`** — a structure step in `parallel`'s image (§7.5): one index,
  one scheduler slot, the task's one worktree, no branch and nothing to merge.
  Its body is a **sequence**, run repeatedly.
- **Exactly one driver**, `count:` or `for_each:`. There is no `while:`
  (decision 2).
- **`type: break`** — ends the loop successfully. `type: condition` inside a
  body ends *that iteration* and the loop continues, which is `continue` with
  no new type (decision 3).
- **`.Loop`** — `Index`, `Item`, `IsFirst`, `IsLast` (decision 9).

## The problem

Task 015 gave a workflow the ability to *decide*, and left it unable to
*repeat*. Every workflow is still one pass down a flat list, so the shapes that
matter most in agent work cannot be written at all:

- **Converge.** "Run the tests, fix what broke, run them again" is the single
  most common agent loop there is. Today it is `max_retries` on one step, which
  only re-runs *that step* on *its own* failure — there is no way to say "run
  the probe, and if it is red run a different step, then probe again."
- **Repeat.** "Run the race detector ten times to prove the flake is gone" has
  to be written as ten copy-pasted steps with ten step ids.
- **Iterate a set.** "Do this for each changed file / each module / each item
  the last step found" has no spelling. `fan_out` (§7.6) looks like it should
  fit and does not: its lane list is static in the snapshot at creation, which
  is exactly what makes its cycle and `max_tasks` checks possible, and a list a
  step discovers at run time is not static.

§20 keeps looping out of v1 only implicitly — it is not named there, because
015 promoted "branching and conditionals" out and left *nested* control flow
behind. A loop body is nested control flow, and 015 decision 1 named the
trigger for revisiting it: **"the first workflow that cannot be written flat."**
A converge loop is that workflow. This task is that trigger firing, and it is
cheaper than the `branch`/`switch` case 015 deferred: a loop body has one arm,
so the step list stays a list and `current_step` stays an integer.

## Decisions

### 1. A loop is a step type with a body, not a field on a step

*2026-08-18.* `type: loop` carries `steps:`, exactly as `parallel` does. It
occupies one `step_index`, holds one scheduler slot, and its body steps share
that index.

**Beat:** a `loop:` field on an ordinary step (`- {id: fix, type: agent, loop:
{count: 5}}`). It is flatter and it changes no invariant — but a one-step body
cannot express the case that opened this task. Converge is *probe, decide,
repair*, three steps, and a mechanism that cannot express its own motivating
example is not the mechanism.

**Beat:** flattening at snapshot time — expanding `count: 5` into five real
steps when the task is created. It needs no engine change at all, and it fails
on `for_each:` over a run-time list, whose length is unknown at creation. A
driver that works for one of two drivers is a special case wearing a feature's
name.

The precedent is what makes this affordable. §7.5 already paid for "a structure
step whose sub-steps share its index and whose outcome is derived"; this reuses
that shape rather than inventing a second one. What a loop adds over a group is
that its body is ordered and runs more than once.

### 2. Two drivers — `count:` and `for_each:`. There is no `while:`

*2026-08-18.* A loop carries exactly one driver, validated at load, the way a
`fan_out` lane carries exactly one of `workflow:`/`steps:`.

`while:` was in this design until the examples were written, and it does not
survive contact with them. A guard is a pure §8.4 template: it can read
`.Task`, `.Project`, `.Workflow`, `.Host` and `.Steps`, and nothing else. It
cannot run a command. So the only thing a *useful* loop condition can read is
what the body itself produced, through `.Steps` — and on the first iteration
the body has not run, so there is no row.

That leaves three ways out and none of them is good:

- Fail the first evaluation with `condition_error`. Correct, loud, and it makes
  the archetype unwritable.
- Let the missing row render a zero `StepResult`, so `ExitCode` reads `0`. This
  is the actual behaviour of `index` on a missing map key, and it means
  `{{ ne (index .Steps "suite").ExitCode 0 }}` is **false** on iteration 1 and
  the loop silently never runs. Precisely the silent hole `missingkey=error`
  exists to prevent (§8.4).
- Make `while:` a do-while by not evaluating it before the first iteration.
  Honest about the mechanism, dishonest about the word.

And a `while:` reading a step *before* the loop reads a constant, so it spins to
`max_iterations` every time. Meanwhile `count:` + `break` (decision 3) writes
the same loop correctly, post-test by construction, with the ceiling that
decision 5 was going to demand of every `while:` anyway. The condition ends up
in the body where it can see the body, and there is no pre/post-test rule to
document.

So `while:` is not deferred pending a better idea — it is **refused as a false
friend**. The trigger for reconsidering is a loop whose exit condition depends
on something no body step can observe.

### 3. `break` ends the loop; `condition` inside a body ends the iteration

*2026-08-18.* `type: break` carries `id`, `name` and a required `if:`, and
nothing else — decision 7 of task 015, applied again for the same reason: it
starts no process, so it cannot time out, be interrupted, be retried or write a
transcript. A true guard ends the loop and the cursor advances past the loop
step; the loop **succeeds**. It is rejected outside a loop body, symmetric with
`condition` being rejected inside a `parallel` group.

`type: condition` inside a loop body keeps the meaning §7.7 gave it — "end the
sequence" — and the enclosing structure supplies the consequence. A loop body
*is* a sequence, so ending it ends **that iteration**, and the loop continues
with the next. That is `continue`, for free, with no third type.

This is 015 decision 3 reapplied: one word, one meaning, whose consequence
follows from what it is attached to. A `condition` at the top level ends the
run; in a group it is rejected because a set has no sequence to end; in a loop
body it ends the iteration.

**Beat:** a `continue` type alongside `break`. Symmetrical, and it would mean
`condition` had a *fourth* reading inside a loop body (or had to be rejected
there, which throws away the composition). One new type, not two.

**Beat:** reusing `condition` for both by position or by a field. Two control
flows from one type, told apart by a modifier, is the `continue_if:` mistake
015 decision 2 already refused.

### 4. A loop runs in the task's one worktree, sequentially

*2026-08-18.* No branch, no child task, no merge, and iterations run one after
another. `max_parallel` has no meaning on a loop.

**Beat:** an iteration as a child task, `fan_out`-shaped — one branch per item,
merged back. It is the natural next want for `for_each`, and it collides
head-on with what makes §7.6 safe: the creation-time cycle, `max_depth` and
`max_tasks` checks are possible **only because the lane list is static in the
snapshot**. A list whose length is discovered at run time destroys that, and
015 decision 11 already had to weaken those checks into a conservative
over-approximation just to allow *conditional* lanes. Making the width dynamic
too would leave nothing to check at creation.

Dynamic per-item fan-out is therefore a separate question — plausibly
`for_each:` on a `fan_out` step — and it needs its own answer to "what replaces
the creation-time bound". Recorded in §20 with that trigger, not smuggled in
here. Keeping them apart also keeps §7.5's and §7.6's meanings intact: a loop
is a group that runs its members in order and more than once, and it inherits
§7.5's "concurrent writes to one worktree are a workflow bug" only in the
degenerate sense that there is no concurrency to have.

### 5. Bounded by construction; the cap blocks

*2026-08-18.* `max_iterations` on the step, defaulting to a new config key
`loop.max_iterations` (default **10**), beside `parallel.max_parallel` and
`fan_out.max_depth`/`max_tasks`. `count:` is validated against the same ceiling
at load, so `count: 5000` is refused in front of the person typing.

Reaching the cap **blocks** the task with a new reason `loop_limit`. It does not
succeed and it does not silently advance.

The reason it must block is §7.2's posture and 015 decision 8's precedent from
the other side. A loop that ran out of iterations did not achieve what it was
looping for; advancing would hand every downstream guard a `.Steps` that says
the work is finished. `condition` exists to *stop and succeed* when a run
genuinely has nothing more to do — that is a decision the workflow made. Running
out of tries is not a decision, it is a wall.

**Beat:** `max_iterations` required rather than defaulted. A required field on
`count:` is redundant (the count *is* the bound), and defaulting keeps the
common `for_each` short. The default is deliberately low: an agent step is
minutes and dollars, and 10 iterations of a three-step body is already 30 agent
runs.

### 6. Iterations and retries are orthogonal

*2026-08-18.* Retries are for a step that **failed**; iterations are for a body
that **succeeded and must run again**. Each body step keeps its own
`max_retries`, spent within an iteration, exactly as a `parallel` sub-step does
(task 014 decisions 17, 18).

A body step that exhausts its budget fails the iteration, and by default fails
the loop, which blocks the task with that step's own reason. `allow_failure:`
(§7.2) is how an author says "keep looping on a red probe" — it is already the
field that turns a failure into readable data, and decision 3's `break` is
already the thing that reads it. No new field.

### 7. `iteration` is a column; the loop owns no row and no cursor

*2026-08-18.* Migration `0009` adds `step_runs.iteration INTEGER NOT NULL
DEFAULT 0`. A body step's row carries the **loop's** `step_index` and its own
`iteration` (1-based); non-loop rows keep `0`. The loop step itself writes **no
row** — its outcome is collected from the body's rows — and no loop cursor is
persisted anywhere.

Both halves are task 014 decision 17 restated, and they buy the same thing:
a re-admission derives what is left to do from the rows. Position is
`(step_index, iteration, body position)`; a resumed loop skips body steps whose
latest attempt succeeded and continues **mid-iteration**, which is §7.5's rule
verbatim. Restarting the iteration would discard work a human may have waited an
hour for.

**Beat:** composite step ids (`repair#3`), needing no migration. They poison
`.Steps` keys, break the "step ids are unique across the whole workflow" rule by
manufacturing ids that are not in the file, and make every consumer parse a
string to recover a number.

**Beat:** a persisted loop cursor on the task row. It is a second source of
truth for a fact the rows already hold, and §12.4 recovery would have to
reconcile them.

### 8. The row carries its item; the extent is a fact the rows hold

*2026-08-18.* Migration `0009` also adds `step_runs.loop_item TEXT`. Iterations
that already have rows take their item **from those rows**; only new iterations
draw from the re-derived list, and the loop ends at that list's length.

This is where a `for_each` list parts company with a guard. 015 decision 10 says
guards are re-evaluated every time and never sticky, and that is right for a
guard: it is a *question*, and re-asking it against current facts is the point.
A `for_each` list is not a question — it is the loop's **extent**, the thing
iteration numbers are indices into. Re-deriving it mid-loop (after a crash,
after an `edit + retry` on the producing step, after a task field changed) would
make "iteration 3" name a different item than the row recorded, and decision 7's
resumption would silently re-index onto the wrong work.

Recording the item on the row is §7.6's move: "which lanes are already merged is
a fact git holds", so no merge cursor is persisted. Here, which item iteration 3
ran is a fact the row holds. There is no cache to invalidate and no new block
reason; drift is bounded to iterations that have not started.

### 9. `.Loop` joins the context, and `.Steps` visibility becomes positional

*2026-08-18.* `.Loop{Index, Item, IsFirst, IsLast}` — `Index` 1-based, `0`
outside any loop so a shared template can tell. `Item` is a **string**:
`Task.Fields` is `map[string]string` and every value already in §8.4 is a
string, so structured items would push a new type through the render context,
the API DTOs and the TUI for a case nobody has yet.

`.Steps["suite"]` under repetition resolves to the **latest** iteration. This
falls out of the existing assembly for free — `engine.go` walks rows in order
and assigns `steps[run.StepID] = res`, so last row wins — provided rows are
ordered by `(step_index, iteration, attempt)`.

The part that is **not** free is the failed-row filter. `engine.go` currently
hides a failed row with `if run.StepIndex >= env.index`, which is what keeps
`parallel` siblings invisible to each other (§7.5 requires it). Body steps of a
loop share the loop's index, so under that rule a `break` guard could not read
the `allow_failure` probe sitting two lines above it in its own body — the
converge loop would never break. The rule widens to a **position** comparison: a
row is visible iff it precedes the current position in
`(step_index, iteration, body position)` order, where a `parallel` sub-step's
body position is undefined and therefore never precedes a sibling.

Set-invisibility is preserved, sequence-visibility inside a loop body is gained,
and a step's own failed attempt still stays out of `.Steps["itself"]` mid-retry
because `.LastFailure` is that channel (015 decision 6).

### 10. What a loop body may contain

*2026-08-18.* `agent`, `command`, `condition` and `break`. Rejected at load:
`manual`, `on_input: require`, `parallel`, `fan_out`, and a nested `loop`.

`manual` and `on_input: require` for §7.5's reason exactly — a gate ends the
actor goroutine and releases the slot, and no state means "iteration 3 of this
loop is gated".

`fan_out` for a sharper version of the same: parking in `awaiting_children` ends
the goroutine, and resuming into the middle of an iteration is precisely the
state decision 7 declined to persist. The rows could not say it either, since a
parked parent has no row to hold it.

`parallel` and a nested `loop` because they would make decision 7's derivation
recursive, and following §7.5 is affordable only while it stays one level deep.
The trigger for reconsidering is named: the first workflow that genuinely needs
its iterations to fan out or to run a group.

### 11. The fields a `loop` step carries

*2026-08-18.* `id`, `name`, `type`, `if`, `timeout`, `steps`, one driver, and
`max_iterations`.

- **`if:`** guards the whole loop, with ordinary skip semantics (§7.7).
- **`timeout:`** bounds the **whole** loop and fails it `timeout`, mirroring
  §7.5's group timeout. Body steps still have their own.
- **No `max_retries`.** A loop has no attempt of its own to retry; the body
  steps carry the budgets (decision 6, task 014 decisions 17/18).
- **No `allow_failure`.** On a loop it could only mean "a `loop_limit` block
  advances anyway", which is the lie decision 5 refused. On a body step it
  already does the useful thing.

### 12. Human actions act on the whole loop, and `retry` resumes

*2026-08-18.* On a task blocked inside a loop:

- **`skip`** skips the **whole loop step** and advances past it. There is no
  "skip this iteration" action — §7.6 already settled this posture for a
  structure step ("`skip` keeps its meaning — it skips the whole join — and is
  deliberately not a 'proceed without that lane' button"), and a second meaning
  for one word would need a state nobody can see.
- **`retry`** resumes: the failed body step of the current iteration re-runs
  with a fresh budget, derived per decision 7, and the loop carries on from
  there. It does not restart at iteration 1.
- **`edit + retry`** overrides a body step's prompt or command in the task's
  snapshot, and therefore applies to **every remaining iteration**. This is the
  useful behaviour — fix the prompt, let it keep going — and it is stated in §6
  and the guide rather than discovered on iteration 4.

### 13. Transcripts gain an iteration segment, loop bodies only

*2026-08-18.* `{step_index}-i{iteration}-{step_id}-{attempt}.jsonl` for a body
step; every other name is unchanged. `transcript_path` is stored on the row, so
nothing parses these — the segment exists for the human reading a directory.

**Beat:** an iteration segment on every transcript for uniformity. It renames
every file vincent has ever written to make a directory listing consistent with
itself.

### 14. No new event type

*2026-08-18.* 015 decision 13 declined `step.skipped` on the grounds that
vincent has never emitted a step-lifecycle event and a client can reconstruct
one from `GET /v1/tasks/{id}/steps`. That argument is stronger here, not weaker:
ten iterations of a four-step body would put forty durable events on the stream
to say what forty rows already say, and §12's fan-out is post-commit off the
store hook, so the rows *are* the notification.

Visibility instead comes from a `loop` rollup on `GET /v1/tasks/{id}`, present
only while a loop step is current — `{driver, iteration, max_iterations, item}`,
the shape of §13.2's `children` rollup one level cheaper. The TUI board reads
`step 3/7 · loop 4/10`; the detail view groups body rows by iteration, folded
shut with the latest open, reusing task 012's grouping rather than inventing a
second one.

### 15. No template FuncMap in this task

*2026-08-18.* `internal/workflow/template.go` calls
`template.New(name).Option("missingkey=error").Parse(text)` and never calls
`Funcs()`. Stock `text/template` builtins only: `eq`, `ne`, `index`, `len`,
`and`, `or`, `not`, `printf`, `slice`. No `hasSuffix`, `contains`, `split`,
`trim` or `default`.

`for_each` makes `.Loop.Item` a string authors will immediately want to test —
extension, prefix, path segment — and none of that is expressible. The answer
for now is to filter **at the source**, in the command that produces the list
(`git diff --name-only … | grep -v _test.go`), which works, keeps the filtering
where the output already is, and leaves §8.4 untouched.

A FuncMap is a §8.4 change that lands in *every* prompt, check, run and guard
template at once, and it invites the expression-language argument 015 decision 4
settled. It earns its own task. Trigger: the first `for_each` that cannot filter
at its source.

## Spec §7.8 draft

*Landed in `docs/spec.md` as §7.8 on 2026-08-18, with the code.* Kept here as
the record of what was drafted; the spec is the live text.

> ### 7.8 Loops
>
> *Added 2026-08-__ (task 016).* A `loop` step runs its body repeatedly in the
> task's **one** worktree. It creates no branch, no child task and nothing to
> merge — that is `fan_out` (§7.6). Where a `parallel` group (§7.5) is a set run
> once, a loop is a **sequence** run more than once.
>
> ```yaml
> - id: green
>   type: loop
>   count: 5
>   steps:
>     - { id: suite,  type: command, run: go test ./..., allow_failure: true, max_retries: 0 }
>     - { id: passed, type: break,   if: '{{ eq (index .Steps "suite").ExitCode 0 }}' }
>     - { id: repair, type: agent,   prompt: "The suite is red: {{ (index .Steps \"suite\").Result }}" }
> ```
>
> - **Exactly one driver.** `count:` (a positive integer, at most
>   `loop.max_iterations`) or `for_each:` (a YAML sequence of templates, or a
>   scalar template which is rendered, trimmed and split on newlines with empty
>   lines dropped). There is no `while:`; the converge loop is `count:` plus
>   `break`, which puts the condition in the body where it can see the body.
> - **The body is `agent`, `command`, `condition` and `break`.** `manual`,
>   `on_input: require`, `parallel`, `fan_out` and a nested `loop` are rejected
>   at load, each for the reason §7.5 rejects it: anything that ends the actor
>   goroutine mid-body is state a derived loop position cannot express.
> - **Rows.** One `step_runs` row per body step per iteration, all sharing the
>   loop's `step_index`, told apart by `step_id` and a 1-based `iteration`; a
>   `for_each` row also carries its `loop_item`. The loop has no row of its own;
>   its outcome is derived. A body step's transcript is
>   `{step_index}-i{iteration}-{step_id}-{attempt}.jsonl` (§12.2).
> - **`.Loop`** (§8.4) is `Index`, `Item`, `IsFirst`, `IsLast`, with `Index: 0`
>   outside any loop.
> - **Ending.** The driver being exhausted, or a `break` whose guard is true,
>   ends the loop **successfully** and the cursor advances. A `condition` whose
>   guard is false inside a body ends **that iteration**; the loop continues. A
>   loop reaching `max_iterations` **blocks** with `loop_limit` — running out of
>   tries is not a decision, and `condition` (§7.7) is what a workflow uses to
>   stop and succeed. A `for_each` list longer than `max_iterations` blocks
>   before the first iteration, naming the count. An empty list, or a whole loop
>   guarded off by its `if:`, succeeds having run nothing.
> - **Failure.** A body step that exhausts its retry budget fails the iteration
>   and blocks the task with that step's own reason. `allow_failure:` (§7.2) is
>   how a probe's red result becomes data a `break` can read. Retries are for a
>   step that failed; iterations are for a body that succeeded and must run
>   again.
> - **Resuming.** Position is derived from the rows, never persisted: a
>   re-admitted loop skips body steps whose latest attempt succeeded and
>   continues mid-iteration. Iterations that already have rows take their item
>   from those rows; only new iterations draw from a re-derived `for_each` list.
> - **Concurrency.** A loop is one step, one slot, one worktree, and its
>   iterations are strictly sequential. §11's caps see one running task, exactly
>   as they always did.

## Tasks

- [x] **016.1 — Schema, parser, validation.** `StepLoop` and `StepBreak` in the
  type set; `Count`, `ForEach`, `MaxIterations`, `Steps` on `Step`. Exactly one
  driver; `count` within the ceiling; body-type rejection list (decision 10);
  `break` required-`if:`-and-nothing-else and rejected outside a loop body;
  `condition` **permitted** in a body (it is rejected only in a group); loop
  ids and body ids unique across the whole workflow; `max_retries` and
  `allow_failure` rejected on a loop. `loop.max_iterations` in
  `internal/config`. Spec §8.2, §8.1.
- [x] **016.2 — Store.** Migration `0009_loops.sql`: `step_runs.iteration`,
  `step_runs.loop_item`. `ListStepRuns` ordered
  `(step_index, iteration, attempt)`. Spec §14.
- [x] **016.3 — Template context.** `.Loop` in `RenderContext`; the positional
  `.Steps` visibility rule replacing `run.StepIndex >= env.index`, with the
  `parallel`-sibling case pinned by a test that fails under a naive rewrite.
  Spec §8.4.
  *Depends: 016.2.*
- [x] **016.4 — Engine: `count:`.** The loop executor, body sequencing, derived
  position and mid-iteration resumption; `loop_limit`; the loop-level
  `timeout:`. Spec §7.8, §18.
  *Depends: 016.1, 016.2, 016.3.*
- [x] **016.5 — Engine: `break` and iteration-scoped `condition`.** `break`
  ending the loop successfully; `condition` inside a body ending the iteration
  and leaving its existing top-level meaning untouched. Spec §7.7, §7.8.
  *Depends: 016.4.*
- [x] **016.6 — Engine: `for_each:`.** List resolution (sequence or scalar,
  split on newlines); `loop_item` on the row and rows-are-authoritative
  resumption; the over-cap block before iteration 1; the empty-list no-op.
  Document the `outputTailLines` (100-line) bound on a list drawn from
  `.Steps[…].Result`. Spec §7.8.
  *Depends: 016.4.*
- [x] **016.7 — Recovery and human actions.** §12.4 finalization of an
  interrupted body step; `skip` skipping the whole loop; `retry` resuming at the
  failed body step; `edit + retry` applying to remaining iterations. Spec §6,
  §12.4.
  *Depends: 016.4, 016.6.*
- [x] **016.8 — API and TUI.** `iteration` and `loop_item` through
  `internal/api` and `internal/apiclient`; the `loop` rollup on the task detail
  endpoint; board `loop 4/10`; detail-view rows grouped by iteration, folded,
  latest open. No new event type (decision 14). Spec §13.2.
  *Depends: 016.2, 016.4.*
- [x] **016.9 — Gate.** `scripts/m8-gate.sh`, committed executable via
  `git update-index --chmod=+x`. Command steps only, bodies restricted to
  `exit N` and `git …` so it is portable to pwsh the way `m7` is and `m6` is
  not. Scenarios: count to completion; converge-and-break; `loop_limit`;
  `for_each` static; `for_each` from step output; empty list; iteration-scoped
  `condition`; crash mid-iteration and resume.
  *Depends: 016.4, 016.5, 016.6, 016.7.*
- [x] **016.10 — Docs.** Workflow schema, guides and block-reason pages; the
  config reference for `loop.max_iterations`; CLAUDE.md's gate list; the §20
  amendment promoting loops out of future work and leaving behind, with named
  triggers, `branch`/`switch` (015 decision 1), dynamic per-item fan-out
  (decision 4), and a template FuncMap (decision 15).
  *Depends: all.*

## Notes

**`m8` will not be wired into CI**, for the reason `m6` and `m7` are not: the
push token used for this work has no `workflow` scope, so a branch touching
`.github/workflows` is rejected outright. The addition is two lines in `ci.yml`'s
`gates` job:

```yaml
    - name: M8 gate (loops)
      shell: bash
      run: ./scripts/m8-gate.sh
```

Say so in CLAUDE.md's command list beside `m6` and `m7`, so the gap stays
documented rather than silently carried.

**Three things the implementation settled that the plan left open.**

- **`loop_limit` is reachable for `count:` too.** Decision 5 says the cap
  blocks; §7.8's "Ending" says the *driver* being exhausted succeeds. Both are
  true, and they meet at the ceiling: `count:` is validated against it at load,
  so the only way a count loop reaches it is a config edit landing under a
  queued task — `loop.max_iterations` is read per loop, so a hot reload does
  exactly that. The engine enforces it, which is what stops the reason being
  decorative on one of the two drivers.
- **`for_each` has one rule, not two.** The draft distinguished "a sequence of
  templates" from "a scalar template, split on newlines". The implementation
  applies the split to *every* entry, which agrees with the draft on both
  stated cases and makes a hand-written list and a command's output one
  mechanism rather than two.
- **§8.4's output tail is 200 lines, not 100.** The spec said 100 and the
  daemon has always used 200; `for_each:` reading `.Steps[…].Result` turns that
  from incidental into load-bearing, so the spec was corrected in the same PR
  rather than left to be discovered.

**Two decisions here reverse the design this task was drafted with**, both
because examples were written before the doc was: `while:` was a driver
(decision 2), and a template FuncMap was assumed available for filtering
`.Loop.Item` (decision 15). Neither survived being asked to produce a working
workflow.
