# 018 — Control-flow review: four correctness fixes

**Status:** ✅ done (9/9) · **Opened:** 2026-08-19 · **Closed:** 2026-08-19

A review of the control-flow engine — `parallel` (§7.5), `fan_out` (§7.6),
`condition`/`if:` (§7.7) and `loop`/`break` (§7.8) — against the spec sections
that define them. Four of the five findings are behaviour changes with a
recorded alternative; the fifth is a leak. Nothing here adds a step type, a
field or a driver: the feature surface is unchanged, and the last section
records what a review of that surface concluded should *not* change.

## The findings

### 1. A partial fan-out spawn was a permanent dead end (bug)

`spawnLanes` created one child per lane with its own `CreateTask`, each
committing before the next. A failure on lane 2 therefore left lane 1
committed, and the recovery was `abandonPartialSpawn` — cancel the lanes that
made it, so "a retry starts from a clean slate rather than half a tree".

It did not. A cancelled lane keeps `parent_task_id` and `parent_step_index`,
and `runFanOut` decides *spawn or join* by whether the step has lanes at all.
So the human's `retry` found one lane, took the join path, read it as
`aborted`, and blocked `lane_failed` — forever, because retrying again found
the same lane. Nothing in the API or the TUI clears it. No test covered the
path.

**Fixed** by making the state unreachable rather than recoverable:
`store.CreateTasks` inserts every lane in one transaction, so a failure leaves
no lane behind, and `abandonPartialSpawn` is deleted.

### 2. The join ran against lanes that had not started (bug)

The same *spawn or join* test has a second hole, independent of the first.
"This step has lanes" only means "the lanes have finished" if the parent
reliably parked after spawning them — and the park is a transition that can
lose its compare-and-swap or fail to commit. When it does, the parent stays
`running` with `queued` lanes, §12.4 re-queues it, and the join blocks
`lane_failed` against work that is about to run perfectly well. `retry` walks
straight back into it.

**Fixed:** a parent admitted at a `fan_out` step whose lanes have not all
settled parks again instead of joining.

### 3. Group sibling-blindness only held on the first admission (spec deviation)

§7.5 promises that no `parallel` sibling can be read by another sub-step's
guard, and grounds it in *when* guards run — before anything in the group
starts. That reasoning covers one admission. `renderContext` sent `succeeded`
and `skipped` rows into `.Steps` unconditionally (only `failed` rows went
through the positional `precedes` filter), so a group re-admitted after one
sub-step failed exposed the siblings that had already succeeded.

The observable effect, now pinned by a test that fails without the fix: the
same guard against the same context rendered `[]` on the first run and
`[succeeded]` after a human pressed `retry`. A verdict that depends on retry
history is the decision cache §7.7 refused, reached from the other direction.

**Fixed:** `stepEnv.blindTo` omits every row sharing the group's `step_index`
that is not the sub-step's own, whatever state it ended in.

### 4. A leaked cancel in both structure steps (leak)

`runGroup` and `runLoop` each did:

```go
groupCtx, cancel := context.WithCancel(ctx)
if env.step.Timeout != nil {
    groupCtx, cancel = context.WithTimeout(ctx, env.step.Timeout.Std())
}
defer cancel()
```

With a `timeout:` set, the first context's `cancel` is overwritten and never
called, so that context stays attached to the parent for the rest of the task's
run. `govet`'s `lostcancel` does not see it, because `cancel` *is* used.

**Fixed:** one context, not two.

### 5. A loop with an empty `for_each` left its step index with no row at all

`fan_out` and `loop` are the two structure steps, and each has a degenerate
case where it is reached and runs nothing: every lane guarded off, and an empty
`for_each` list. The fan-out recorded a row saying so; the loop recorded
nothing, and `TestLoopForEachEmptyListSucceeds` asserted that it must not.

That leaves the one step index a task can pass through carrying no row —
against the phase 2 invariant that every one has at least one — and a detail
view with no way to tell "ran nothing" from "never reached".

**Fixed:** the empty case records one row under the loop's own id, `succeeded`
with `iteration: 0` and a summary. It is invisible to the loop's own
derivation, which filters on `iteration > 0`, and it is filtered out of
`.Steps`.

## Decisions

**D1 (2026-08-19) — a partial spawn is made unreachable, not recoverable.**
Beat: *delete the abandoned lanes*, which was the first choice and was
abandoned on contact. There is no task delete anywhere in vincent and no
`task.deleted` event, so it meant introducing the first destructive task
primitive plus an event type for clients to drop the phantom lane — in a system
whose stated posture is that nothing destroys work. Also beat: *filter aborted
lanes out of the spawn-or-join test*, which cannot work, because it would turn
a human's deliberate `cancel` of one lane into a silent re-spawn and §7.6 says
that must block `lane_failed`. One transaction removes the state instead, and
takes 25 lines of cleanup code with it.

**D2 (2026-08-19) — the join's precondition is settled lanes, not existing
lanes.** These are the same question only when the park always commits, and it
is a compare-and-swap. Parking again is idempotent and costs one admission;
the alternative is a block a human cannot clear.

**D3 (2026-08-19) — sibling-blindness is a property of the set, in every
admission.** Beat: *amend §7.5 to say siblings that succeeded under an earlier
admission are visible*. That is cheaper to write and worse to use: it makes a
guard's answer depend on whether a human has pressed `retry`, which is exactly
the non-reproducibility §7.6 chose declared lane order to avoid and §7.7 chose
never to cache a verdict to avoid. The set/sequence distinction is the axis
§7.5–§7.8 are built on; it should not hold only on Tuesdays.

**D4 (2026-08-19) — a loop's extent never falls below the iterations it has
rows for.** Beat: *pin the whole `for_each` list on the first admission* by
persisting it, which would break §7.8's "no loop cursor is persisted anywhere"
for a case that only bites when the author fed the loop something unstable.
Also beat: *leave it and document it*. This is defence-in-depth, and it is
recorded as such: every `for_each` source §8.4 offers — a step result, a task
field, `.Host` — is stable between admissions, so the shrink is not reachable
through any documented input today. It is eight lines, and the failure it
forecloses is the silent kind: a loop reporting success over iterations it
started and never revisited.

**D6 (2026-08-19) — the empty loop records a row, and that row is not a
`.Steps` entry.** §7.8's "the loop has no row of its own" is about its
*iterations*, which are the body's rows; with no iterations there are none, and
the invariant that loses is the phase 2 one. Beat: *leave it and reverse
nothing*, which was this review's own first answer — filed as a separate task on
the grounds that it changes what a detail view shows rather than fixing a
correctness bug. That reasoning does not survive the invariant being named: a
step index with no row is not a display preference.

The second half of the decision is the one that took the work. A `succeeded`
row is visible in `.Steps` under the loop's id, which would make that id a key
present exactly when the loop did **nothing** and absent when it did something
— an inverted signal, worse than no signal, and worse than the gap it was
closing. Beat: *record it as `skipped` with a new skip reason*, which is
arguably the more accurate state but reads as visible-and-meaningful under §7.7
and so has the same problem. So the row is `succeeded`, matching the fan-out's
no-lane row exactly, and `stepEnv.blindTo` filters every `loop`-typed row out
of `.Steps`. A `fan_out`'s row is a real result — "merged 3 lanes" — and stays
visible; the asymmetry between the two structure steps is now in one place and
stated, rather than spread across a missing row and a surprising key.

**D5 (2026-08-19) — no new step type, field or driver.** The last section says
why, item by item. The review's conclusion is that the control-flow surface is
the right size for the project's scope and that its stated omissions —
`while:`, `continue`, `on_false:`, nesting — are still right.

## Tasks

- [x] 018.1 `store.CreateTasks`: insert several tasks in one transaction,
  sharing the branch resolution and event append with `CreateTask` ✓ 2026-08-19
- [x] 018.2 `spawnLanes` uses it; `abandonPartialSpawn` deleted ✓ 2026-08-19
- [x] 018.3 `runFanOut` parks when its lanes have not all settled ✓ 2026-08-19
- [x] 018.4 `stepEnv.blindTo`: group set-invisibility in every admission
  ✓ 2026-08-19
- [x] 018.5 `planLoop` clamps the extent to the highest recorded iteration and
  re-checks the ceiling ✓ 2026-08-19
- [x] 018.6 one context per structure step, not two ✓ 2026-08-19
- [x] 018.7 §7.5, §7.6 and §7.8 amended in place; tests for each fix, each
  verified red without it ✓ 2026-08-19
- [x] 018.8 an empty `for_each` records one row under the loop's own id;
  `TestLoopForEachEmptyListSucceeds` reversed ✓ 2026-08-19
- [x] 018.9 `blindTo` keeps a `loop`-typed row out of `.Steps` ✓ 2026-08-19

## Verification

`go test ./...` and `go run mage.go lint` for `GOOS=linux`, `darwin` and
`windows` are clean. The four gates that touch control flow pass: `m1`, `m2`,
`m6` (parallel and fan-out), `m7` (conditions), `m8` (loops).

Each behaviour fix has a test that was verified to fail without it:

| Fix | Test | Failure without the fix |
|---|---|---|
| 1 | `TestCreateTasksIsAllOrNothing` | 1 task and 1 `task.created` committed, want 0 |
| 2 | `TestFanOutParksWhenItsLanesHaveNotSettled` | `stop = false` — falls through to the join |
| 3 | `TestGroupSiblingsStayInvisibleAcrossAdmissions` | attempt 2 reads `[succeeded]`, want `[]` |
| 4 | `TestLoopForEachExtentNeverShrinksBelowItsRows` | `plan.total = 1`, want 3 |
| 5 | `TestLoopForEachEmptyListSucceeds` | 0 rows at the loop, want 1 |
| 5 | `TestLoopForEachEmptyListSucceeds` | a later step reads the loop id as `[succeeded]`, want `[]` |

Fix 2 and 018.5 are driven directly rather than through the scheduler, because
each needs a state no admission produces on purpose. That is stated rather than
papered over: an end-to-end test would have to fake the same state anyway, and
would spend a worktree and a minute doing it.

## What a review of the feature set concluded not to change

The brief asked what should be added or modified. The answer is nothing, and
the reasons are worth recording because each of these will be proposed again.

**Already-settled omissions, not relitigated.** `while:` (§7.8 — the converge
loop is `count:` plus `break`, which puts the condition in the body where it
can see the body), `continue` (§7.8 — a `condition` in a loop body already
means it), `on_false:` on a `condition` (§7.7 — "stop and block" is a `command`
that exits nonzero; the gap the type fills is *stop and succeed*), and loose
truthiness in a guard (§7.7). These are binding decisions from tasks 015 and
016.

**Considered and declined:**

- **`.Loop.Total`** (so a prompt can say "attempt 3 of 5"). The cheapest thing
  on the list — the API's loop rollup already carries `max_iterations`, so this
  is one field through `LoopContext`. Declined because `.Loop.Index` plus
  `.Loop.IsLast` covers what a prompt actually needs to say, and §8.4 pays for
  every field in the API DTOs and the TUI. It is the first thing to add if a
  real workflow wants it.
- **`cancel_siblings:` on a `parallel` group** (fail-fast). Today a sub-step's
  failure never cancels its siblings, and §7.5 gives the reason: a
  nearly-finished test run should not be thrown away by a linter that failed
  first. Fail-fast is the right default for CI, where the run is free to
  restart; it is the wrong one here, where a sibling may be twenty minutes of
  agent time. An opt-in field would be honest, but it is a second policy on a
  step type whose whole value is that its members are independent, and the
  workflow that wants it can put the two steps in sequence.
- **Relaxing the nesting rules** (a `loop` in a `parallel`, a `parallel` in a
  loop body, nested loops). Every one of these is refused at load with the same
  reason: position is *derived from the rows* keyed by one `step_index`, and two
  derivations on one index have no answer. Lifting the restriction means
  persisting a structure cursor, which is the crash-recovery property tasks 014
  and 016 spent their design budget avoiding. Not worth it for shapes a lane's
  own workflow can already express.
- **`for_each` over `fan_out` lanes** (a dynamic lane list). This is the most
  tempting item and the most expensive. §7.6's creation-time cycle, `max_depth`
  and `max_tasks` refusals are all "possible because the whole tree's shape is
  static once lane lists are in the snapshot". A run-time lane list gives that
  up, and what replaces it is discovering a depth explosion as two hundred
  worktrees six hours later — the exact failure §7.6 exists to refuse in front
  of the person typing.
- **Structured `for_each` items** (maps rather than strings). §8.4 is
  string-valued throughout and `LoopContext.Item` says so deliberately. A
  workflow that needs fields per item can put them in a line and split them in
  the body.
- **A `switch`/multi-way branch.** A chain of `if:` guards is longer to write
  and needs no new concept; the shorter spelling buys nothing a reader of the
  YAML did not already have.
- **A step-level failure handler (`on_failure:` / try-catch).** `allow_failure:`
  plus an `if:` on the next step is the same thing composed from parts that
  already exist, and it keeps one failure vocabulary (§7.2) instead of two.

**One observability asymmetry, fixed rather than deferred.** The empty-loop row
was first written up here, as a display question worth a task of its own. It is
finding 5 above instead: naming the phase 2 invariant it broke settled that it
was not a display question. D6 records both halves, including the `.Steps`
visibility problem that only appeared once the row existed.
