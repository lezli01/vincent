# 017 — Workflow visualization in the TUI

**Status:** ⬜ planned (0/8) · **Opened:** 2026-08-18

Vincent's workflow screen currently explains a workflow as a numbered list of
its top-level steps. That was sufficient while workflows were mostly linear,
but the language now has real structure: `parallel` groups, `fan_out` lanes and
joins, guards and `condition` steps, loops, and `break`. A list can name those
constructs but cannot show their control flow.

This task adds a read-only workflow graph to the TUI and deliberately designs
that graph as the foundation of a later TUI workflow builder. The first release
is a viewer, not an editor, but its model, layout and interaction boundaries must
not need to be replaced when editing arrives.

The intended shape is:

```text
workflow definition from daemon API
             │
             ▼
      WorkflowDiagram
  nodes · edges · groups
             │
             ▼
          Layout
             │
             ▼
       DiagramScene
 positions · routed edges
             │
             ▼
       WorkflowGraph
 selection · pan · viewport
             │
             ▼
 Bubble Tea v2 + Lip Gloss v2
```

A representative workflow should read spatially, for example:

```text
 ╭──────────────╮
 │ plan         │
 │ agent        │
 ╰──────┬───────╯
        │
 ╭──────▼───────╮
 │ condition    │
 ╰──┬────────┬──╯
    │ true   │ false
    │        └──────────────► END
 ╭──▼───────────╮
 │ fan_out      │
 ╰──┬────────┬──╯
    │        │
 ╭──▼───╮  ╭─▼─────╮
 │ API  │  │ tests │
 ╰──┬───╯  ╰─┬─────╯
    └─────┬───┘
      ╭───▼────╮
      │ merge  │
      ╰───┬────╯
          │
     ╭────▼────╮
     │ loop    │◄────╮
     ╰────┬────╯     │
          └──────────╯
```

The exact box drawing may evolve with implementation; the topology may not.

## The problem

The TUI is intentionally an API client. `GET /v1/workflows` currently returns a
registry summary whose step DTO carries only `id`, `name`, `type`, and `agent`.
That is exactly the right shape for a registry list, and exactly the wrong shape
for a graph: nested `steps`, fan-out `lanes`, `merge`, loop drivers and guard
expressions have already been discarded before the TUI sees the workflow.

The workflow model itself is richer. A `Step` may contain nested `Steps`,
`Lanes`, a `Merge`, `If`, `Count`, `ForEach`, and `MaxIterations`, with structure
whose semantics are defined by tasks 014–016. Reconstructing that structure from
the summary DTO would be lossy; reading YAML files directly from the TUI would
break the daemon/API boundary and would not work for built-ins or future remote
clients.

A second problem is architectural. A workflow builder is a likely next step. If
017 solves visualization by printing an ad-hoc tree, builder work will have to
throw it away because editing requires stable node identity, explicit edges,
ports/groups, hit targets, selection and deterministic re-layout. The viewer
must therefore be the first mode of an editor-capable component rather than a
one-off renderer.

## Goals

- Show a workflow's control-flow shape in the TUI, not just its top-level list.
- Correctly represent sequence, `parallel`, `fan_out`, merge, guards,
  `condition`, `loop`, and `break`.
- Preserve Vincent's daemon/API boundary: the TUI receives normalized workflow
  structure from the daemon and never parses repository workflow files itself.
- Keep Bubble Tea v2, Bubbles v2 and Lip Gloss v2 as the only TUI framework
  stack.
- Make diagram construction and layout deterministic and independently
  testable, without ANSI rendering or Bubble Tea state.
- Make the graph component editor-capable in its internal model and interaction
  seams while shipping only a read-only mode in this task.
- Degrade cleanly in narrow terminals and remain understandable without color.

## Non-goals

- Creating, deleting or changing workflow steps from the TUI.
- Writing YAML or preserving YAML comments/formatting.
- Drag-and-drop or mouse-first editing.
- A general-purpose graph visualization library inside Vincent.
- Visualizing live task execution state; this task visualizes workflow
  definitions. Runtime overlays can be added later against the same node ids.
- Recursively expanding a lane that references another named workflow. The
  referenced workflow is a collapsed node in 017; opening it is navigation, not
  recursive rendering.
- Replacing the existing external-editor path. `e` remains the escape hatch for
  editing workflow YAML until a builder task explicitly supersedes it.

## Decisions

### 1. Stay on the Charm stack; do not introduce a second TUI framework

*2026-08-18.* The visualization is a Bubble Tea v2 component styled with Lip
Gloss v2 and hosted in a Bubbles v2 viewport. Vincent already owns application
state, input routing, styling and screen composition in this stack. A graph does
not justify introducing `tview`, `termui`, or another event/rendering model
inside it.

Bubbles' viewport is the scrolling/cropping primitive. The graph owns a logical
scene larger than the terminal; the viewport decides which cells are visible.
Horizontal scrolling must remain enabled for a graph that is wider than the
screen.

**Beat:** a second TUI framework embedded only for the graph. It would create two
focus models, two styling systems and two ideas of terminal dimensions, then
make a future builder coordinate edits across both.

**Beat:** Lip Gloss' tree representation as the primary model. A tree can show
containment but not a fan-out join, a condition that exits a sequence, or a loop
back-edge without inventing false hierarchy.

### 2. `workflowgraph` is a Vincent-owned component, not a third-party graph widget

*2026-08-18.* Add a focused package under `internal/tui/workflowgraph`. Its
public surface to the rest of the TUI is small: load a definition, set size,
set/read selection, update interaction state, and render.

The package may use Bubble Tea, Bubbles and Lip Gloss, but its diagram model and
layout files must not import Bubble Tea. That keeps topology and geometry usable
by a future builder command model and makes them unit-testable without terminal
events.

Wanda is useful prior art for terminal graph navigation and layout ideas, but is
not a dependency for 017. Pulling in a graph widget whose current Charm versions
or bundled layout engine differ from Vincent's stack would trade a small amount
of drawing code for a large integration surface and binary-size cost.

**Beat:** depending on Graphviz or an ELK/elkjs bridge for the first version.
Vincent workflows are not arbitrary graphs: they are ordered sequences with a
small, known set of structured branch/join/loop constructs. A deterministic
workflow-specific layout is simpler to ship, test and keep stable across
platforms.

### 3. Diagram, layout and rendering are separate data stages

*2026-08-18.* The component uses three representations:

```go
type Diagram struct {
    Nodes  []Node
    Edges  []Edge
    Groups []Group
}

type Scene struct {
    Nodes  []PlacedNode
    Edges  []RoutedEdge
    Groups []PlacedGroup
    Width  int
    Height int
}
```

The exact fields are implementation detail, but the responsibilities are not:

1. **Build** converts the normalized workflow definition into semantic nodes,
   edges and groups.
2. **Layout** assigns deterministic coordinates and edge routes.
3. **Render** paints a `Scene` to terminal cells/strings with styles.

Node ids are stable semantic ids derived from workflow step ids plus explicit
synthetic ids for structural artifacts such as a merge or sequence end. Screen
coordinates are never identity.

**Beat:** rendering while recursively walking `workflow.Step`. That is short for
one screenshot and makes selection, directional navigation, edge routing,
incremental editing and geometry tests all depend on the renderer's recursion.

### 4. The daemon exposes a full workflow-definition endpoint

*2026-08-18.* Keep `GET /v1/workflows?project_id=` as the compact registry list.
Add a separate detail endpoint:

```text
GET /v1/workflows/{name}/definition?project_id=<id>
```

It returns the one workflow selected with the same project/global shadowing
rules as the registry list, but with a normalized, recursively structured
workflow DTO. The response includes enough semantic data for visualization and
future editing: workflow defaults and every step's applicable fields, nested
`steps`, fan-out `lanes`, `merge`, guards and loop configuration.

The API owns its DTO rather than serializing `internal/workflow.Workflow`
directly. The apiclient owns a matching client model. This keeps the HTTP
contract deliberate when the internal parser model changes.

A broken registry entry returns the same structured findings model used by the
list rather than making the TUI reopen and parse the source file. A built-in
workflow is equally addressable despite having no file path.

**Beat:** expanding the list endpoint to return every prompt, command and nested
step for every workflow. The workflow list is a hot navigation surface; paying
for full definitions when only one entry is being viewed is wasteful and makes
its otherwise-small contract harder to evolve.

**Beat:** importing `internal/workflow` into the TUI and loading files. The TUI
is an API client by architecture, project scoping/shadowing belongs to the
registry, and built-ins have no file to open.

### 5. Layout is deterministic, top-to-bottom and workflow-specific

*2026-08-18.* The primary flow direction is top-to-bottom. Ordinary sequential
steps occupy successive ranks. Structured steps introduce local groups:

- **sequence** — one node below the previous node;
- **parallel** — group header, members distributed horizontally, one join back
  to the main sequence;
- **fan_out** — lane group distributed horizontally, visually distinct from
  process-level parallelism, followed by an explicit merge node;
- **named workflow lane** — one collapsed workflow-reference node inside its
  lane;
- **guard on an ordinary step** — an `if` marker on the node; do not draw a
  second branch merely to show skip-and-continue;
- **`condition`** — explicit true continuation and false edge to a sequence END;
- **`loop`** — a framed body with an exit edge and a routed back-edge from the
  body end to the loop header;
- **`break`** — true edge exits the enclosing loop and false continues through
  the body.

Sibling order is source order. Equal input always produces equal coordinates
and routes. The first implementation optimizes for readable structure, not
minimum area or globally optimal crossings.

**Beat:** a generic force-directed layout. It is nondeterministic without extra
constraints, wastes the ordering information the workflow language already
provides, and moves nodes between renders in a way that is hostile to keyboard
navigation and editing.

### 6. Color is decoration; topology is encoded in characters and labels

*2026-08-18.* Node type, branch meaning and selection must remain readable with
styles stripped. Box shapes, connector glyphs, labels such as `true`, `false`,
`merge`, and compact type badges carry meaning. Lip Gloss colors then reinforce
it.

Long names are truncated inside a bounded node width with the full label
available in the inspector/status area when selected. The renderer measures
terminal display width, not byte or rune count.

If Unicode box drawing is unavailable in a future terminal-compatibility mode,
all structural glyphs are centralized so an ASCII palette can be substituted
without changing layout.

### 7. The viewer already has selection and focus, but no mutation commands

*2026-08-18.* `WorkflowGraph` stores a selected node and viewport offset from day
one. Read-only mode supports keyboard focus, spatial selection and panning. Its
internal mode enum reserves the interaction seam a builder will need, e.g.
`ModeView` now and later `ModeInsert`, `ModeMove` or `ModeConnect`; 017 does not
implement those mutation modes.

The workflows takeover keeps its current list navigation. Expanding a workflow
shows the graph; an explicit focus action transfers input to the graph viewport,
and `Esc` returns one layer to the workflow list before a later `Esc` leaves the
takeover. This preserves Vincent's one-layer-per-Escape rule.

Selection movement is geometry-aware rather than source-index-only: a right
move from one fan-out lane should prefer the nearest node to its right, while a
down move follows the nearest node in the next rank. Tab/shift-tab may provide a
deterministic source-order fallback.

Mouse support is optional convenience, not required acceptance for 017.

### 8. Narrow terminals crop the canvas; they do not switch to a different model

*2026-08-18.* The graph has one layout model at every terminal size. A viewport
crops/pans a larger scene instead of reflowing branches into a different
semantic shape. This keeps selection stable when resizing and prevents the
future builder from having two coordinate systems.

At widths too small to make a node readable, the workflows screen may show a
short "terminal too narrow for graph" hint plus the existing textual metadata,
but it must not silently misrepresent topology by flattening the graph.

### 9. Builder support is a constraint on interfaces, not scope creep

*2026-08-18.* 017 does not edit workflows, but every boundary must permit it:

- `Diagram` uses explicit nodes/edges/groups rather than render-only strings.
- Node identity does not depend on coordinates.
- Layout accepts a diagram and options and returns a scene; it does not mutate
  the workflow definition.
- Rendering is a pure projection of the scene plus visual state.
- `WorkflowGraph` owns focus/selection separately from the workflow definition.
- API detail data is rich enough that a later builder does not need a second
  read path.

A future builder task may add an editable intermediate workflow document,
validation calls, YAML serialization and mutation commands. It must not need to
replace the viewer's topology/layout/rendering pipeline.

**Beat:** implementing add/delete/connect now "because the types are there".
Editing raises separate questions — preserving comments and ordering, invalid
intermediate states, validation timing, undo/redo, write concurrency and file
ownership — that deserve their own decisions rather than being smuggled into a
visualization task.

## Proposed package boundary

The names are illustrative; the dependency direction is binding:

```text
internal/apiclient
    │ WorkflowDefinition DTO
    ▼
internal/tui/workflowgraph
    ├── diagram.go   semantic graph + Build
    ├── layout.go    deterministic coordinates/routes
    ├── render.go    Scene -> terminal content
    └── model.go     Bubble Tea component + viewport/focus
            │
            ▼
internal/tui/workflows.go
```

`diagram.go` and `layout.go` must not import `tea`, `lipgloss` or `viewport`.
`render.go` may import Lip Gloss. `model.go` owns Bubble Tea/Bubbles interaction.

The server-side DTO lives in `internal/api`, with the corresponding client DTO
and request method in `internal/apiclient`. The TUI never imports
`internal/workflow` to bypass that contract.

## Acceptance examples

The test corpus needs at least these shapes, using compact workflows built in
tests rather than screenshots of hand-written ANSI:

1. three ordinary sequential steps;
2. guarded ordinary step that skips and rejoins the same sequence;
3. `condition` with true continuation and false END;
4. `parallel` with three members and a join;
5. `fan_out` with inline lanes, named-workflow lane and merge;
6. counted loop with a body and back-edge;
7. loop containing a `break` exit;
8. nested structure that is currently legal, especially loop + parallel or
   fan-out + guarded lane;
9. labels containing wide Unicode characters;
10. a graph wider and taller than its viewport, proving both-axis navigation.

Tests assert semantic graph contents and geometry invariants before asserting
rendered text. Render tests strip ANSI where color is not the subject. No test
should depend on a specific color profile.

## Tasks

- [ ] **017.1 — Full workflow-definition API and client model.** Add the
  collision-safe definition endpoint, server DTO, apiclient DTO/method and API
  tests. Preserve project/global shadowing, built-ins and registry findings.
- [ ] **017.2 — Semantic diagram builder.** Depends: 017.1. Add the
  `workflowgraph` diagram types and conversion from the API definition. Cover
  sequence, guards, condition, parallel, fan-out/merge, workflow references,
  loop and break with unit tests for stable ids and edges.
- [ ] **017.3 — Deterministic layered layout.** Depends: 017.2. Place nodes,
  groups and orthogonal connector routes top-to-bottom with source-ordered
  siblings. Add geometry/invariant tests including nested structures.
- [ ] **017.4 — Terminal renderer.** Depends: 017.3. Render nodes, groups,
  routed connectors, labels and selection with display-width-safe truncation;
  keep semantic meaning visible without color.
- [ ] **017.5 — Reusable `WorkflowGraph` Bubble.** Depends: 017.3, 017.4. Add
  viewport sizing, two-axis pan, focus, selection and deterministic keyboard
  navigation in read-only `ModeView`.
- [ ] **017.6 — Workflows-screen integration.** Depends: 017.1, 017.5. Fetch a
  definition only for the selected/expanded workflow, show loading/error states,
  preserve the existing editor action, obey one-layer-per-Escape, and handle
  resize/narrow-terminal behaviour.
- [ ] **017.7 — Integration and regression tests.** Depends: 017.6. Add TUI and
  live-API coverage for definition loading, registry reloads, project shadowing,
  focus transitions, resize and large graphs. Run the normal race-enabled test
  suite and the three-platform CI matrix; do not mark done from assumed
  portability.
- [ ] **017.8 — Behaviour docs and acceptance walkthrough.** Depends: 017.7.
  Amend `docs/spec.md`, `docs/reference/api.md` and `docs/guides/tui.md` in the
  same change as the implemented behaviour. Document keys and graph semantics,
  then manually inspect representative sequential/parallel/fan-out/condition/
  loop workflows in the TUI before closing the task.

## Future trigger: TUI workflow builder

Open a separate task when the viewer has shipped and the first real editing
workflow is chosen. That task should start from the `WorkflowGraph` component
and answer the questions 017 intentionally leaves open: editable document
model, serialization, validation of incomplete states, undo/redo, write API/file
ownership, conflict detection, and the exact insert/move/connect interaction.

The success criterion for this design is simple: that task should add mutation
semantics around the graph, not replace the graph.
