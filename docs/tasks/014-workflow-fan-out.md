# 014 — Parallel steps and workflow fan-out

**Status:** 🚧 in progress (5/14) · **Opened:** 2026-08-17

Two features that let one task do several things at once, shipped in that order.

**Phase 1 — `type: parallel`.** Steps that run concurrently in the task's one
worktree. No branch, no child task, no merge:

```yaml
steps:
  - id: verify
    type: parallel
    max_parallel: 4
    steps:
      - { id: test,      type: command, run: go test ./... }
      - { id: lint,      type: command, run: go run mage.go lint }
      - { id: typecheck, type: command, run: go vet ./... }
```

**Phase 2 — `type: fan_out`.** Lanes become real child tasks, each with its own
worktree and branch, merged back into the parent's branch at the end of the same
step:

```yaml
steps:
  - id: build
    type: fan_out
    merge: { on_conflict: block }
    lanes:
      - { id: api,  workflow: implement-module, fields: { module: api } }
      - { id: docs, steps: [ { id: write, type: agent, prompt: "Document the API." } ] }
```

One branch is still delivered at the end, because the fan-out step does not
finish until every lane is merged into the branch the task already owns.

## The problem

Spec §7 opens with "steps execute strictly in order", and §20 lists "workflow
branching/conditionals, parallel steps, and step fan-out" as explicitly out of
v1. That was the right call for v1 and is the wrong one now, for two different
reasons that this document deliberately keeps apart:

- **Verification is embarrassingly parallel and currently serial.** A workflow
  that runs tests, then a linter, then a type check pays the sum of three
  independent waits. Nothing about them interacts; they read the worktree and
  report an exit code.
- **Some deliverables are several disjoint pieces of work.** Implementing two
  modules that do not touch each other, or writing code in one place and docs in
  another, is one deliverable whose parts have no reason to wait for each other —
  but a single task has one worktree, one branch, and one cursor, so they do.

The second one is where all the difficulty lives, because parallel *mutation*
has to converge: several branches must become one branch, and merging can
conflict. The first needs none of that machinery and is most of the wall-clock
win, which is why the two ship separately.

What this is **not** solving: running several candidate solutions and picking
one. That discards work rather than merging it, and designing the merge around a
case that never merges would contort it (decision 15).

## Decisions

### 1. The unit of parallel mutation is a child task, not a lane inside a task

*2026-08-17.* A `fan_out` step creates real child tasks. Each child is an
ordinary task in every respect: its own row, worktree, branch, scheduler slot,
gates, blocks, transcripts, recovery, `gc` and `doctor` coverage. Its
`base_branch` is the parent's `branch_name` — an existing column, set the way
any other task's is.

**Beat:** parallel lanes *inside* one task, each with a sub-worktree. That
version has to re-cut nearly every invariant in CLAUDE.md at once:
`tasks.current_step` is one `INTEGER`; `worktree.Manager.Path(taskID)` is one
directory per task id and `BranchName(taskID, …)` one branch; the `taskrun`
actor is the **sole** writer of a task's state and step_run rows, so N lanes
means N writers or one writer multiplexing N step clocks; §6 has one state per
task, and there is no answer to what a task with one blocked lane and two
running lanes *is*; the board is one row per task; recovery finalizes one
running step.

Child tasks pay for none of that. The new machinery is a parent↔child link, the
spawn, and the merge — everything else is inherited. The honest cost is that N
children consume N scheduler slots, so a fan-out competes for the caps like any
other work: **a fan-out is not a way to exceed your caps, it is a way to fill
them.** That belongs in the user-facing docs, not in a footnote.

### 2. Two step types, not one vocabulary

*2026-08-17, retracting an earlier position taken in the same design session.*
The design started from "two features, one YAML vocabulary." That does not
survive contact: unifying them means one step type whose lanes contain steps
that contain lanes, i.e. a recursive schema in the shared path, to express two
mechanisms that share concepts and nothing else. `parallel` runs processes in
this task's worktree; `fan_out` creates tasks. They differ in what they produce,
what bounds them, what failure means, and whether git is involved at all.

So: `type: parallel` (inline steps, shared worktree, no merge) and
`type: fan_out` (lanes, child tasks, merge). Recursion is confined to `fan_out`'s
own `lanes:`, where it is inherent (decision 5) rather than imposed.

### 3. Fan-out and join are one step, not two

*2026-08-17.* `fan_out` spawns the lanes **and** merges them; `merge:` and
`on_conflict:` are its fields. There is no separate `join` step type.

The requirement is that one branch is delivered. A `fan_out` without a join
would be a workflow whose behaviour we would have to define and then advise
nobody to write, and steps authored *between* a fan-out and its join could not
run anyway — the parent holds no slot while its children work (decision 6). One
step also keeps `current_step` semantics exactly as §7 describes them and gives
recovery a single place to re-enter (decision 9).

### 4. A lane is a named workflow or inline steps, and the whole tree is snapshotted at creation

*2026-08-17.* A lane carries either `workflow:` (a registry name, resolved
through the usual builtin < global < project shadowing) or inline `steps:`.
Exactly one, enforced the way `rejectFields` already enforces per-type fields.
Either way the lane's steps are resolved and written into the **parent task's
snapshot at task-creation time**, and flattened into the child's own flat
snapshot when the child is created.

Resolving at fan-out time instead was rejected outright: §5.3 says execution
always uses the snapshot precisely so later edits to workflow files never mutate
in-flight tasks, and a lane read from the registry hours into a run would be
exactly that mutation, in the one place nobody would look for it.

Two things fall out of flattening at spawn. A child is indistinguishable from a
hand-created task, so `edit + retry`, `Marshal`, and the `locator` never meet a
nested workflow — **nesting exists at authoring time only**. And an inline lane
needs a `workflow_name` for its child's NOT NULL column: it gets
`{parent_workflow}/{step_id}/{lane_id}`, which provably cannot collide with a
registry name, because `validate` already rejects `/` in `Workflow.Name`.

Nested paths cost nothing in error reporting: `locator.line` builds
`yaml.PathString("$." + path)` and `parentPath` already walks both `.` and `[`
segments, so `steps[2].lanes[0].steps[1].prompt` resolves with no change to that
code. The recursion cost is in `validate` and `Marshal`.

### 5. Nesting to any depth, bounded by configuration and checked at creation

*2026-08-17.* A lane's workflow may itself contain a `fan_out` step, to any
depth. A flat one-level restriction was considered first and rejected by the
author of the work: composition is the point of naming a workflow as a lane, and
a rule that a workflow may be used as a lane only if it never fans out makes
reusable workflows silently unreusable.

What that buys has to be paid for, and the payment is that a cheap parse-time
ban is replaced by three creation-time checks:

- **Cycles.** Workflow A naming B as a lane while B names A is an infinite
  spawn. Detected during the decision-4 snapshot resolution with a visited set
  over resolved workflow identity, `400` naming the path (`build → verify →
  build`). Registry load emits a §8.2 **warning** only — a cycle between two
  files is real only once a task picks a root, and shadowing decides which files
  those even are.
- **`fan_out.max_depth`** (config, default 3) and **`fan_out.max_tasks`**
  (config, default 64), both `400` at creation.

The property that makes those checks possible is worth protecting: **the whole
tree's shape is static at root creation.** Lane lists live in the snapshot, and
a `for_each` list comes from fields that the parent's lane spec set, which are
themselves in the snapshot. So the tree is computable in the insert path, and a
depth-3 explosion is a `400` in front of the person typing rather than 200
worktrees discovered six hours in. Any future feature that makes a lane count
non-static carries the run-time check as its own cost.

Note where the bound meets the decision: depth is **unlimited by design and
bounded by a config default**. Deeper trees are a config edit, not a code change.

### 6. `awaiting_children` is a new state, not a reuse of `awaiting_gate`

*2026-08-17.* §6 gains `awaiting_children`, holding **no** slot. New table rows:
`FanOut: {Running → AwaitingChildren}`, `ChildrenSettled: {AwaitingChildren →
Queued}`, `Cancel: {AwaitingChildren → Aborted}`.

A parent must sit in a persisted, slot-free state because the actor invariant
says a gate, a block or a pause releases the slot and **ends the goroutine**. A
parent that kept its goroutine alive waiting on children would hold a slot for
hours doing nothing, which is the exact starvation `awaiting_gate` exists to
avoid.

**Beat:** reusing `awaiting_gate`. It would make `HumanActionsFrom` offer
**approve / reject / skip** on a task whose children are still running — the API
would `200` a meaningless action and the TUI would render a button that lies.
The states differ in what a human can do about them, which is the only thing §6
is for.

No `Pause` row is added. Pause is valid from `queued` and `running` today, and a
parent that owns nothing running has nothing to pause; children are ordinary
tasks and pause individually.

**This cannot deadlock at any depth.** A parent releases its slot *before* its
children need one, so there is no hold-and-wait anywhere in the chain, under any
cap, at any nesting depth. That is a consequence of the state holding no slot,
and it is the reason this design tolerates unbounded nesting at all.

### 7. Sequential `git merge --no-ff` in declared lane order

*2026-08-17.* The join merges each lane branch into the parent's branch one at a
time, in `lane_order` — the order the lanes are declared — stopping at the first
conflict. Message `Merge lane '{lane_id}' of task {child_id}`. Git identity is
whatever the user's own config says; vincent runs as the invoking user (§16) and
has no business inventing an author. No attribution or co-author trailer.

**Beat (a):** an octopus merge (`git merge A B C`). It refuses outright when any
pair conflicts, producing one unresolvable failure instead of a sequence that
can be resolved lane by lane — the opposite of what decision 8 needs.

**Beat (b):** rebasing each lane onto the parent. It rewrites lane history,
which breaks a branch a human may already be reviewing, and forces the same
conflict to be resolved once per commit.

Declared order rather than completion order is what makes a re-run conflict
identically, which decision 9's idempotent recovery depends on. `--no-ff` keeps
each lane visible in history and matches the repo's own no-squash convention.

### 8. A conflict blocks by default; agent resolution is opt-in

*2026-08-17.* A conflicting merge stops the task with the new block reason
`merge_conflict`, joining the shared `Reason*` vocabulary, and leaves the
worktree conflicted so a human resolves in place. `on_conflict: {agent: …}` opts
into an agent attempt first, gated by a `check` the way any agent step is, and
falls back to the block when the check fails.

Blocking by default is §7.2's posture applied unchanged: retries are for
failures a retry can fix, and a human decides what a machine could not. The
alternative default — an agent silently resolving a semantic conflict and the
merge commit landing unread — is the one outcome that turns a time-saving
feature into a correctness liability.

Archive gets **no special case**. A conflict-blocked worktree is dirty by
construction, and archive already refuses a dirty worktree without explicit
confirmation (§10). That is the correct behaviour here; §18 says so rather than
the code carving an exception.

### 9. Re-entry is disambiguated by the task's state, with no merge cursor persisted

*2026-08-17.* Two very different things re-enter a half-merged join, and only
one of them may run `git merge --abort`:

- **`interrupted`** — a crash between lane 2 and lane 3. Recovery aborts if
  `MERGE_HEAD` exists, then re-merges **every** lane from the top. Already-merged
  lanes are "Already up to date" no-ops, so the whole step is naturally
  idempotent.
- **`blocked` with `merge_conflict`** — a human who has just resolved by hand and
  hit retry. If `MERGE_HEAD` exists and the index is clean, commit the merge and
  continue with the remaining lanes. If the index is still conflicted, block
  again with the same reason.

**Beat:** persisting a merge cursor (which lanes are merged) on the step run.
The task's own state already distinguishes the two cases, and a stored cursor is
a second copy of a fact git holds authoritatively — one that can disagree with
the repository after a manual `git merge --abort` on the user's part. Deriving
beats storing here for the same reason it does in decision 13.

The failure this pair exists to prevent is specific and expensive: recovery
running `--abort` over a conflict a human spent an hour resolving.

### 10. Overrides and priority inherit down the whole tree

*2026-08-17.* A root's `agent_override` / `model_override` / `effort_override`
and its `priority` propagate to every descendant, with a lane spec free to
override for its own subtree.

A task-level override means "this piece of work", and a fan-out is one piece of
work spread over many rows; an override that silently stopped at depth 1 would
be the surprising reading. Priority inheritance is the load-bearing half:
admission is `priority DESC, created_at ASC` and descendants are created *late*,
so priority-0 children of a priority-5 root would queue behind unrelated work
and stall the root indefinitely — a fan-out would make an urgent task slower
than not fanning out at all.

### 11. Cancel cascades, archive refuses then cascades, branches are kept

*2026-08-17.*

- **Cancel** on a parent in `awaiting_children` cascades depth-first to every
  non-terminal descendant. Nothing should keep burning agent time for a join
  that will never happen. Branches and worktrees survive the cancel, so no work
  is destroyed — only stopped.
- **Archive** refuses while any descendant is non-terminal, then cascades, with
  the same dirty-worktree confirmation applied per child that §10 already
  requires.
- **After a successful join**, children reach `done` on their own — the join
  does not touch their state — and their branches are kept. §10's rule is
  unchanged, and task 008's empty-branch deletion applies at archive exactly as
  it does for any task.

The cost is real and goes in the docs plainly: an N-lane fan-out leaves N
worktrees on disk until someone archives them. That is precisely the pressure
`vincent gc` (task 005) and `vincent doctor` (task 006) were built for, but it
will be felt, and a user who fans out routinely will meet it before they meet
anything else in this feature.

### 12. `max_parallel` bounds a `parallel` group; `fan_out` gets no knob of its own

*2026-08-17.* Phase 1 needs a bound and phase 2 does not, for a reason worth
stating rather than assuming.

A `parallel` group runs K processes **inside one task's slot**, so the global
and per-project caps — which count *tasks* in slot-holding states (§11) — do not
see them. `max_parallel:` on the group (config default 4) is therefore a genuine
second concurrency dimension, and the spec should admit that instead of leaving
a reader to discover that one task can saturate a machine while the caps read 1.

`fan_out` children are ordinary queued tasks that the existing caps govern
exactly as they govern everything else. A third knob there could only ever make
things slower than the caps the user already set, and decision 5's `max_tasks`
is the real protection against explosion.

### 13. Descendants are excluded from the task list; the rollup is derived, never stored

*2026-08-17.* `GET /v1/tasks` returns `parent_task_id IS NULL` by default, with
`?parent_id=` for one parent's lanes and `?include_children=true` for the flat
everything. `GET /v1/tasks/{id}` on a task in `awaiting_children` carries a
`children` rollup: subtree counts by state plus the ids of blocked and
awaiting-gate descendants, computed by one recursive CTE over `parent_task_id`.

The board is a list of the work you asked for; a 64-task tree would bury it, and
task 009's project → workflow grouping would fragment across synthetic lane
workflow names. The cost of hiding descendants is that a blocked lane is
invisible in `vincent task list` — which is exactly what the rollup pays for,
and why the two decisions are one decision.

Derived rather than denormalized because a stored counter is a second truth that
drifts, and the daemon having one truth is the property this codebase is built
on. The recursive CTE is bounded by `max_depth`.

### 14. `task.children_changed`, emitted on every fan-out ancestor

*2026-08-17.* A new **durable** event type, payload `{task_id, child_id,
to_state}`, emitted on each fan-out ancestor when a descendant transitions.
Clients re-fetch the rollup — ids, not objects, the way §13.3 does everything
else.

This exists because the per-task SSE stream filters on `task_id`, so a root's
stream never sees a depth-2 transition and its rollup cannot update live. It is
also a correction to a position taken earlier in the design session, that
`task.state_changed` would cover this on the PR D precedent: it does not, and
the difference is a TUI that has to poll.

**Beat:** widening the per-task stream filter to a subtree membership test. The
subtree is not fixed at subscribe time — children appear as fan-outs fire — so
the subscription would have to grow itself by watching `task.created` inside the
post-commit fan-out. Emitting keeps the filter exactly `task_id = ?`. The cost is
bounded and explicit: at most `max_depth` extra event rows per descendant
transition.

### 15. Non-goals, recorded

*2026-08-17.*

- **Competing candidate lanes** — N attempts at the same problem, pick one. It
  discards work rather than merging it, and shaping the merge around a case that
  never merges would contort every decision above.
- **Run-time dynamic fan-out** — an agent deciding mid-run that there are seven
  subtasks. It breaks the snapshot-as-authority rule (decision 4) and makes step
  indices unstable across a crash, and it is what decision 5's creation-time
  checks trade away.
- **Policing whether a `parallel` step writes files.** The group shares one
  worktree; concurrent writes are undefined behaviour, documented as such. §10
  already states that worktrees isolate working trees, not process-level
  resources — faking a sandbox here would be the same dishonesty as faking an
  adapter capability.

---

Decisions 16 onwards come from the delivery-planning session held the same day,
which walked the design against the code rather than against itself. They are
the questions decisions 1–15 left open — several of them only visible once the
existing store, FSM and validation code was read.

### 16. Sub-steps share the group's `step_index`, and three keyed mechanisms widen to take a `step_id`

*2026-08-17.* 014.2's "one `step_runs` row per sub-step sharing the group's
`step_index` and keyed by `step_id`" collides with three mechanisms that key on
`(task_id, step_index)` alone: `store.CountStepAttempts` (attempt numbering),
`store.LastFailedStepRun` (which seeds `.LastFailure` for the §8.4 failure
block), and transcript filenames `{step_index}-{attempt}.jsonl`. The two
queries take a step id; transcripts take one **only for a sub-step**, which
becomes `{step_index}-{step_id}-{attempt}.jsonl`.

*Amended during implementation, 2026-08-17:* the transcript rename was written
here as unconditional. Applying it only where it is needed leaves §12.2 true
for every workflow that does not use a group, and confines a path change to
the case that actually collides. Step ids are slugs, so nothing in a name can
escape the transcript directory.

**Beat (a):** giving each sub-step its own `step_index`. `tasks.current_step` is
one `INTEGER` and the group must stay addressable as one step — this is
decision 1's trap in miniature, re-cutting an invariant to avoid widening two
queries.

**Beat (b):** a `sub_step_id` column keyed as a triple. It stores what the
existing `step_id` column already holds.

This extends rather than overturns the phase 2 decision recorded at
`store/transitions.go:301`: attempt numbers stay monotonic over a step's whole
history, now per sub-step, so transcript files still never collide or truncate.

### 17. A `parallel` group has no `step_runs` row of its own

*2026-08-17.* Only sub-steps get rows. The group's state is derived: it
succeeded iff every sub-step succeeded, it is running while any sub-step is.

**Beat:** a group row carrying its own state and timestamps. It is a second copy
of a fact the sub-step rows already hold — the same reason decisions 9 and 13
derive rather than store. The TUI folds sub-steps under their group on
`step_index`, which it already has.

### 18. A failing sub-step does not cancel its siblings; the retry budget is per sub-step

*2026-08-17.* Siblings run to completion, then the group fails. `max_retries` on
a sub-step governs that sub-step, falling back to the group's, and a retry
re-runs only the failed sub-step (014.2).

**Beat:** cancelling siblings at the first failure. It discards work that was
about to succeed, and it does so in the most expensive way available: a killed
sub-step records an `interrupted` attempt, which consumes no retry (§7.2), so
the retry re-runs it from scratch. A failed linter should not throw away a test
run whose output is the thing the human wants to read.

### 19. `on_input: require` is rejected inside a `parallel` group

*2026-08-17.* Validation rejects it alongside `manual` (014.1). `check`,
`timeout` and `max_retries` are legal per sub-step, and the group itself may
carry a `timeout` bounding the whole group.

The reasoning 014.1 gives for `manual` applies unchanged: `awaiting_input` is
one task state carrying one pending request (§7.4), and there is no state
meaning "one sub-step of one group is waiting on a human" any more than there is
one meaning "one sub-step is gated". Task 013 made this reachable, so it needs
saying.

**Beat:** serializing input requests from N concurrent agents. It defines an
answering order nobody asked for and leaves the other agents idle holding the
one slot.

### 20. `Settled()` is a new predicate; `Terminal()` is left alone

*2026-08-17.* Decision 6 says the parent re-queues when the last descendant
reaches "a terminal state". Read against the code that is wrong:
`taskstate.Terminal()` is `archived` **only**, which no child reaches on its
own. The join waits on a new `Settled()` = `done | aborted`.

A `blocked`, `awaiting_gate` or `paused` child therefore holds the join open —
which is what decision 13's rollup exists to surface, and the two decisions
should be read together.

**Beat (a):** widening `Terminal()`. It means "no further transition is
possible", and for a `done` task archival is still possible, so the widening
would be a lie told to every other caller.

**Beat (b):** treating any slot-free state as settled. It wakes the parent over
a blocked lane and merges a branch missing that lane's work, with nothing in the
result saying so.

### 21. A lane that settles as `aborted` blocks the join with `lane_failed`

*2026-08-17.* New block reason `lane_failed` in the shared `Reason*` vocabulary.
The parent blocks and merges **nothing** — not even the lanes that succeeded.

This is decision 8's posture applied to the other way a fan-out fails to
converge. Merging the successful subset delivers an incomplete deliverable on a
branch that looks finished, which is the same correctness liability decision 8
refuses for conflicts.

**Beat:** merging what succeeded and reporting the rest. A partial merge is
indistinguishable, downstream, from a complete one.

### 22. `retry` on a `lane_failed` parent re-checks; `skip` keeps its existing meaning

*2026-08-17.* Retry re-evaluates every lane's state. If any lane is not `done`,
it blocks again with `lane_failed`. The remedy is to fix the **child** — an
ordinary task, with the ordinary retry — and then retry the parent.

`skip` on the parent is unchanged and skips the whole join: no lanes are merged.
It is deliberately **not** a "proceed without that lane" button, and there is no
such button. Anything else would need a merge subset to be expressible, which
decision 21 just refused.

**Beat:** retry re-spawning aborted lanes as fresh children. It creates a second
spawn path and a "which child is the current attempt at lane X" question that
`lane_id` cannot answer without becoming versioned.

### 23. A child's title is `{parent title} — {lane id}`, and its branch is named the ordinary way

*2026-08-17.* Nothing about child branch naming is special-cased: a child is an
ordinary task, so `ResolveBranchName` runs with the child's own id, slug,
project and fields, through whatever template the project or global config sets
(task 001). The title is the only new input, and `Slug` derives from it.

**Beat:** a `title:` field on the lane spec. 014.5 enumerates `Lane` as `ID`,
`Workflow`, `Steps`, `Fields`; a fifth field earns its place only once someone
wants a title that the parent's own title does not imply.

### 24. `on_conflict.agent` is a full agent `Step`

*2026-08-17.* The value is a `workflow.Step` of type `agent`, validated by the
existing `validateStep` and executed by the existing agent executor in the
parent's worktree, with the conflicted file list available to the prompt
template. `prompt`, `check`, `check_timeout`, `timeout`, `max_retries` and the
`agent`/`model`/`effort` triple all behave exactly as they do anywhere else.
`max_retries` defaults to 0: one attempt, then the block.

This makes decision 8's "gated by a `check` the way any agent step is" literally
true rather than approximately true.

**Beat:** a bespoke struct with a hand-picked subset of those fields. It is a
second agent-step vocabulary that will drift from the first one field at a time.

### 25. The scheduler performs `ChildrenSettled`

*2026-08-17.* The scheduler transitions a parked parent, woken by
`task.children_changed` (decision 14) rather than by new polling.

**Beat:** the last child's `taskrun` actor doing it. Two children settling
concurrently both believe they are last, or neither does; and an actor writing
another task's state breaks the sole-writer invariant outright. The scheduler is
already the single serialized decision point that exists so admission is
unraced, and this is the same kind of decision wearing a different name.

### 26. `task.children_changed` fires on child creation too

*2026-08-17.* Emitted on every fan-out ancestor when a descendant is created as
well as when one transitions. Creation changes the rollup's counts exactly as a
transition does, and a root whose depth-2 subtree is still being spawned is
precisely when a live count is worth having. Step-level activity stays out: that
is the per-task stream's job, unchanged.

### 27. The `children` rollup is present whenever a task has children

*2026-08-17.* Not only in `awaiting_children`. A parent mid-join, or one already
`done`, still wants its lane ids reachable from one `GET`.

**Beat:** gating the field on the state, as decision 13 words it. That forces a
client to know the state in order to know whether to look, and the rollup's
whole purpose is to answer "what is going on down there" in one request. The
cost of the general form is one indexed existence check on tasks with no
children.

### 28. `fan_out.max_tasks` counts descendants only

*2026-08-17.* Across the whole resolved tree, excluding the root. The bound
exists to cap what a fan-out *creates*; counting the root makes the configured
number off by one against the sentence that explains it.

### 29. A lane's `fields:` merge with the parent's, lane wins

*2026-08-17.* Decision 4 gives a lane `fields:` and decision 10 inherits
overrides and priority, but fields were left unstated. They inherit the same
way: the subtree gets the root's context and overrides what it needs.

**Beat:** replacement. A lane that had to restate every field the parent set
would make `fields: { module: api }` unwritable in any workflow that uses fields
for anything else.

### 30. Two config blocks, read at the point of use

*2026-08-17.* `config.yaml` gains `parallel: { max_parallel: 4 }` and
`fan_out: { max_depth: 3, max_tasks: 64 }` — a block per step type, named the
way this document names them.

**Beat:** folding all three into `defaults:`. What lives there today is timeouts
a step may override per-step; these three are not step-overridable, so they
would be misfiled next to things that look like them and behave differently.

All three are read **when they are used** — `max_parallel` when a group starts,
the two bounds in the task-creation path — so a hot reload (§12) governs the
next group and the next task and never resizes something already running. That
also keeps daemon configuration out of the workflow snapshot, which §5.3 reserves
for workflow content.

## Tasks

### Phase 1 — `type: parallel`

- [x] **014.1 — Schema and validation.** ✓ 2026-08-17 `type: parallel` with inline `steps:` and
  `max_parallel:`; sub-steps validated by the existing `validateStep`, with
  `manual` rejected inside a group — a gate ends the actor goroutine and
  releases the slot (§6), and there is no state meaning "one sub-step of one
  group is gated" — and `on_input: require` rejected for the same reason
  (decision 19); `check`, `timeout` and `max_retries` legal per sub-step, plus a
  group-level `timeout`; `rejectFields` for everything that is not
  `steps`/`max_parallel`/`timeout`; the `parallel.max_parallel` daemon default in
  `internal/config` (decision 30).
- [x] **014.2 — Engine.** ✓ 2026-08-17 Depends: 014.1. Concurrent execution inside the task's
  actor goroutine, bounded by `max_parallel`; one `step_runs` row per sub-step
  sharing the group's `step_index` and keyed by `step_id`, with no row for the
  group itself (decision 17); `CountStepAttempts`, `LastFailedStepRun` and
  transcript naming widened to take a step id (decision 16); per-sub-step attempt
  numbering and retry budget; a retry re-runs only the failed sub-step; a failure
  does not cancel siblings (decision 18); the group succeeds iff every sub-step
  does. The actor remains the sole writer — it forks, collects, and writes the
  rows itself.
- [x] **014.3 — Clients and docs for phase 1.** ✓ 2026-08-17 Depends: 014.2. TUI step list
  renders a group and its sub-steps with independent live output (`run_id` on
  every chunk already disambiguates, §13.3 unchanged); spec §7, §8.2 and §11
  amended in place, including the explicit note that `max_parallel` is a second
  concurrency dimension the caps do not govern; `reference/workflow-schema.md`,
  `guides/workflows.md`, `reference/configuration.md`.

### Phase 2 — `type: fan_out`

- [x] **014.4 — Migration and store.** ✓ 2026-08-17 `0007_fan_out.sql`: `parent_task_id`,
  `parent_step_index`, `lane_id`, `lane_order`, `idx_tasks_parent`. Typed CRUD
  for the parent link, the recursive-CTE subtree query, and the list filters
  from decision 13.
- [ ] **014.5 — Lane schema.** Depends: 014.1. `type: fan_out` with `lanes:`,
  `merge:`, `on_conflict:`; the `Lane` type (`ID`, `Workflow`, `Steps`,
  `Fields`) with `workflow` and `steps` mutually exclusive and exactly one
  required; recursive validation; `Marshal` round-trip.
- [ ] **014.6 — Creation-time tree resolution.** Depends: 014.5. Resolve every
  named lane workflow through registry shadowing into the parent's snapshot;
  synthetic `workflow_name` for inline lanes; cycle detection with the path in
  the `400`; `fan_out.max_depth` and `fan_out.max_tasks` computed and enforced
  in the insert path; the registry-load §8.2 cycle **warning**.
- [x] **014.7 — FSM.** ✓ 2026-08-17 `awaiting_children` in `taskstate`, its three table rows,
  `HoldsSlot` false, the new `Settled()` predicate with `Terminal()` left alone
  (decision 20), and the §6 human-action table (cancel only).
- [ ] **014.8 — Spawn and resume.** Depends: 014.4, 014.6, 014.7. The `fan_out`
  step creates its children (base branch = parent's branch, title
  `{parent} — {lane}` and the ordinary branch template per decision 23, fields
  merged with the parent's per decision 29, overrides and priority per
  decision 10), parks the parent, and the scheduler — not the last child's actor
  (decision 25) — re-queues it via `ChildrenSettled` once every descendant is
  `Settled()`.
- [ ] **014.9 — The join.** Depends: 014.8. Sequential `--no-ff` merges in
  `lane_order`; `ReasonMergeConflict = "merge_conflict"` and
  `ReasonLaneFailed = "lane_failed"` in the §18 vocabulary; `on_conflict: {agent: …}`
  as a full agent `Step` (decision 24); the decision-9 re-entry rules for both
  `interrupted` and `blocked`, plus decision 22's re-check on a `lane_failed`
  retry, wired into `taskrun/recover.go`.
- [ ] **014.10 — Lifecycle.** Depends: 014.8. Cascading cancel; archive refusing
  a non-terminal descendant then cascading with per-child dirty confirmation;
  `gc` and `doctor` seeing lane worktrees as the ordinary task worktrees they
  are.
- [ ] **014.11 — API and events.** Depends: 014.4, 014.8. List filters
  (`?parent_id=`, `?include_children=`), the `children` rollup on
  `GET /v1/tasks/{id}` whenever the task has children rather than only in
  `awaiting_children` (decision 27), and `task.children_changed` emitted
  post-commit on every fan-out ancestor, on child creation as well as on every
  descendant state change (decision 26).
- [ ] **014.12 — TUI.** Depends: 014.11. A parent row reading
  `awaiting_children (2 blocked)`; drilling from a parent into its lanes and
  back; the new state in the board's colour and action-bar tables
  (`internal/tui/bindings.go` key table updated with it).
- [ ] **014.13 — Docs.** Depends: all. Spec §5.3, §6, §10, §11, §12.4, §13.2,
  §13.3, §14 and §18 amended in place with dated notes — §12.4 and §14 because
  recovery aborting a merge and the new `tasks` columns are exactly what those
  sections describe; §20 amended to promote parallel steps and fan-out out of
  future work the way cursor was, leaving branching/conditionals there;
  decision-record rows **23 and 24**, one per step type, because decision 2
  separated the two mechanisms and one row would re-merge them; user-facing
  `reference/workflow-schema.md`, `reference/api.md`,
  `reference/configuration.md`, `reference/task-lifecycle.md`,
  `guides/workflows.md` and the block-reason table — no new page, the caveats
  landing next to the schema that causes them — including, stated plainly, that
  a fan-out fills the caps rather than exceeding them, and that N lanes leave N
  worktrees until archived.
- [ ] **014.14 — Gate.** Depends: 014.2, 014.9, 014.10. New `scripts/m6-gate.sh`
  covering **both** phases, committed executable via `git update-index --chmod=+x`
  — a non-executable gate passes on Windows and exits 126 on both POSIX legs —
  and wired into CI on all three platforms, merge behaviour on Windows being
  the thing a gate should prove rather than assume. Scenarios: a `parallel`
  happy path; a failing sub-step; a retry re-running only the failed sub-step;
  happy-path two-lane merge; an induced conflict reaching `merge_conflict`,
  resolved by hand, retried, remaining lanes merged; a crash mid-merge
  recovering by `--abort` and idempotent re-merge; one depth-2 tree; a lane
  aborted reaching `lane_failed`; and both creation-time `400`s (cycle,
  `max_tasks`).

## Verification

Not started.
