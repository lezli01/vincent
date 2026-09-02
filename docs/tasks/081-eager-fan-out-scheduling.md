# 081 — Let a `fan_out` step schedule lanes eagerly

**Status:** ✅ done (1/1)
**Issue:** #302
**Amends:** §7.6, §11, §12.4, §18 (no new block reason — §18's `lane_failed`
row gains eager's wording)
**Extends:** [080](080-fan-out-dag.md) decision 9, which accepted barrier
scheduling as the shipped posture *and* named #302 as where eager scheduling
would be decided; **narrows** its decision 2, whose reversal of
[014](014-workflow-fan-out.md) decision 21 stays scoped to lane lists that use
`needs:` (decision 4 below)

Task 080 gave a `fan_out` step a `needs:` graph and ran it in **barrier
rounds**: a parked parent is re-queued only when its whole subtree has settled,
so a lane waits for its round rather than for its dependencies. Given
`4←{1,2}`, `5←{1,3}`, `6←{2,3}`, `7←6`, `8←6`, lanes 7 and 8 need only lane 6
but cannot start until 4 and 5 have settled too. This adds `schedule:` —
`barrier` (default) and `eager` — and makes the second one work.

The issue's framing that "the step's per-admission logic from #301 is
unchanged" was not accurate, and correcting it was the bulk of the work. Two
guards assumed a barrier: `runFanOut` parked immediately whenever any lane was
unsettled (under eager, that is every wake, so the step would never merge
anything), and `runJoin` failed `lane_failed` whenever any spawned lane was not
`done` (under eager, a merely running lane would read as a failure).

## Phases

- **081.1 — `schedule:` and the eager admission.** ✅ The field and its
  validation, the watermark column and the scheduler's wake branch, the eager
  variants of both guards, the merge counter, docs, the skill, the
  `update-workflows` checklist and an `m6` scenario.

## Decisions

### 1. The eager wake is edge-triggered by a settled-count watermark

The issue's option (a) — "wake on any descendant settling, no new state" — does
not work as written. `resumeSettledParents` is **level-triggered**: `admit`
calls it on every wake and every 5 s tick, and "a direct child has settled"
stays true after the parent parks again. An eager parent would be re-queued,
find nothing, park, and be re-queued by its own park event. That is a spin, not
the bounded churn the issue costs out.

The parent therefore carries a watermark: the number of **settled direct
children** the admission observed, persisted when it parks
(`tasks.settled_children_watermark`, migration 0024). The scheduler re-queues
an eager parent when the live settled-direct-child count exceeds that
watermark. Properties this buys:

- Pure SQL in the scheduler — it never parses the lane graph, and
  `internal/scheduler` keeps its two imports (`config`, `store`).
- Self-clearing, so no spin.
- It is a wake *position*, not a second copy of the graph. It is recomputed
  from the rows at every park, so `retry` and `edit + retry` — which rewrite
  the snapshot at `internal/taskrun/actions.go` — cannot stale it. That is the
  failure mode that sank the issue's option (b).
- Churn is bounded by the **direct** lane count, not by subtree size: a
  depth-2 descendant settling does not move a root's direct-child count.
- The watermark is written from the count read at the *start* of the
  admission, so a child settling mid-admission still moves the count past it
  and wakes the parent again.
- `ChildrenRollup.Done()` and the existing barrier resume stay untouched and
  keep running for eager parents too. A wake lost to any race degrades to
  barrier timing rather than stranding the parent — eager is a throughput
  optimization layered over a rule that already terminates.

NULL means barrier, which is every parent that ever parked before the column
existed. `TransitionTask` clears it on any transition *out of*
`awaiting_children`, the construction `admit_not_before` and
`pending_input_json` already use, so a barrier park can never inherit an
earlier eager step's number.

The count is **direct children**, not this step's lanes. A workflow may fan out
more than once, and an earlier step's lanes are all settled by the time a later
one parks; counting only the current step's lanes would undercount by exactly
those, and the watermark would be exceeded the moment it was written.

### 2. `iteration` under eager is a monotonic merge counter

`roundOf` derives `step_runs.iteration` from the lane's wave in the graph.
Under eager, two admissions can merge two lanes of the same wave and compute
the same iteration — the exact retry-budget and transcript-name collision task
080 decision 3 exists to prevent (`stepEnv.ref()` scopes attempts on
`(task, step_index, step_id, iteration)`, and §12.2 names transcripts
`{step_index}-i{iteration}-{step_id}-{attempt}`).

Under eager, `iteration` is instead how many merge rows this step already has,
counted as `COUNT(DISTINCT iteration)` over its **succeeded** rows
(`store.SucceededIterations`). Succeeded only, deliberately: a merge that
blocked on a conflict must re-enter at the same iteration when a human retries
it, or the retry would start a fresh budget and write its transcript somewhere
else.

Barrier keeps the wave-derived number and is bit-for-bit unchanged, because its
Nth merge admission merges wave N — the two agree there, which is what lets the
default path stay untested-for-change.

Consequence stated in the docs: an eager step can write up to one merge row per
lane. `.Steps["build"].Result` still resolves to the latest row, as decision
3's `blindTo` audit established.

### 3. A settled-not-done lane blocks before anything new is merged

When an eager admission finds a lane settled without finishing while others are
in flight, it blocks `lane_failed` **merging nothing new** in that admission.
Already-merged lanes stay merged (task 080 decision 2), in-flight lanes are
left to finish, and no further lane is spawned.

The alternative — merge everything mergeable, then block — would make the
branch content at block time depend on which wake happened to notice the
failure. That is a stopwatch question about *delivered commits*, which is a
strictly worse version of the timing dependence eager is already asking the
author to accept.

*Correction to the brief's wording.* "Settled without finishing" is `aborted`
or `archived`, **not `blocked`**: `taskstate.Settled` does not include
`blocked`, and §7.6 already says a `blocked`, `awaiting_gate` or `paused` lane
*holds the join open* until a human resolves it. Treating `blocked` as a
failure here would have made eager diverge from barrier on a case the spec
already decides, and would have blocked the parent on a lane a human was about
to retry. The eager rule is therefore the same three-way split barrier makes
implicitly: `done` merges, unsettled is "not yet", settled-not-done blocks.

### 4. `eager` degenerates to `barrier` when no lane declares `needs:`

Without `needs:`, every lane spawns in one round under either mode, so eager
buys no scheduling. What it would change is **merging**: lanes would merge as
they finish, so a late failure leaves earlier lanes merged — widening task 080
decision 2's reversal of task 014 decision 21 to flat lists, which #301
deliberately kept bit-for-bit (`TestFanOutFlatListIsOneRound` pins it).

So a step whose selected lanes declare no `needs:` **among themselves** runs as
a barrier whatever `schedule:` says. "Among themselves" matches `readyLanes`
exactly: a `needs:` naming a lane this run did not select imposes no ordering,
because a lane that will never spawn cannot be waited for. The decision is
taken at **spawn**, over the selected lanes, so a derived list that turns out
flat is covered by the same rule. It is not a load-time rejection:
`schedule: eager` on a list that happens to be flat is redundant, not wrong,
and for a derived list nobody can know at load.

This is why the unsettled-lane guard at the top of `runFanOut` is applied
twice: once cheaply for a step that does not declare `eager` at all, and again
after selection for a declared-eager step whose selected lanes turn out flat.
Selection is the only thing that can answer the question, and running it before
the guard for every step would change the barrier path.

### 5. `barrier` stays the default, for the reason the issue gives

Eager makes a lane's starting tree timing-dependent, and §8.4 already omits
`.Now` from the template context on exactly that argument. A workflow trading
reproducibility for throughput says so in the file. Under eager the parent
branch's *commit topology* is not reproducible even when the delivered tree is,
because the set a given merge commit joins varies between runs — this is stated
in §7.6, in the guide and in the schema reference, not left to be discovered.

## Notes

- `internal/taskrun/recover.go` needed no change, as the brief predicted.
  Recovery re-merges from the top, merges are idempotent, a lane with a child
  row is never in the ready set, and the transition out of `awaiting_children`
  clears the watermark, so the next admission recomputes it. §12.4 gained the
  sentence saying so.
- `scripts/m6-gate.sh` scenario 10 is the first `m6` scenario to drive `needs:`
  at all — task 080 left the DAG scenario out explicitly. It asserts from the
  API that an eager DAG spawns a dependent lane while an unrelated sibling is
  still running.
