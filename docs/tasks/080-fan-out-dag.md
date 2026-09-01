# 080 — Fan out a dynamically derived DAG of lanes

**Status:** ✅ done (2/2)
**Issue:** #301
**Amends:** §5.3 (unchanged, and says why), §7.6, §7.8, §8.4, §12.4, §18
**Extends:** [014](014-workflow-fan-out.md) decisions 5 and 28 under their own
terms; **reverses** its decision 21 where, and only where, `needs:` is used
**Follow-up:** eager per-lane scheduling is #302, deliberately not here

A `fan_out` step's lanes were a fixed *set*, decided at task creation. A plan is
a *graph* whose width is discovered at run time. This gives the step both
halves: `needs:` between lanes, and a lane list derived from what an earlier
step found.

## Phases

- **080.1 — `needs:` and the round scheduler.** ✅ Declared lanes only. No
  bounds change: a static lane list is still countable at creation, so the whole
  scheduling change lands without touching §7.6's creation-time guarantee.
- **080.2 — derivation.** ✅ `for_each:` plus a single `lane:` template,
  snapshot materialization at spawn, `max_lanes:` and the run-time tree bound.
  This is where the creation-time guarantee is given up, for a derived list
  only.

## Decisions

### 1. A derived item is a JSON object, one per line

`for_each` rendering is reused as §7.8 defines it — every entry rendered,
trimmed, split on newlines, empty lines dropped — and each resulting line is
then parsed as a **JSON object**. `.Item` in the `lane:` template is that
object.

This is a deliberate widening of §8.4's rule that every template value is a
plain string, and it is what makes `{{ .Item.id }}` and `{{ .Item.needs }}` mean
anything: a DAG node carries both an identity and its edges, and one string
cannot say both. `.Issue.Labels` is the existing precedent for structure in the
render context. **`.Loop.Item` is not changed** (task 016 decision 9); the
widening is scoped to a `fan_out` lane template and lives in
`workflow.LaneContext`, which embeds the ordinary context and adds one field.

A line that is not a JSON object blocks with `fan_out_invalid`, naming the line.
§8.4's 200-line `Result` tail applies to a list drawn from a producing step
exactly as it does for a loop.

### 2. A lane failure mid-DAG leaves earlier rounds merged — reversing 014's 21

Task 014 decision 21 said the parent "blocks and merges **nothing** — not even
the lanes that succeeded", because a partial merge is indistinguishable
downstream from a complete one. Rounds make that unachievable: round 1's merges
are on the branch before round 2 is known to fail.

The rule now:

- Block the step with `lane_failed` naming the lane, as before.
- **Keep already-merged rounds merged.** The task is `blocked`, not `done`, so
  nothing downstream consumes the branch, and which lanes are in it is legible
  from the child rows.
- Let in-flight lanes of other rounds finish — they are real tasks, and killing
  them destroys work, which is the posture §7.6 already takes on cancel.
- Spawn no further lanes.

Two alternatives were rejected explicitly. Resetting the parent branch would
make vincent destroy already-integrated commits, which §7.6's "the work is
stopped, not destroyed" refuses everywhere else. Deferring all merges to the end
would leave `needs:` as ordering with no code behind it, because a dependent
lane's worktree would no longer contain its dependencies' commits.

**The reversal is narrow.** A lane list with no `needs:` is a single round, so
nothing of its failure semantics changes; `TestFanOutFlatListIsOneRound` pins
that.

### 3. A round rides on `step_runs.iteration`

Rounds put several rows on one non-loop `step_index`/`step_id` and need a
discriminator: `stepEnv.ref()` scopes the retry budget on
`(task, step_index, step_id, iteration)`, so rounds all sitting at `iteration:
0` would collide and round 2's merge would count as round 1's second attempt.

No migration — the column exists from `0009_loops.sql`. A new `step_runs.round`
was rejected as a second near-identical discriminator every row-keyed mechanism
would then have to consider.

The audit decision 3 demanded, mechanism by mechanism:

- `stepEnv.iteration()` — returns the loop's iteration when there is a loop and
  the round otherwise, so `round` is 0 everywhere else and nothing outside a
  `fan_out` changes.
- `precedes` — compares `run.StepIndex` first and only consults the loop or
  follow-up position within one index. A `fan_out` env has neither, so it
  returns false for its own rows, which is what it did before: a step's own
  earlier attempts were never "preceding".
- `blindTo` — filters `loop`-typed rows out of `.Steps`. A `fan_out`'s row is a
  real result and stays visible; under repetition a step id resolves to its
  latest row, so `.Steps["build"].Result` is the last round's summary.
- The loop derivation's `iteration > 0` filter (`loop.go`) reads history under
  the **loop's** `step_index`. A `fan_out` never shares one — it is not valid
  inside a loop body — so no fan-out row can reach it.
- The §12.2 transcript name `{step_index}-i{iteration}-{step_id}-{attempt}` now
  distinguishes rounds, which is the behaviour wanted and is stated in §7.8.

### 4. One task record, two phases

Structured the way task 014 carried `parallel` and `fan_out`: both phases under
this record, the issue's acceptance criteria as the verification list.

### 5. Derived lanes are materialized into the snapshot at spawn

Derivation runs **once**, at spawn, in the parent's render context, and the
derived lanes are written into the task's snapshot by
`Store.SetTaskSnapshot`. `applyOverride` is the precedent for rewriting a
snapshot mid-run. After materialization the step is an ordinary static
`fan_out`, so the graph, preview, editor and `workflowdef` are correct with no
further change.

This makes `internal/taskrun` a snapshot writer outside a human action for the
first time. The sole-writer invariant holds — the task's own actor already owns
that task's rows — and `derive.go`'s header says so out loud.

Lane id uniqueness and the slug rule, load-time checks for a declared list,
become spawn-time blocking checks as well: two items can render to one id.

### 6. Bounds move to spawn time for derived lists

`fan_out.max_depth` survives unchanged — it counts nesting, and a dynamic width
does not nest, and the lane's static `workflow:` keeps `ResolveTree`'s cycle
detection and depth check meaningful. `fan_out.max_tasks` cannot be checked at
creation for a derived list, so a per-step `max_lanes:` and a spawn-time
tree-size check (`Store.FanOutTreeSize`, a recursive CTE) block with
`fan_out_limit`, parallel to §7.8's `loop_limit`. §13.4 is the precedent: it
already enforces `mcp.max_depth` and `mcp.max_tasks` at run time for this exact
reason.

A derived step counts as **one** lane against the creation-time bound, which is
an under-count by construction and is why the run-time check exists.

### 7. `needs:` is happens-after, not isolation

There is one parent branch, so a lane declaring `needs: [api, db]` also sees
`docs` if `docs` merged in the same round. Dependencies are satisfied *at
least*, never exactly, and §7.6 says so rather than emulating the alternative.
Per-lane integration branches were rejected: they buy a merge lattice — N
branches, conflicts resolved N times, a final join reconciling divergent
integrations — and buy nothing, since the deliverable is one branch and a lane
that works against `{api, db}` and breaks against `{api, db, docs}` is broken in
the deliverable too.

Merge determinism is unchanged: sequential, `--no-ff`, in `lane_order`, stopping
at the first conflict, no merge cursor persisted, git the authority (task 014
decision 9). An already-merged lane re-merges as "Already up to date", which is
what lets a crashed parent re-run a whole round.

### 8. A guarded-off lane imposes no ordering

A `needs:` naming a lane this run did not select is satisfied vacuously. A lane
that will never spawn cannot be waited for, and waiting would strand its
dependents in `awaiting_children` with nothing saying why. Guards are evaluated
on every admission, as they always were; the parent's render context does not
change while it is parked, so the answer is stable across rounds.

### 9. Scheduling posture: barrier rounds only

`ChildrenRollup.Done()` is `Settled == Total` over the whole subtree and stays
untouched, so a lane needing only `wire` still waits for its whole round.
Accepted: it is what makes "what did this lane start from" reproducible across
re-runs. Eager scheduling is #302, and it is not free — it makes a lane's
starting tree depend on timing rather than on the graph.

## Risks this work carries

- The `iteration` audit (decision 3) was the likeliest source of a silent bug:
  five mechanisms assumed `iteration > 0` meant "inside a loop". Each is
  accounted for above.
- `runJoin`'s "merge nothing unless every lane is done" is load-bearing in
  existing tests. It is kept, over **every** spawned lane rather than one
  round's, which is why the single-round case is unchanged.
- §7.6 is one of the spec's most heavily cited sections; it is amended in place
  with dated notes in this same branch.

## Not in this change

- Eager per-lane scheduling (#302).
- Drawing `needs:` edges in the TUI workflow graph and the CLI renderer. The
  new fields reach `GET /v1/workflows/schema` and the workflow-definition DTO,
  and a materialized step draws exactly as any `fan_out` does; an underived one
  draws with no lanes. `workflow.SentinelLane` is in place for the "unknown
  width" label the graph should show.
- A `scripts/m6-gate.sh` scenario driving a derived DAG end to end.
