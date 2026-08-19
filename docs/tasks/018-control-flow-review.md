# 018 — Control-flow review: three correctness fixes

**Status:** ✅ done (7/7) · **Opened:** 2026-08-19 · **Closed:** 2026-08-19

A review of the control-flow engine — `parallel` (§7.5), `fan_out` (§7.6),
`condition`/`if:` (§7.7) and `loop`/`break` (§7.8) — against the spec sections
that define them. Three of the four findings are behaviour changes with a
recorded alternative; the fourth is a leak. Nothing here adds a step type, a
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

**One observability asymmetry, left alone.** A `fan_out` that selects no lanes
records a decision row saying so; a `loop` whose `for_each` list is empty
records nothing, and `TestLoopForEachEmptyListSucceeds` asserts that it must
not. The two are inconsistent, and the loop's side is the odd one — "every step
index a task passes through has at least one row" is a phase 2 decision the
empty loop breaks. It is left as it is because the assertion is deliberate and
reversing it is a change to what the TUI shows for a step that did nothing, not
a correctness fix. Worth a task of its own if the empty-loop case ever confuses
someone reading a detail view.
