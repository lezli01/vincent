# 090 — A retry on a parked fan-out parent cascades to its blocked lanes

**Status:** ✅ done (5/5)
**Issue:** #328
**Amends:** §6 — the `awaiting_children` row's "Cancel is the only human action",
the `retry` row's valid-from set and the `edit + retry` row's refusal; §7.6's
"Resuming" bullet, where a blocked lane's resolution is now one retry at the
parent; §13.2 and `docs/reference/api.md` for the `/retry` states and the
`retried_descendants` field
**Amends in scope, without relitigating:**
[014](014-workflow-fan-out.md) decision 22 — its remedy ("fix the child, then
retry the parent") remains the truth for an **`aborted`** lane, because nothing
here re-admits an aborted task, and its beat (retry re-spawning aborted lanes as
fresh children, which `lane_id` cannot version) stays refused. What changes is
that the parent's retry now also re-admits the **blocked** children decision 22
never considered
**Keeps, without relitigating:** [014](014-workflow-fan-out.md) decision 20 —
`blocked` is still not `Settled`, so the join still never proceeds over a blocked
lane. This changes who issues the retry, not what the join will merge

A `fan_out` lane that blocked was a dead end for the tree above it. A blocked
task is not settled, so `Scheduler.resumeSettledParents` never returned its
parent to the queue; the parent sat in `awaiting_children`, and §6 offered
exactly one human action from there — `cancel`, which destroys the work. `retry`
on the parent was a `409`. Recovery was a manual walk of the lane tree, once per
lane and once per level down to `fan_out.max_depth`, with nothing in the product
to walk it for you.

The engine already behaved correctly under a cascade, which is what made this
cheap. A parent re-admitted while its lanes are unsettled does not join and
fail: under barrier scheduling `parkForUnsettled` parks it again, and under
`schedule: eager` `mergeSet` reads an unsettled lane as "not yet". So
re-admitting lanes beneath a parked parent converges with no new engine logic,
whichever order the scheduler happens to run things in — which is why the whole
change is one state-table row, one cascade walk, and the plumbing that carries a
count out to the clients.

## Tasks

- [x] **090.1** The state machine and the engine: `Retry` becomes legal from
  `awaiting_children`, and `Runner.Retry` grows the parked branch and
  `cascadeRetry`.
- [x] **090.2** `POST /v1/tasks/{id}/retry` answers 200 from `awaiting_children`,
  reports `retried_descendants`, and refuses `branch_override` there.
- [x] **090.3** The clients: `apiclient.Retry` carries the count, the TUI offers
  `r` on a parked parent and stops offering `E` where it is now a 400, and the
  CLI and the action bar say how many lanes were re-admitted.
- [x] **090.4** `scripts/m6-gate.sh` scenario 12: two lanes blocked on one
  missing repository setting, one retry on the parent, both merged.
- [x] **090.5** The spec, the API/CLI/TUI/lifecycle pages, the workflows guide
  and the changelog.

## Decisions

### 1. The parked parent's row is not written at all

*2026-09-05.* The §6 table gains `Retry: {AwaitingChildren: {To:
AwaitingChildren}}` so that `taskstate.Can` is true — the API answers 200
instead of 409 and `available_actions` lists `retry` — but `Runner.Retry`
branches on `awaiting_children` **before** `transitionFrom` and only cascades.
No compare-and-swap, no `task.state_changed` whose `from` and `to` are equal,
and above all no `retry_cursor_at` stamp.

The reason is `Repair`'s, and it is stated verbatim in the code: nothing was
retried on *this* task, and stamping the cursor would silently hand the join a
fresh §7.2 budget nobody asked for. Skipping the CAS loses nothing else —
`applyAction`'s pending-pause clear is moot here, because `pause` is not valid
from `awaiting_children`, so there is never one to clear.

The self-transition in the table is therefore a **legality marker**, not a swap
any caller performs, and it says so in a comment beside the row.

**Beat:** a real `awaiting_children → queued` transition on the parent. It would
re-admit the parent, which then parks again after a full admission — work,
events and a step run for nothing, and a `retry_cursor_at` that lies.

### 2. The `blocked` path keeps everything it had, and cascades after it

*2026-09-05.* A `blocked` parent still takes its own `blocked → queued` swap and
its cursor stamp, exactly as before, and the cascade runs after the transition
has committed — the shape `Cancel` already uses with `cascadeCancel`. Its error
is logged rather than returned: the parent's own retry has already committed, so
reporting a failure would tell the caller the opposite of what happened. On the
parked path there is nothing else to report, so the cascade's error **is** the
call's error.

Both shapes cascade because both are reachable: a parent blocked for its own
reason can have a blocked descendant under it — an eager or DAG fan-out blocked
on `merge_conflict` in round 1 with a round-2 lane blocked, or a lane set mixing
one aborted and one blocked lane.

This replaces the rationale in the issue's fourth bullet, which claimed
cascading from `blocked` "stops the `lane_failed` case needing the lanes fixed
first". That is wrong: `lane_failed` is raised by `mergeSet` only for a lane that
settled **without** finishing — `aborted` or `archived` — and `retry` is illegal
from `aborted`, so the cascade cannot touch it. The bullet is kept; its reason is
replaced by the case that is actually reachable.

### 3. Retry gets its own response type, carrying a count and not ids

*2026-09-05.* `runAction`'s shared task body has no room for a second thing to
say, so `handleTaskRetry` stops using it and writes the task fields plus
`retried_descendants` — the way `/repair` already leaves `runAction` to carry
its catalog warnings. The field is **always** present, `0` for an ordinary
blocked retry with nothing under it, so a client never has to tell "no cascade"
from "an old daemon".

A count and not the ids: §13.3's convention is that a client re-fetches what it
decides it needs, and the ids under a wide fan-out are unbounded.

### 4. Every override is refused from `awaiting_children`, with a 400

*2026-09-05.* A parked parent's cursor is on a `fan_out` step, which has no
prompt and no command to rewrite, and `branch_override` would rename the branch
every live lane holds as its own `base_branch`. A typed error from
`Runner.Retry` (`ParkedOverrideError`) covers `prompt_override` and
`run_override`; `branch_override` is refused in `handleTaskRetry` **before**
`renameBranchForRetry` runs, because that rename is committed ahead of the
action and a refusal afterwards would leave the branch moved.

The TUI's `E` gains the state check its own hint line already applied, so
edit+retry is not offered where the daemon would answer 400.

### 5. Cascaded children skip the task-013 `on_input: require` re-check

*2026-09-05.* `checkRetryInput` lives in the API handler and applies to the
addressed task only. A child that would block `input_unsupported` re-blocks on
its own row, which is the issue's stated posture for every non-quota reason and
is exactly what retrying that lane by hand does today. A parked parent passes the
gate as a matter of course: it reads `on_input` off `agent` steps, and the
parent's cursor is a `fan_out`.

### 6. The walk is `ChildrenOf`'s existing recursive CTE, sorted

*2026-09-05.* No store change and no migration. `store.ChildrenOf` already
returns the `Blocked` set for the whole subtree at any depth in one recursive
CTE, which is exactly the set to re-admit — nested fan-outs included, with no Go
recursion and no new query. The ids are sorted before the walk because the CTE
has no `ORDER BY`, and a deterministic order is something a gate can assert.

A descendant that is itself `awaiting_children` is deliberately absent from that
set and is left parked: it needs no help, since a re-admitted lane beneath it
parks it again (barrier) or reads as "not yet" (eager). A lane that leaves
`blocked` between the rollup and the write is skipped on its
`InvalidActionError` — it moved, which is not an error for the human who asked —
and the count excludes it.
