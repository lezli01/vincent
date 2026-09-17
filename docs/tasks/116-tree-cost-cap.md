# 116 — `max_tree_cost_usd`: a cost cap across a whole fan-out tree

**Status:** ✅ done (6/6)
**Opened:** 2026-09-17
**Issue:** [#409](https://github.com/lezli01/vincent/issues/409)
**Renumbered:** opened as 115; task 115 reached `master` first
([115](115-scheduled-daemon-backups.md)), so this record, its task IDs and
every citation of them are 116.
**Spec:** amends §7.6, §12.3, §13.2, §15, §17, §18

## Problem

Task 033's `max_task_cost_usd` caps what **one task row** spends. A `fan_out`
lane is its own row (§7.6, 014 decision 1), so a tree may spend lanes × cap
before any row trips, and the parent's own rollup never sees a lane's spend.
Task 033 decision 1 deferred a per-tree cap as "layerable later against the
same enforcement point" and named its two open questions: a recursive rollup
over `parent_task_id`, and which task blocks when the tree total trips.

This work answers both and adds the tree cap at the enforcement point 033
built. It relitigates no recorded decision: 033 decisions 2–6 and 096 decision
18 stand as written.

## What shipped

**A tree** is a root task (no `parent_task_id`) plus every descendant at any
depth. A task that never fans out is a tree of one. The tree total is the §17
rollup of `cost_usd` over every step run of every task in the tree: retries,
repair runs, follow-up rounds and the lanes a follow-up round spawns all count.
Archived descendants count too, the same way `store.ChildrenOf` counts them. It
is a lifetime total and never resets.

A new top-level config key, `max_tree_cost_usd`, is compared against that total
at every attempt boundary. The task whose attempt took the tree over blocks with
a new `tree_cost_limit` reason. The §13.2 `children` rollup gains `cost_usd`, so
a parent shows what its lanes spent, and the TUI's detail view turns that into a
`tree cost` fact.

## Decisions

### 1. The cap is a config key only: `max_tree_cost_usd` (2026-09-17)

It is a top-level float next to `max_task_cost_usd`. `0` (the default) is off,
a negative value fails the load, and it is read per check, so hot reload
reaches running work. "Raise the cap and retry" therefore stays the remedy.

There is no create-time field on `POST /v1/tasks`, no `vincent task add` flag,
no trigger `limits:` key, no workflow field and no migration.

**Beat:** a workflow field, which would reverse 033's "a budget is not
something a step inherits". A create-time field was not rejected, only not
needed yet: it can be added later against the same check, the way 096 decision
18 added one to 033.

### 2. The tree cap is independent of the per-task cap (2026-09-17)

The two caps measure different quantities, so the tree cap is not folded into
the "lower of" that 096 decision 18 built for the per-task pair. Both are
checked at the same boundary. When both are over at once, the task blocks
**`cost_limit`**: the narrower cap is the one raising the tree cap cannot clear,
and existing `cost_limit` behaviour and tests stay unchanged.

This precedence was not put to the author. It is the conventional default and
is recorded here so it can be challenged in review.

**Beat:** extending 096 decision 18's "lower of" to the tree cap. That rule
compares two caps on the same figure, one task's rollup. The tree cap is
compared against a different figure, so there is no single number to take the
lower of.

### 3. A new block reason: `tree_cost_limit` (2026-09-17)

It is a separate `taskrun.ReasonTreeCostLimit` constant, not a reuse of
`cost_limit`, because the fix differs: raise a different key, and usually retry
on the parent. The blocked task gets that reason. Its log line names the root
id, the tree total and the cap.

**Beat:** reusing `cost_limit`. The reason is what tells a reader which remedy
applies, and here the remedies differ.

### 4. The task whose attempt pushed the tree over blocks, and only that one (2026-09-17)

That is usually a lane, but it can be the parent: the parent's own steps before
the fan-out, after the join, or a `merge.on_conflict: agent` run are attempts in
the tree too. A parent parked in `awaiting_children` makes no attempts, so it
never blocks this way while parked. Its join stays open because a lane is
`blocked`, which §13.2's `children.blocked` already shows.

**Beat:** blocking every unsettled task in the tree at once. A task's actor is
the sole writer of that task's state, so the actor at the boundary can block
only its own task. Each sibling learns the tree is over at its own next
boundary, which is where decision 6's overshoot comes from.

### 5. The parent's rollup shows why (2026-09-17)

The §13.2 `children` rollup gains `cost_usd`: the descendants' summed spend,
`null` when no descendant reported any cost. It covers the API DTO, `apiclient`
(`ChildrenRollup.CostUSD *float64`), and a TUI detail fact on tasks with
children. The fact shows the **tree** figure, the task's own `cost_usd` plus
`children.cost_usd`, and renders `—` when neither side reported a cost, never
`$0.00` (033 decision 5). The board rows are unchanged.

The sum is a store query of its own and stays out of `ChildrenOf`'s settle
query, which the scheduler's re-queue path uses and which should not pay for a
`step_runs` join it never reads.

### 6. Enforcement stays at attempt boundaries only (2026-09-17)

The check runs after an attempt finishes, as 033 decisions 2, 4 and 6 require.
It does not also run before an attempt or at spawn. The overshoot is therefore
**at most one attempt per task still working in the tree** when the cap trips:
every lane that is running or queued finishes one attempt, then blocks at its
own boundary. The docs say this plainly instead of claiming 033's "one attempt".

The same rule makes a `retry` on a `tree_cost_limit`-blocked lane buy one
attempt of progress. A cascade `retry` on the parent (task 090) re-admits every
blocked lane, so each makes one attempt per press. Both are documented.

**Beat:** also checking before an attempt or at spawn. That would narrow the
overshoot, but it adds a second enforcement point beside the one 033 decisions
2, 4 and 6 settled, and a second place for 033 decision 2's propagation rule to
be missed.

### 7. `HasCost` guards the tree the way it guards a task (2026-09-17)

The check is inert unless some step run in the tree reported a cost. A tree
mixing claude with codex or cursor lanes counts only what was reported. The docs
state this undercount; it is never estimated from token counts (033 decision
5).

### 8. Out of scope: lanes do not inherit `restricted` or the task's own cap (2026-09-17)

`laneTask` (`internal/taskrun/fanout.go`) copies neither the parent's
`restricted` nor its own `max_task_cost_usd` to a lane. A task created
`restricted: true` (every triggered task by default) that fans out therefore
runs its lanes' agent steps full-auto. That breaks task 096 decision 17's
"forces every agent step to restricted", and `RestrictedMismatch` does not look
into lanes either.

It is a security gap and is to be filed as a separate bug issue rather than
wait for #409's delivery slot. Nothing here depends on it, and this work
deliberately does not fix half of it.

## Tasks

- [x] **116.1** `internal/config`: `MaxTreeCostUSD float64`
  (`max_tree_cost_usd`) beside `MaxTaskCostUSD`, zero by default, non-negative
  validation in the neighbours' shape, the commented key in `bootstrap.go`, and
  the key in the edit path. ✓ 2026-09-17
- [x] **116.2** `internal/store`: `TreeCost`, which climbs from any task to its
  root and sums `step_runs.cost_usd` over the root's whole subtree, archived
  rows included, and `DescendantsCost` for `children.cost_usd`. Both return a
  `CostRollup` carrying `HasCost`, and both stay out of `ChildrenOf`.
  ✓ 2026-09-17
- [x] **116.3** `internal/taskrun`: `ReasonTreeCostLimit`;
  `stepOutcome.costLimit` carrying the reason instead of 033's `costExceeded`
  bool, so every propagation branch in `runSteps`, `group.go` and `loop.go`
  covers both caps by construction; `overCostCap` returning which cap fired,
  per-task first, with the tree read only when the key is set; and `repair.go`'s
  comment extended to the tree cap. ✓ 2026-09-17
- [x] **116.4** Surfaces: `max_tree_cost_usd` on the `GET`/`PATCH /v1/config`
  DTOs, `apiclient.Config`/`ConfigPatch`, `vincent config` and the TUI config
  editor (`max tree cost`); a daemon-view cap line shaped like the per-task one,
  `off` for zero; `children.cost_usd` through `internal/api` and `apiclient`,
  and the TUI detail `tree cost` fact. ✓ 2026-09-17
- [x] **116.5** Tests: engine tests against `cmd/fakeagent` in
  `costlimit_test.go`'s style (a lane crossing blocks while its row keeps its
  state and no retry is consumed; off, generous and exactly equal; the parent's
  own pre-fan-out spend and a post-join block on the parent; a grandchild
  counting toward the root; `cost_limit` winning a tie; beating a failure with
  retries left and a `retry_backoff` hold; firing mid-`loop` and in a
  `parallel` group; inert and mixed trees; a lane retry and a cascade retry each
  buying one attempt; a raised cap through reload); store rollups; config load,
  default, rejection and edit round trip; API, `apiclient` live, TUI daemon view
  and detail fact. ✓ 2026-09-17
- [x] **116.6** §7.6, §12.3, §13.2, §15, §17 and §18 amended, dated; task 033
  decision 1 points here; `configuration.md`, `task-lifecycle.md`,
  `troubleshooting.md`, `api.md`, `workflows.md`, `agents.md`, `tui.md`,
  `triggers.md`, `features.md`, `faq.md`, `security-model.md`,
  `skills/vincent-triggers/SKILL.md` and `CHANGELOG.md` updated. ✓ 2026-09-17

## Noted, deliberately not folded in

- **No gate script**, for 033's reason: this is engine behaviour with a config
  input, which `internal/taskrun`'s tests prove directly.
- **No checklist line** in `update-workflows` or `update-triggers`. This is a
  config key, not a workflow or trigger feature, so neither version-coupled
  checklist gains one. `skills/vincent-triggers/SKILL.md` names the key only
  where it already advised a tight `limits.max_task_cost_usd`.
