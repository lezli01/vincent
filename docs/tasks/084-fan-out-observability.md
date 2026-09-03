# 084 — Make a fan-out run observable

**Status:** ✅ done (5/5)
**Issue:** #316
**Amends:** §7.6, §13.2, §15
**Amends in place:** [080](080-fan-out-dag.md) decision 5 — the derived
`lane:`/`for_each:` pair **moves** to a `derived_from:` record instead of being
dropped
**Claims:** 080's deferred *"drawing `needs:` edges in the TUI workflow graph
and the CLI renderer"*, for its TUI half only; the CLI renderer stays deferred
and `workflow.SentinelLane` stays in place for it
**Keeps, without relitigating:** [014](014-workflow-fan-out.md) decision 13
(descendants are excluded from the task list) and [051](051-live-workflow-graph-tab.md)
decision 1 (a lane's state rides on its caption, never on its inline step
nodes), plus 051's non-goal of unrolling lanes into extra nodes

A `fan_out` step's lanes are real child tasks — own worktree, branch, step
rows, transcript — and almost none of that was reachable from the parent. The
board excluded lanes and offered one modal drill that only worked while the
parent was `awaiting_children`; the graph printed a lane's task id as text you
could not open; the parent's Output pane was blank while the fan-out ran; and
after the join the diff was a wall of merged hunks with nothing saying who
wrote what. The more work a workflow fanned out, the less of it vincent could
show.

Every code claim in the issue checked out. Two of its premises narrowed the
work rather than blocking it:

- **The engine already names the lane in every failure it writes.**
  `join.go` writes `lane "api" (task 42) is blocked, not done` for
  `lane_failed` and `lane "api" (task 42) conflicts in:` plus the conflicted
  paths for `merge_conflict`; `derive.go` already names the offending line, id
  or bound for `fan_out_invalid` and `fan_out_limit`. Attribution is therefore
  a **rendering** task — lift those into the detail header, the step row and
  the graph caption, and add the lane's *own* block reason, which is the one
  fact the engine's message cannot carry. No new engine strings, and §18's
  block-reason table is unchanged.
- **This is rendering plus one read-only endpoint.** Nothing here changes what
  a fan-out *does*.

## Phases

- **084.1 — Attribute the parent's diff to its lanes.** ✅
  `GET /v1/tasks/{id}/diff?by=lane` (`internal/api/difflane.go`), the
  apiclient option and response shape, the Diff tab's `lane › file` grouping
  over task 012's file grouping, and `m6-gate.sh` scenario 11 driving it end
  to end over a two-lane fan-out.
- **084.2 — Reach a lane from the workspace.** ✅ `l` and `U` on every tab
  that can name a lane, `esc`'s navigation stack, the Output pane's `<`/`>`
  lane selector and its single subscription, and the failure attribution on
  the detail header and the `fan_out` step row.
- **084.3 — Draw the lane DAG, and keep a derived list's provenance.** ✅
  `needs:` edges between lane columns, waves stacked in the rounds they run
  in, the `eager` badge, the lane's own block reason on its caption, and
  `workflow.Derivation` — the `derived_from:` record, its validator exemption,
  its refusal in an authored document and its absence from the served schema.
- **084.4 — The board's lane tree.** ✅ A `fan_out` parent's row becomes a
  disclosure control: `L` expands its lanes as indented task rows and folds
  them away again, composing to `fan_out.max_depth`. `laneParent` and the
  modal drill are removed.
- **084.5 — Documentation.** ✅ The §7.6, §13.2 and §15 amendments, task 080's
  decision 5 note, `docs/guides/tui.md`, `docs/reference/api.md`,
  `docs/reference/workflow-schema.md`, `docs/features.md`, `CHANGELOG.md`, and
  corpus entry 12 plus runtime legs 13–15 in
  [`docs/gates/017-workflow-graph.md`](../gates/017-workflow-graph.md).

## Decisions

### 1. The board keeps decision 13; the parent's row becomes a disclosure control

Task 014 decision 13 excludes descendants from the task list, and it is right:
a list is the work someone asked for, and a 64-task tree buries it. So the
lanes do not join the list. The parent's row gains `▸`/`▾` instead, and its
lanes render as indented task rows beneath it.

The lanes stay out of every count **by construction** rather than by
filtering — they are fetched with their own `GET /v1/tasks?parent_id=N`, one
request per *expanded* parent, and never enter the board's task list. So the
flat count, every group header's count and the `!` attention badge are
computed from exactly the list they were computed from before, and a board
with nothing expanded issues exactly the request it issued before and renders
exactly the rows it rendered before. A lane row is an ordinary task row to
everything else: folding, the `space` selection, the action keys, the column
shedding ladder.

The press acts in **every** state the parent passes through. No list row
carries a field saying "this task once had lanes" — §13.2 serves `children` on
the detail endpoint only — so the press asks, and a task with no lanes answers
with none and nothing moves. That is the single biggest gap the old drill had:
it could be entered only from `awaiting_children`, and a parent `blocked` on a
failed lane or a merge conflict is exactly when a lane is worth reading.

An expanded lane is **not** filtered out by the board's filter. The filter is a
question about the list, and hiding half of an opened fan-out would make the
expansion lie about what it opened.

### 2. The expanded set is session-only

It is deliberately *not* written to `{data_dir}/tui.json` beside task 054's
folds. A fold is a label path, which survives a restart still meaning what it
meant. A task id is not, and 054 decision 4's rule for dropping a path whose
project or workflow has left the board has no honest counterpart for a task
archived while the TUI was down.

### 3. `esc` gains a navigation stack

The workspace remembers the chain it was opened *through*, and `esc` pops one
task before falling through to the board — drill three lanes deep, `esc` three
times. That is what makes "open any lane and get back" true at any depth, and
it composes with the board tree rather than duplicating it. A task on the chain
that has since been archived or vanished is **dropped** from it rather than
popped to. A task opened from the board arrives with an empty chain.

The reciprocal jumps are explicit keys rather than inferred: `l` opens the lane
the current tab's selection resolves to, `U` opens this lane's parent, and the
`parent task` fact in the Task Details inspector becomes an action instead of a
bare number.

### 4. `l` resolves the lane from where the reader is standing

A tab that carries a lane selection of its own is taken at its word — the
Workflow tab's graph cursor, the Output pane's selector, the Diff tab's lane
sections. The Steps timeline means the `fan_out` row under the cursor. Every
other tab means the lane the failure is about. One binding, registered on every
tab that can name a lane, because a reader standing anywhere in a parent's
workspace means the same thing by it; the tabs differ only in the resolution
(`taskView.laneJump`).

Where a tab already gave `l` the vim meaning of `→` — the Output pane's attempt
selector — that meaning is kept for a task with no lane to open.

*This is also where the one defect of the phase was found and fixed
(`0fadbd8`): `l` on the **Diff** tab resolved to the lane the join blamed
rather than the lane whose section the cursor was in, so reading `web`'s hunks
and pressing `l` opened `api`. The diff pane already knew the child task of the
section under its cursor; `laneJump` now asks it, and falls back to the blamed
lane only for the remainder and for an ungrouped diff.*

### 5. The Output pane shows one lane at a time

`<`/`>` cycle the task's own output and each lane's. **Exactly one** extra live
subscription exists at a time, torn down when the selection moves and when the
workspace leaves the task.

Interleaving every lane was rejected on 051 decision 1's cost objection (one
call per lane plus a refetch on every `task.children_changed`, on a surface
refreshed live) and on 049 decision 4's one-SSE-seam rule — and because the
daemon drops live chunks for a slow subscriber (§13.3), so a 64-lane interleave
would render lossily and read as a bug. The transcript file stays the durable
copy; this is a view, not a second store.

### 6. Diff attribution is server-side, on `?by=lane`

The daemon walks its own `Merge lane '{lane_id}' of task {child_id}` commits on
the parent's first-parent chain and emits one section per lane
(`diff <merge>^1 <merge>^2`), plus a remainder section for the parent's own
commits and uncommitted work. That makes the merge subject §7.6 fixes a
**machine-read contract** rather than a convenience, which §7.6 now says out
loud: changing the wording breaks attribution for every branch already on disk.

Fetching each lane's own `/diff` from the client was rejected on the same 051
cost objection, and because a lane's diff is taken against *its* base — so
under `needs:` it double-counts a dependency merged into it.

A task that fanned nothing out comes back as a single remainder holding the
whole diff, so a client can read the grouped shape unconditionally. The
parameter absent is byte-for-byte today's `text/plain` response. Any other
value is a `400` rather than a silent fall-through: `lane` is the only grouping
there is, so anything else is a typo, and answering it with the ungrouped diff
would hand a client the wrong shape and call it success.

A file two lanes touched appears under both, which is the truth.

### 7. The Pull Request tab lists lanes without fetching their checks

The parent's own section is unchanged; beneath it, one row per lane carrying
branch, linked pull request number and state, from the lane list the parent
already has. Checks stay one call for one task — the jump opens the lane, whose
own tab has them. The row is not GitHub-gated: `l` is a jump between two tasks
vincent owns, and it means the same thing whether or not a lane has a pull
request.

### 8. Derivation provenance survives materialization — task 080 decision 5, amended

Decision 5 said that after materialization "the step is an ordinary static
`fan_out`, so the graph, preview, editor and `workflowdef` are correct with no
further change". That was true of everything that **runs** the step and false
of everything that **draws** it: afterwards a derived list and a hand-authored
one are the same lanes, so no reader could be told which they were looking at.

The `lane:`/`for_each:` pair now **moves rather than disappearing**, into a
`derived_from:` record on the step — the same move a resolved lane's
`workflow:` makes to `resolved_from:`. It keeps the `lane:` id template and the
`for_each:` templates only; the rest of the `lane:` template is already visible
on every lane it produced, and copying it would be one more thing that could
disagree with the lanes beside it. The two live fields are still cleared, and
materialization still happens once, at spawn.

It is the first **snapshot-only** field, and three load-time rules make that
safe:

- **Exempt from `validateFanOutShape`'s exclusivity check.** A snapshot is
  re-parsed on every admission (§5.3), so `lanes:` beside a *record* has to be
  accepted where `lanes:` beside a live `lane:`/`for_each:` is refused.
- **Refused in an authored document**, with the wording an unknown key gets.
  It cannot be refused at decode and still be readable back out of a snapshot,
  so the refusal lands in validation instead, keyed off a new
  `workflow.Options.Authored`. False is the permissive reading because every
  snapshot re-parse in the daemon spells its options `workflow.Options{}`; the
  registry sets it on everything it parses and on the options it hands the API,
  which is every surface that accepts an authored document.
- **Absent from `GET /v1/workflows/schema`**, so the structured editor never
  offers a control for something no author may set.

Recovering the provenance from the spawn round's `step_runs` row was the
alternative and was rejected: the retry budget can rewrite that row, and the
picture a reader is shown must not change because a lane was retried.

### 9. The graph draws over the authored lane columns, and unrolls nothing

Task 051's non-goal — no unrolling of loop iterations or discovered fan-out
lanes into extra nodes — stands. `needs:` edges, waves, the `eager` badge and
the derived mark are all drawn over the lane columns that are already there.

A lane that needs nothing keeps its edge from the step's header; a lane that
needs others takes its incoming edges from *their* merges instead, because
drawing both would say a dependent lane starts in round one. The wave is
**derived** — a topological level over `needs:`, the same derivation the engine
schedules by — never authored. A fan-out whose lanes need nothing is one wave
and lays out exactly as it did before.

`barrier` gets no badge: it is the default, and the difference worth seeing
without selecting is the one where a lane's dependents start before its
siblings have finished. The derived mark goes on the **frame**, because the
derivation produced the lanes and the lanes are what the frame encloses.

051 decision 1 is kept: the lane's own block reason goes on the **caption**,
which is where 051 put the lane's state, and never on the inline step nodes.
The caption is the only place that fact can be told at all — a lane's steps run
in the child and never appear on this graph.

## What landed

- `internal/api/difflane.go` + `tasks.go` — `?by=lane`, its sections and its
  `400`; `internal/apiclient/tasks.go` — the typed call.
- `internal/workflow` — `Derivation`, `Options.Authored`, the validator
  exemption and the authored-document refusal; `internal/api/workflowdef.go`
  and `internal/apiclient/workflowdef.go` — the DTO field, absent from the
  served schema.
- `internal/taskrun/derive.go` — the pair moves instead of being nil'd.
- `internal/tui` — `boardlanes.go` (new) and `board.go`/`boardfold.go`/
  `boardgroup.go`/`boardmark.go` for the lane tree; `root.go`/`shell.go` for
  the navigation stack; `taskview.go`, `detail.go`, `detailrender.go`,
  `taskworkflowtab.go`, `taskpulltab.go`, `diffpane.go` for the jumps, the
  attribution, the lane selector and the lane grouping; `bindings.go` for the
  keys, which are the documentation.
- `internal/tui/workflowgraph` — `diagram.go`, `layout.go`, `render.go` for the
  edges, waves, badge and derived mark.
- `scripts/m6-gate.sh` scenario 11.

## Follow-ups

- **Corpus entry 12 and runtime legs 13–15 of
  [`docs/gates/017-workflow-graph.md`](../gates/017-workflow-graph.md) have not
  been walked.** The graph's acceptance is a judgement about a picture, as it
  has been since 017; the automated half is
  `internal/tui/workflowgraph/lanedag_test.go` against `testdata/lanedag.txt`.
- **No `docs/assets/tui-*.png` was recaptured.** Every existing capture is
  still a true picture — an unexpanded board and a diff with no lanes render as
  they did — and the new states are additive, so nothing on the page is now
  wrong. A person re-runs `scripts/screenshots.sh` to add them.
- The **CLI graph renderer** is still deferred from task 080, and
  `workflow.SentinelLane` is still in place for it.
