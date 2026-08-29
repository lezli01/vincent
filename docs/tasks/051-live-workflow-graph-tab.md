# 051 — Live workflow graph tab on the task workspace

**Status:** ✅ done (5/5)

Adds a fifth tab, **Workflow**, to the full-screen task workspace (task 049):
the task's own `WorkflowSnapshot` drawn as an `internal/tui/workflowgraph`
diagram, with a per-node run-state overlay.

This is the follow-up task 017 named rather than an omission. Its non-goals
say so — "Visualizing live task execution state; this task visualizes workflow
definitions. **Runtime overlays can be added later against the same node ids**"
(`017-workflow-visualization.md:116`) — and decision 16 reserved the single END
node as "the anchor a later runtime overlay wants for *reached*". Decisions 3
(coordinates are never identity) and 19 (a live reload re-lays-out in place,
keeping selection by node id) are what make an in-place overlay possible; this
task spends the seam they left rather than reopening it.

017 decision 13 chose a sub-layer over a routed screen because the workflows
takeover had no tab strip to add to. The workspace has one (049 decision 2), so
a fifth tab is where that reasoning points. 017 decision 20 declined a CLI
counterpart for the definition endpoint; the new task-scoped endpoint inherits
that — there is no `vincent task workflow`.

## Sub-tasks

- [x] **051.1** `GET /v1/tasks/{id}/workflow` and its client
      (`internal/api/taskworkflow.go`, `apiclient.GetTaskWorkflow`).
- [x] **051.2** Lane-inner node-id namespacing, with its golden refresh
      (decision 2).
- [x] **051.3** The overlay through the pure diagram/render/model pipeline:
      `RunState`, `Overlay`, `Model.SetOverlay`, the off-snapshot group.
- [x] **051.4** The tab and its bindings (`taskworkflowtab.go`, `taskview.go`,
      `ctxTaskWorkflow`).
- [x] **051.5** Documentation, the gate's runtime leg, and the spec amendment.

## Decisions

1. **A `fan_out` lane's state lands on the lane caption, not on its steps.**
   A lane with inline steps is *N* nodes inside a `Column`; only a
   named-workflow lane collapses to one node. Those inline steps run in a child
   task, so the parent holds no `step_run` for them and cannot honestly paint
   them. The child's state and id go on the lane caption, which already carries
   the lane id and its guard; the lane's step nodes render as never-reached. The
   child list comes from the existing `GET /v1/tasks?parent_id=`.

   *Rejected:* stamping every inline step node with the child's task-level
   state — it asserts per-step truth the parent does not have, four steps all
   reading "running" when at most one is. *Rejected:* fetching each child's own
   `step_runs` to join per node — accurate, and it costs one call per lane plus
   a refetch on every `task.children_changed`, on a surface refreshed live.

2. **Lane-inner node ids get a namespace, and that is a prerequisite.** The
   overlay joins `step_run.step_id` to a node, and `Node.ID` was the raw step
   id while `builder.add` appended without dedupe. Step-id uniqueness is *per
   body* (§7.6, task 014 decision 4), so a top-level `build` and a lane's
   `build` were already two nodes with the same id, and a parent `step_run` for
   `build` would have painted both. `Build` now prefixes lane-inner node ids
   (`<fanout>.<lane>/<step>`) while `Node.StepID` keeps the raw id. This also
   fixes a latent 017 selection bug and amends 017 decision 3's "stable
   semantic ids derived from workflow step ids" with the namespace rule.

   *Rejected:* refusing to bind any node inside a fan_out group and leaving
   `Build` alone — cheapest, keeps the goldens intact, and leaves a duplicate-id
   selection bug in a component whose whole premise is that ids are identity.

3. **Runs with no node are drawn off-graph, below END.** A follow-up round runs
   a step that "is not part of the snapshot" (`internal/api/actions.go`), and a
   repair rewrites one. Unmatched `step_run`s render as an explicit
   off-snapshot group beneath the single END node — 017 decision 16's "reached"
   anchor is exactly the right place for "and then this happened, outside the
   authored flow". They are neither dropped nor smuggled into the topology.

   *Rejected:* a count line in the inspector strip only — it keeps the picture
   pure and makes the one surface whose value is being accurate about what
   happened silent about the attempts most likely to be surprising.

4. **Spliced includes stay flat; `resolved_from` is the attribution.** A task
   graph shows the *N* steps an `include` expanded into where the workflows
   screen shows one collapsed node. That is correct: the task graph is a picture
   of what ran, and what ran is the flat body. `resolved_from` already prints in
   the node's inspector Detail as `from: a → b`.

   *Rejected:* framing consecutive steps that share a `resolved_from` chain — a
   fourth `Group` kind, new layout cases and a golden sweep, for a resemblance
   neither surface promises.

5. **`tab` keeps meaning "next tab" on the Workflow tab.** A direct collision:
   049 decision 2 makes `tab`/`shift+tab` the workspace-wide tab cycle, while
   017 decision 14 gives `tab` the graph's source-order node walk. Inside the
   workspace the tab cycle wins, so the component's walk stands down
   (`Model.SetSourceWalk(false)`) rather than being shadowed. Arrow/`hjkl`
   selection, `shift+arrow` panning and the pager keys carry over unchanged. If
   a source-order fallback proves missed in the gate walkthrough, it earns a
   non-colliding key then.

6. **The new endpoint returns a task-scoped body, not the registry envelope.**
   `workflowgraph.Build` takes a `*apiclient.WorkflowBody` and nothing else, and
   the registry envelope's `scope`, `file`, `platforms` and
   `platform_supported` are registry facts a snapshot has none of — sending them
   empty would be four fields inviting a wrong reading. The response is the
   `definition` body plus `errors`/`warnings`, following 017 decision 11's rule
   that a document which does not parse is a 200 with findings and a null
   `definition`, never a 4xx. A task's provenance is `workflow_origin` on
   `GET /v1/tasks/{id}`.

7. **The overlay is derived from the rows the detail sub-model already holds,
   not from a second subscription.** 049 decision 4 keeps the task's SSE seam in
   one model; the tab reads `TaskDetail.Steps` after each refresh and recomputes
   the whole overlay. It is total rather than incremental on purpose: an
   incremental overlay would be a second copy of the engine's state machine
   living in a viewer. The definition itself is fetched once per open and
   refetched only when an `edit + retry` override appears, which is the one
   writer of a running task's own snapshot.

## Non-goals

- A CLI counterpart (017 decision 20).
- Unrolling loop iterations or discovered fan-out lanes into extra nodes. The
  topology is the authored one; re-laying out as a task runs would move nodes
  under a reader, which is what 017 decision 5 refuses.
- Editing. The graph remains a viewer, here as on the workflows screen.
