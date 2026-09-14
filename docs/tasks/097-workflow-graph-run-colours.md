# 097 — Color the task Workflow tab graph by run state

*Issue [#418](https://github.com/lezli01/vincent/issues/418). Planned and
implemented 2026-09-14.*

Status: **done (4/4)**.

**Spec:** amends §15 (*Workflow graph*).

## Problem

The Workflow tab of the task workspace ([051](051-live-workflow-graph-tab.md))
draws each node's run state in words and glyphs only. The Steps tab and the
board already color the same states, so the one surface that exists to answer
"where is this task" is the one a reader has to read word by word. A long,
branching workflow is where that costs most: which path the run took is spelled
on every node and shown on none.

## Decisions

1. **Node colors follow the issue's table.** A node takes the style of its
   newest attempt, and `RunState.Task` — a task parked on the node — wins over
   the step's state, the precedence `stateGlyph` already uses.
   `succeeded`/`approved` green, `running` cyan, `failed`/`rejected` red,
   `interrupted` yellow, `skipped`/`stopped` faint; task `blocked` red and
   bold, `awaiting_input` yellow and bold, `paused` magenta. A never-reached
   node looks as it did. Border, label row and kind row all take the state
   style. A colored node shows its selection through the heavy border glyphs
   alone; an uncolored one keeps the cyan `Selected` style.

   *Beat:* yellow for in-progress (it is the warning color everywhere else in
   the TUI, and `interrupted` needs it); border-only or glyph-only coloring (a
   thin tinted border is lost at a glance, which is the reading this is for);
   a three-color good/bad/other collapse (it would make `interrupted` and
   `skipped` read as the same thing as `running`).

2. **One step-state palette in `internal/tui`, shared with the Steps tab.**
   `stepStateStyle` beside `renderAttemptState` in `detailrender.go` is the
   single lookup; both the Steps tab and the graph read it, so `approved` and
   `rejected` gain green and red on the Steps tab too. Task-level styles —
   parked states and every lane state — come from the board's `stateStyles`.
   The graph owns a copy of neither.

3. **Off-graph attempts get words first, then color.** `buildOverlay` gives
   each off-snapshot node its newest attempt's `RunState`, built exactly as an
   authored node's, under `workflowgraph.OffNodeID`. It goes into
   `Overlay.Nodes`, never `OffGraphRun`: `sameOffGraph` compares that struct to
   decide a re-layout, and a state change must never re-lay-out. Those nodes'
   stripped text changes on purpose — they now print `✔ succeeded` and the like.

   *Beat:* color-only off-graph nodes, which would have made color carry
   meaning on its own (task 017 decision 6).

4. **An edge is taken by rule**, not by "both ends reached" alone.
   - Flow edges are taken when both ends are reached.
   - A `condition`'s `false` branch is taken only when its newest row is
     `stopped`; its onward edge only when that row is `succeeded` and the
     target is reached. A `break`'s `true` branch likewise only on `stopped`,
     its onward edge only on `succeeded`. These are the verdicts the engine
     already records (`internal/taskrun/engine.go`, `loop.go`), and the newest
     row governs.
   - A back-edge is taken when its source is reached and some node in the
     loop's body, at any depth, has `Iteration ≥ 2`.
   - `needs:` edges and edges into or out of a lane's inline steps are never
     taken — those steps are the child task's (051 decision 1).
   - END is reached only when the task is `done`, which the host supplies as
     `Overlay.Done`. END itself stays unpainted.

   *Beat:* the literal both-ends rule (it lights a passed condition's `false`
   edge once the task reaches END); coloring every edge out of a finished step
   (it lights both branches of every condition).

5. **Structure nodes are reached by derivation, for edges only.** A `parallel`
   or `loop` header is reached when any node in its group is; a fan_out's merge
   when its fan_out's row is past `running` or anything after it is reached.
   Their boxes are not painted — no row, no words. A taken edge takes its
   source's style, or its target's when the source has no state of its own.

   *Beat:* leaving gaps at structure nodes, which breaks the colored path at
   every `parallel`, `loop` and merge.

6. **Lane captions take the board's task-state styles** —
   `stateStyles[Task]` when the child is parked (pause-requested included),
   else `stateStyles[State]`.

7. **The host supplies the lookups; topology stays in the renderer.**
   `workflowgraph.Theme` gains `NodeState` and `LaneState`; nil, or a `false`
   answer, means the role style. Which edges were taken is a pure function in
   `workflowgraph` (`taken.go`). The canvas records a per-cell tint beside its
   role. Where wires share a cell a taken edge beats an untaken one, the later
   of two taken edges in `Scene.Edges` wins, and an arrowhead belongs to the
   edges that end there. Edge labels keep the `EdgeLabel` role.

8. **The definition graph keeps its own theme.** `graphTheme()` is unchanged
   for the workflows screen's `g` layer; the Workflow tab uses
   `taskGraphTheme()`, which is `graphTheme()` plus the two lookups.

Task 017 decision 6 is kept, not relaxed: with every style stripped the picture
is byte-identical to the uncolored rendering of the same overlay. 017 decisions
3 and 5 hold — coloring changes no coordinate and loses no selection.

## Tasks

- [x] **097.1** `workflowgraph`: `Theme.NodeState`/`LaneState`, per-cell
  tints, the taken-edge and derived-reach rules, `Overlay.Done`,
  `OffNodeID` exported; tests for tints, selection, crossings, every rule, and
  that styling changes neither the stripped picture nor the layout.
- [x] **097.2** `internal/tui`: `stepStateStyle` shared with the Steps tab,
  `taskGraphTheme()`, and `buildOverlay` setting `Done` and off-graph states.
- [x] **097.3** Spec §15 amendment, the TUI guide's Workflow tab section, and
  `CHANGELOG.md`.
- [x] **097.4** The 017 gate's runtime leg gains the color checks (legs
  16–19). They have not been walked yet; a walk is recorded as a row in
  `docs/gates/017-workflow-graph.md`, which says so.
