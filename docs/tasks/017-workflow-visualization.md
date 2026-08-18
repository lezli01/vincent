# 017 — Workflow visualization in the TUI

**Status:** 🚧 in progress (4/9) · **Opened:** 2026-08-18

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
  recursive rendering. *Clarified 2026-08-18: that navigation is itself out of
  scope. The node names its workflow and `enter` on it does nothing — a back
  stack would land a third Escape layer in the same change as the first two,
  and the referenced workflow is one `esc` and a cursor move away in the list.*
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

*Superseded 2026-08-18 by decision 10: the name moved into the query string,
because a registry name is neither URL-safe nor unique. The rest of this
decision stands.*

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

### 10. The definition endpoint takes the name as a query parameter

*2026-08-18.* The endpoint is
`GET /v1/workflows/definition?name=<name>&project_id=<id>`, not
`/v1/workflows/{name}/definition` as decision 4 first wrote it.

Two registry facts make a name a poor path segment. A workflow name is only
validated to exclude whitespace and path separators on a file that *parses*
(`workflow.go`); a file that does not parse is still listed, under
`fallbackName` — its declared `name:` if that much decodes, otherwise the
file's base name — so a broken entry's name may contain anything at all. And
a name is not unique within a scope: the first file wins a duplicate and the
loser is listed as a second entry under the same name (`registry.go`), which
`Registry.Lookup` cannot reach.

The endpoint therefore serves what `Lookup` serves — the shadowing winner —
and the response carries `file` and `scope` so a client can tell which entry
it got. A duplicate-name loser has no definition to draw; it has findings,
which the list already carries.

**Beat:** the path parameter. It would need escaping rules for exactly the
broken entries decision 4 says must stay addressable, and it puts a name in
the position where a stable identifier belongs.

**Beat:** addressing by scope + file path, which is the only truly unique key.
It is unique and unusable: built-ins have no file, and every caller would have
to list before it could fetch.

### 11. The definition response is an entry envelope, and a broken workflow is a 200

*2026-08-18.* The body is the registry entry — `name`, `scope`, `file`,
`platforms`, `platform_supported`, `requires_input`, `errors`, `warnings` —
with the recursive definition under `definition`, which is null when the file
did not parse. A 404 is reserved for the one honest case: no entry of that
name in that project's view of the registry.

Repeating the list's derived fields is deliberate. The graph layer is opened
against one workflow and must not have to join against a list row it may not
hold; `RunsHere()` and `NeedsInputAgent()` have already computed them, and
this is a per-workflow, on-demand call.

**Beat:** a 422 for a workflow that does not parse. It would make the client
treat "this file is broken" as a transport failure, when the list's own rule
(§13.2) is that a broken file is shown with its errors rather than hidden.

### 12. The DTO is full-fidelity and as-authored; defaults stay a separate block

*2026-08-18.* "Normalized" in decision 4 means *recursively structured*, and
nothing more. Steps are reported as the file writes them, with the workflow's
`defaults` alongside them; the DTO never folds a default into the step that
inherited it.

Folding is lossy in the one direction that matters: `agent: claude` written on
a step and `agent: claude` inherited from `defaults` become indistinguishable,
which is precisely the distinction §8.6 exists to make and precisely what a
future builder must round-trip. The resolved answer already has an endpoint —
`POST /v1/resolve`, which the workflows view calls today.

Prompts and `run:` bodies are included even though 017's UI does not show them
(decision 15). The cost decision 4 was avoiding was paying for every workflow
in a hot list; this call is for one workflow, on demand. A structure-only DTO
would be the second read path decision 9 forbids.

**Beat:** a visualization-sufficient subset. It would ship 017 slightly
smaller and hand the builder task a DTO revision as its first job.

### 13. The graph is a sub-layer of the workflows list, not a routed screen

*2026-08-18.* `workflowsView` gains a nullable `graph` field, opened with `g`
and rendered in place of the list while set — the shape `projectsView` already
uses for `form`. The existing `enter` text expansion is untouched.

The expansion carries findings, platform notes and §8.6 resolution that the
graph does not show, and it is the surface `e` and `R` act on; replacing it
would make 017 a regression for broken workflows. Escape closes one layer at a
time: graph, then list, then home.

`e` and `R` carry into the layer. `e` opens the graphed workflow's own file,
which with decision 18's live reload makes edit-save-watch the loop the
feature is for; `R` refetches this one definition, which is the layer's only
recovery from a failed fetch. Both register in the binding registry under a
new `ctxWorkflowGraph` context, so the footer and the `?` overlay stay honest.

**Beat:** a new `viewID` takeover routed by the root. It would add a screen to
the view table that the command palette cannot offer and that is reachable
only from the workflows list — a routed screen that is not routable.

**Beat:** replacing the text expansion with the graph, or stacking both inside
it. Both need the list to scroll first (017.9), and the graph wants the full
width and height a layer gives it.

### 14. Selection moves and the viewport follows

*2026-08-18.* Arrows and `hjkl` move the *selection*; the viewport follows via
`viewport.EnsureVisible`. `shift+arrows` pan explicitly, `tab`/`shift+tab` are
the source-order fallback decision 7 anticipated, and the viewport's own pager
keys come along. Bubbles v2's viewport supports both axes (`SetXOffset`,
`ScrollLeft`/`ScrollRight`, `SoftWrap: false`), which is what makes decision 8's
crop-and-pan model available without hand-rolled offset arithmetic.

Mouse support is click-to-select plus the wheel. The wheel is free from the
viewport's `Update` and would be conspicuously missing in a scrollable surface
when every other scrollable pane in the TUI has it; click-to-select needs only
the cell-to-node hit test that selection requires anyway. Drag-to-pan is a
stateful gesture that belongs to an editor.

**Beat:** arrows panning with `tab` as the only way to select. Panning is not
what the reader is doing; selecting is.

**Beat:** a pan/select mode toggle. That is the mode machinery decision 7
deliberately reserved rather than spent.

### 15. Presence on the node, content in the inspector

*2026-08-18.* A node carries compact badges for the things that exist —
`if` for a guard, `chk` for a `check:` field, `on_conflict: agent` on a merge
— and never their text. The inspector is a fixed 2–3 line strip at the bottom
of the layer, above the global footer, showing the full label, type, step id,
guard expression, check command, merge policy and loop bounds.

A guard is a Go template; truncated into a 22-column node it reads
`{{ eq .Steps.c…`, which tells a reader less than a clean `if` badge does.
`condition` steps are the exception by type: their expression is their whole
meaning, and their edges are labelled `true` and `false`.

The strip deliberately does *not* show prompts or `run:` bodies. They are in
the DTO for the builder's read path (decision 12), and the escape hatch for
reading them is `e`, which the non-goals keep as the editing path.

**Beat:** a right-hand inspector pane. Horizontal columns are the scarcest
resource for a top-to-bottom graph in an 80-column terminal, and a pane would
fight decision 8's rule that a narrow terminal crops rather than reflows.

**Beat:** rendering `check` as an attached box. A failing check sends the step
into retry and then a block — failure handling, which the graph does not draw
— and a box would re-open the settled question of `check` being a type.

**Beat:** drawing `merge.agent` as a node. It runs only on conflict, so a node
in the main flow implies a step that usually does not happen; that is the same
false-branch noise decision 5 refused for guards.

### 16. One END per top-level sequence; nested sequences route to their own structure

*2026-08-18.* Every workflow's top-level sequence ends in exactly one END
node, whether or not a `condition` targets it. Nested sequences get none: a
`condition` inside a loop body ends *that iteration* (§7.8) and routes to the
loop header, a `break`'s true edge routes to the loop's exit, and a guarded
member of a `parallel` group simply does not start, which per decision 5 draws
no branch at all.

One terminal node in the whole diagram is also the anchor a later runtime
overlay wants for "reached".

**Beat:** an END per sequence, nested ones included. It draws terminators for
sequences that do not terminate the workflow, which is the one thing a
control-flow picture must not get wrong.

**Beat:** no END unless a condition needs one. Then "the workflow finishes
here" is implied by running out of boxes, and the false edge of a condition
has nowhere honest to land.

### 17. Fixed node width, and a narrow-terminal threshold derived from it

*2026-08-18.* Every node is the same width — a layout option defaulting to
about 22 columns — so ports sit at predictable columns and orthogonal routing
stays simple. A fan-out drawing four lanes into one merge reads correctly only
on a regular grid.

Decision 8's "too narrow for a graph" threshold is computed from that width
plus its gutter (about 26 columns), never hardcoded, and is checked on width
only: height is what the viewport is for.

**Beat:** content-sized nodes with a cap. Tighter for short names, and it
costs the saving back the first time a `parallel` group's three members are
three widths and its join legs are three lengths.

**Beat:** a fixed threshold like 40 columns. A nested group at 30 columns is
cramped but true, and decision 8's rule is that we never misrepresent
topology, not that we refuse to show a tight one.

### 18. `parallel` and `fan_out` differ in frame and in badge

*2026-08-18.* A `parallel` group is drawn with a light frame and a `parallel`
badge; a `fan_out` with a heavier frame and a `fan_out` badge. Both are
required by decision 6: the badge is what survives a screenshot pasted into an
issue, the frame is what reads at a glance while scanning.

The strongest signal costs nothing either way and is structural: `fan_out`
always has a merge node beneath it and `parallel` never does, because one
creates child tasks with branches to join and the other runs processes in the
task's one worktree.

A `loop` or `parallel` header shows its driver as authored — `loop ×3`,
`loop for_each`, and `max N` only when the file sets it, never the daemon's
fallback, for decision 12's reason. A `for_each` list is discovered at run
time, so a definition viewer cannot know how many iterations it becomes; the
body is drawn once with a back-edge regardless.

**Beat:** distinguishing the two by color alone, which decision 6 already
forbids, or by badge alone, which vanishes at a glance.

### 19. No cache, no memory across viewings, and a live reload re-lays-out in place

*2026-08-18.* The definition is fetched on every open — loading line in the
layer, error with `R` to retry — and never cached. The invalidation rule a
cache would need is "drop it whenever the registry changes", and the registry
changing is exactly when someone is sitting in this view editing files, so the
cache would be cold whenever it mattered. This is a localhost call for one
workflow.

While the layer is open, a `workflow.registry_changed` event refetches and
re-lays-out in place, keeping the selected node if its id still exists and
falling back to the entry node if it does not. Selection survives because
decision 3 forbids coordinates from being identity and layout is
deterministic, so an unchanged region does not move. That is continuity within
one viewing, and it is the cheapest real test of decision 3's premise.

Reopening the layer resets selection to the entry node. Remembering it is
state the daemon does not have, and it would need the same invalidation dance
the view's `resolutions` cache already performs.

**Beat:** freezing the graph with a "changed on disk" note. Live reload
reflecting saves immediately is the workflows view's stated §15 behaviour, and
a graph that goes stale while you edit is the wrong half of that promise.

### 20. No CLI command and no configuration in 017

*2026-08-18.* `vincent workflow` keeps `ls` and `validate`; the definition
endpoint gets no CLI counterpart. The useful rendering of a workflow
definition is the graph, which lives in `internal/tui/workflowgraph` — serving
it from the CLI would either invert the dependency direction or require a
second renderer, and a JSON-only `show` is a `curl` with extra steps in a
system where curl is a first-class client. Revisit once the renderer exists
and could be reused headlessly.

Node width and the ASCII glyph palette stay layout options, not `config.yaml`
settings. Decision 6 already ruled the ASCII palette a future
terminal-compatibility mode rather than a user preference, and a width knob is
a setting nobody can choose well before using the default — unlike
`tui.board.group_by`, which existed because there was no defensible single
answer. A later setting reads from the same layout struct.

**Beat:** shipping `vincent workflow show` for symmetry with the other
endpoints. Symmetry is not a use case.

### 21. Golden files with invariants, and a walkthrough rather than a script

*2026-08-18.* Rendered output is asserted against golden files under
`internal/tui/workflowgraph/testdata/`, ANSI stripped, refreshed with an
update flag — the artifact a reviewer actually reads when a layout changes,
and the same fixture convention the adapter parsers use. Ten multi-line
canvases inline would swamp the test bodies.

Golden files alone can be refreshed into blessing a wrong picture, so they are
paired with invariant assertions that hold regardless of styling: a
back-edge's target, a join's column, rank ordering, node identity across
re-layout.

Acceptance adds `docs/gates/017-workflow-graph.md`: the ten shapes as real
workflows someone can point the TUI at, plus the run record the other gate
documents keep. There is no gate *script*. What is being judged is whether a
picture reads correctly, which is the judgement `m3-gate.sh` already declines
to automate; a script would assert over curl what the golden files assert
directly, more slowly.

### 22. Delivery: one pull request, four ordered commits

*2026-08-18.* The work lands as one pull request whose commits are the
reviewable units: the API and client model; the pure diagram/layout/render
pipeline; the `WorkflowGraph` component and its screen integration, with
017.9 as its own commit; then the documentation.

**Beat:** three sequential pull requests. The reviewable-unit benefit is what
ordered commits already give, and staging would idle the work between merges
for a feature whose stages are days apart, not weeks.

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

- [x] **017.1 — Full workflow-definition API and client model.** ✓ 2026-08-18 Add
  `GET /v1/workflows/definition?name=&project_id=` (decision 10), the server
  DTO, the apiclient DTO/method and API tests. Preserve project/global
  shadowing, built-ins and registry findings; a broken workflow answers 200
  with findings and a null definition (decision 11).
- [x] **017.2 — Semantic diagram builder.** ✓ 2026-08-18 Depends: 017.1. Add the
  `workflowgraph` diagram types and conversion from the API definition. Cover
  sequence, guards, condition, parallel, fan-out/merge, workflow references,
  loop and break with unit tests for stable ids and edges.
- [x] **017.3 — Deterministic layered layout.** ✓ 2026-08-18 Depends: 017.2. Place nodes,
  groups and orthogonal connector routes top-to-bottom with source-ordered
  siblings. Add geometry/invariant tests including nested structures.
- [x] **017.4 — Terminal renderer.** ✓ 2026-08-18 Depends: 017.3. Render nodes, groups,
  routed connectors, labels and selection with display-width-safe truncation;
  keep semantic meaning visible without color.
- [ ] **017.5 — Reusable `WorkflowGraph` Bubble.** Depends: 017.3, 017.4. Add
  viewport sizing, two-axis pan, focus, selection and deterministic keyboard
  navigation in read-only `ModeView`.
- [ ] **017.6 — Workflows-screen integration.** Depends: 017.1, 017.5. Open the
  graph with `g` as a sub-layer of the list (decision 13), fetch its definition
  on every open with loading/error states (decision 19), keep `e` and `R` inside
  the layer under a `ctxWorkflowGraph` binding context, suppress `g` on an
  entry already known invalid, obey one-layer-per-Escape, and handle
  resize/narrow-terminal behaviour.
- [ ] **017.7 — Integration and regression tests.** Depends: 017.6. Add TUI and
  live-API coverage for definition loading, registry reloads, project shadowing,
  focus transitions, resize and large graphs. Run the normal race-enabled test
  suite and the three-platform CI matrix; do not mark done from assumed
  portability.
- [ ] **017.8 — Behaviour docs and acceptance walkthrough.** Depends: 017.7.
  Amend `docs/spec.md`, `docs/reference/api.md` and `docs/guides/tui.md` in the
  same change as the implemented behaviour. Document keys and graph semantics,
  add `docs/gates/017-workflow-graph.md` carrying the acceptance corpus and its
  run record (decision 21), then walk it in the TUI before closing the task.

- [ ] **017.9 — A viewport for the workflows list.** Depends: 017.6. Discovered
  while designing the graph layer: `workflowrender.go` joins every line into one
  string and never crops, so a registry taller than the terminal is truncated
  today — before any graph exists. Give the list the viewport the detail, daemon
  and diff panes already use, keep the cursor revealed as it moves, and return
  to the graphed row on `esc`.

## Future trigger: TUI workflow builder

Open a separate task when the viewer has shipped and the first real editing
workflow is chosen. That task should start from the `WorkflowGraph` component
and answer the questions 017 intentionally leaves open: editable document
model, serialization, validation of incomplete states, undo/redo, write API/file
ownership, conflict detection, and the exact insert/move/connect interaction.

The success criterion for this design is simple: that task should add mutation
semantics around the graph, not replace the graph.
