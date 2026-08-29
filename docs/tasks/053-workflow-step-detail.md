# 053 — A full step-detail modal in the workflow graph

**Status:** ✅ done (5/5)
**Spec:** amends §15 (Workflow graph)
**Supersedes:** task 017 decision 15's "the escape hatch for reading a prompt
is `e`", and decision 13's parenthetical about not wanting a third Escape
layer in one change. Everything else in both decisions stands.

## Problem

The graph (task 017) draws a workflow's control flow correctly and is the only
view of a definition that cannot say what a step *is*. Node boxes truncate at
their inner width; the inspector strip packs two lines with `packRow` and drops
the rest with no sign that it did, so a step with no `timeout` reads exactly
like one whose `timeout` did not fit; and `prompt`, `run`, `env`,
`instructions`, `permission_mode`, `input_timeout`, `check_timeout`,
`max_parallel`, `count`/`for_each` and `max_iterations` were never emitted at
all, though `apiclient.WorkflowStepDef` carries every one. A step that sets no
`agent:` runs `defaults.agent` (§8.6) and the graph showed neither the value nor
the fact that it was inherited. `enter` was unbound in `ctxWorkflowGraph` — the
obvious gesture for "tell me about this node" did nothing.

Reading a prompt therefore meant `e`: the whole file in `$EDITOR`, and the
picture you were reading abandoned to answer a question about one node.

## Decisions

**1. A bordered popup over the graph, not a third in-place layer.**
*(2026-08-29)*

It reuses `frame()` + `overlay()` at the geometry the answer, repair and
follow-up popups already use — width `min(bodyW-6, 120)`, centred, the picture
still visible around it. That keeps 017 decision 13's "Escape closes one layer
at a time" literally true (modal → graph → list → home) and makes the modal the
same kind of object `ctxForm`, `ctxRepairForm` and `ctxFollowUpForm` already
are, rather than inventing a second layering idiom inside one takeover.

**Beat:** a full-body layer. It buys about six columns of reading width and
costs the reader the graph they opened the node from.

**Beat:** choosing between the two by terminal size. That is exactly the
two-layouts-one-feature shape decision 8 refuses.

**2. A field nothing sets does not appear.** *(2026-08-29)*

A value authored on the step shows unmarked; a value the step leaves empty and
`defaults` supplies shows the effective value marked as inherited; a field
neither the step nor `defaults` sets is omitted entirely. The modal never
prints the daemon's own run-time fallback. That is decision 12's rule (the DTO
is as-authored, and folding a default into the step it was inherited by
destroys the §8.6 distinction) and decision 18's precedent (`max N` is drawn
only when the file sets it) applied to a longer surface.

The agent-shaped defaults — `agent`, `model`, `effort`, `permission_mode`,
`on_input`, `input_timeout` — are inherited only by steps that run an agent,
and retries and timeouts only by steps that run at all. `agent: claude
(inherited)` on a `command` step would state something the run will never do.

**Beat:** resolving the values properly. It would mean a second
`POST /v1/resolve` fetch and a second source of staleness inside a layer whose
whole caching posture (decision 19) is "fetch it fresh, never cache it".

**Beat:** showing every applicable field with an em dash for the unset ones,
which would make an agent step's modal mostly empty rows.

**3. The inspector strip is untouched, bytes included.** *(2026-08-29)*

The strip stays the glance view exactly as it renders today; the new `enter`
row in the footer and the `?` overlay is the affordance that says there is
more. `TestGraphRendersIdenticallyWithNoModalOpen` holds the whole layer to
byte equality with the modal shut, and `TestStripDetailIsUnchanged` holds the
strip's rows over the whole corpus.

**Beat:** an overflow marker (`+3 more`, a trailing `…`). It spends strip
width, changes the golden renders, and duplicates what the footer now says on
every node. It is a follow-up if a reader misses the key in practice.

**4. `enter` is never inert.** *(2026-08-29)*

Every node opens something: an authored step shows its fields; a `merge` node
its conflict policy and, when set, its resolver agent (itself a
`WorkflowStepDef`, shown as one); a collapsed `workflow_ref` names the workflow
it stands for and whether it becomes a child task (lane) or spliced steps
(include); a `parallel`/`fan_out`/`loop` group header shows `max_parallel` or
the loop's driver and bounds; the `#end` node says the workflow ends there.
That is a property, held by a test over every corpus fixture, not a case list.

`TooNarrow` still wins: below `MinWidth()` the layer shows its existing hint
and `enter` opens nothing, because there is no node drawn to open.

**5. While the modal is open it owns the keyboard.** *(2026-08-29)*

Scroll and pager keys move it, `esc` closes it back to the graph with the same
node selected. `e` and `R` carry one layer further, for the reason decision 13
carried them into the graph: `e` is the editing path from wherever you are
reading, and `R` is the layer's only recovery. A refetch — from `R` or from a
live `workflow.registry_changed` — re-renders an open modal from the new
definition when the node id still exists and closes it back to the graph when
it does not, which is decision 19's selection rule extended one layer.
Reopening resets the scroll offset, for decision 19's reason.

**6. The pure stages stay pure.** *(2026-08-29)*

`diagram.go` builds the full detail as data — sections of labelled fields, each
with an `Inherited` flag, multi-line values carried unwrapped — and
`workflowrender.go` wraps and prints it at a width only it knows.
`boundary_test.go` still holds `diagram.go` and `layout.go` to importing
neither Bubble Tea nor Lip Gloss.

## Tasks

- [x] **053.1** Build the per-node full detail in `internal/tui/workflowgraph`
  as pure data, covering every field of `WorkflowStepDef`, `WorkflowLaneDef`
  and `WorkflowMergeDef`, with `defaults` marked as inherited. ✓ 2026-08-29
- [x] **053.2** Add the modal to the graph layer: `enter` opens, `esc` closes,
  the modal owns the keyboard, `e`/`R` carry through, a refetch re-renders or
  closes it. ✓ 2026-08-29
- [x] **053.3** Render it as a bordered popup over the unchanged graph, with
  wrapping, scrolling and the workflow-level header. ✓ 2026-08-29
- [x] **053.4** Register `ctxWorkflowStep` and the `enter` row, with probes in
  `bindings_test.go`. ✓ 2026-08-29
- [x] **053.5** Spec §15 amendment, `docs/guides/tui.md`, the gate corpus entry
  and its walk, and the screenshot tape. ✓ 2026-08-29
