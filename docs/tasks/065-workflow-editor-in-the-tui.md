# 065 — A workflow editor in the TUI: create, edit and fork

**Status:** ✅ done (13/13) · **Issue:** [#261](https://github.com/lezli01/vincent/issues/261)
· **Spec:** §5.2, §8.2, §12.3, §13.2, §13.4, §15 view 5, §19's M3 footnote

The workflows view read the registry and did not author it. `e` handed the
terminal to `$EDITOR` on raw YAML and that was the whole authoring story, which
left the workflow — the object vincent is built around — as the one first-class
thing a user could not author from the product. To add a step you had to know
§8.2 by heart, and the loop was save → reload → read the badge → back to vim: a
malformed file was reported, not prevented.

## The recorded decision this reverses, and why that is safe

"Creating a workflow file from the TUI is out of v1" is a **PR M decision**
(T3.6, grill session 2026-08-09, `docs/history/v0-tasks.md`), surfaced into §15
view 5 and §19's M3 footnote because PR M held that key decisions — the negative
ones included — belong in the spec rather than only in the ledger. It is binding
and it is not being relitigated silently. It is retired because **every blocker
it names has since been removed.** In its own words: *"Creation needs a
server-exposed global workflows directory (`GET /v1/config` does not expose
one), a filename prompt and a starter template; §15 view 5 asks for list + `e` +
live reload and nothing more."*

- A **starter template** exists: `workflow.SkeletonSource` and the `--from`
  examples (task 034).
- A **server-exposed directory** is not needed at all. The write endpoint takes
  `{scope, project_id, name}` and the daemon resolves the path itself, which is
  the ownership invariant CLAUDE.md states and the shape task 060 used for
  `config.yaml`.
- A **filename prompt** is a form row.

Task 060 supplies the affirmative argument: a file the daemon owns and already
hot-reloads, which a human may edit by hand at any moment, is a different object
from the process supervising the TUI. The negative that still stands — stop and
gc — is untouched. PR M's other workflow-view decision (*"`e` edits the real
file in place and then does nothing… no `POST /v1/workflows/validate` on the
buffer — the view waits for `workflow.registry_changed`"*) is **unamended**:
validation moved to the daemon endpoint, not into the TUI, and `e` behaves
exactly as it did.

## Decisions

1. **The writer is line-oriented, not a YAML round trip.** The two precedents in
   this repository are both text edits, for the reason `SetName`'s doc states
   outright: a marshal round trip drops every comment. `config.Apply` (task 060
   decision 9) walks lines with an indentation stack; goccy's AST does not
   re-emit untouched bytes identically, and blank lines are not tokens.
   `workflow.Edit` extends that approach to the structural operations config
   never needed — insert and remove sequence items, create a nested block, write
   a `|` block scalar, reorder steps. **"An untouched region comes back
   byte-identical" is a hard acceptance criterion** and is asserted on bytes, in
   `internal/workflow/edit_test.go` and again in `scripts/m13-gate.sh`.

2. **The wire carries edit operations, not YAML.** This follows from decision 1
   and is the ownership rule made concrete: a client that sent a whole document
   would have had to discard the comments to build it, and the daemon would have
   nothing to preserve. So `PATCH /v1/workflows` carries `{ version, ops[] }`,
   each op a path plus `set` / `insert` / `remove` / `move` —
   `config.Apply`'s dotted-path assignment widened with list indices
   (`steps[2].prompt`, `steps[1].steps[0].id`,
   `steps[3].lanes[0].merge.on_conflict`). An `insert` names its new entry's
   keys and the *daemon* renders them. `POST /v1/workflows` writes
   `SkeletonSource` with `SetName` applied, or copies a fork source's bytes
   verbatim, so **create puts no YAML on the wire either**. The daemon holds the
   original bytes end to end; the TUI never composes YAML; `create-workflow`'s
   built-in prose is untouched.

3. **The schema is served, not re-derived.** "The form knows the schema" means
   §8.2's table — including its context-sensitive rules: a `parallel` sub-step
   may not be `manual`/`parallel`/`fan_out`/`condition`/`loop`, `condition` and
   `include` reject nearly every other field, `break` is valid only inside a
   loop body, `merge.agent` belongs to `on_conflict: agent` — must be readable
   by a client. PR L recorded that re-deriving the daemon's checks in the TUI is
   how the two drift. So `GET /v1/workflows/schema` serves a machine-readable
   descriptor and the TUI renders forms from it. `TestSchemaMatchesValidation`
   walks the descriptor against `validateStep` itself: every field a type offers
   must survive that type's `rejectFields` call and every field it withholds
   must be refused by it, so drift in either direction fails. Agent, model and
   effort value sets are **not** in the descriptor — they come from
   `GET /v1/agents` (§9.6), which the new-task form already consumes.

4. **Stale writes are refused, scoped to workflows.** Task 060 decision 6
   rejected preconditions for `PATCH /v1/config` — *"a precondition concept no
   other endpoint in this API carries, for a race between a human and
   themselves."* That stands for config, unamended. Workflows get a version
   token (mtime + sha256) and a 409 because the second writer is not the same
   human: the `create-workflow` built-in writes the **live registry directory**
   from an agent run (§5.2), `$EDITOR` is one key away in the same view, and a
   structured form stays open across a whole editing session. A scoped
   extension carrying that reasoning, not a reversal.

   *Correction to the issue:* it names `update-workflows` as a third live
   writer. Per §5.2 it is not — its deliverable is the task's own worktree and
   branch, merged like any other diff. The argument rests on `create-workflow`,
   `$EDITOR` and external editors, which is enough.

5. **The write routes are excluded from MCP.** They join `mcp.Excluded` beside
   `PATCH /v1/config`, under task 057 decision 4's wording: an agent must not
   reconfigure the daemon supervising it, and a workflow file is what that
   daemon runs. Nothing regresses — `create-workflow` writes through the
   filesystem, not the API. The schema read route is an ordinary tool. The
   route-table parity test forces both, so neither can drift.

6. **`e` keeps meaning `$EDITOR`.** It means that in all seven contexts in
   `bindings.go`; taking it for the structured editor would give one key two
   meanings depending on the view. The structured editor takes new keys in the
   workflow contexts: `a` create (the projects view's `a` add), `f` fork, `i`
   edit the entry under the cursor. `n` is unavailable — it is a global (new
   task); `E` is ruled out because it reads as `e`'s sibling and already means
   "$EDITOR, then retry" in the task-action scope.

7. **One task record, one branch, one PR.** The writer, the schema descriptor
   and the forms are one design; reviewing them apart hides the seams, and the
   coverage argument — a form covering half the schema sends you back to vim for
   the other half — only holds if they land together.

8. **New files are 0644, not config's 0600.** A project workflow is meant to be
   committed and shared with a team (§5.2) and carries no secret. An existing
   file keeps its own mode: the daemon is not the authority on a file a
   repository owns.

9. **Two behaviours are inherited rather than invented.** A create whose `name:`
   is already declared in the target scope is refused as the §5.2 duplicate it
   would be (`workflow.DeclaredName` already answers this), and a fork keeps the
   source's `name:`, because keeping it is what makes it shadow.

## Deliberately not amended

`skills/vincent-workflows/SKILL.md`, `internal/workflow/builtin.go` and
`update-workflows`' checklist. CLAUDE.md's rule fires when *a workflow feature*
lands; this adds no field, no step type and no semantics — there is nothing new
for a workflow author to be taught or for the built-in to propagate. Recorded
here explicitly so the next reader sees it was considered rather than missed.

## Sub-tasks

| # | What | Status |
|---|---|---|
| 065.1 | `internal/workflow/edit.go` — the line-oriented document editor: path resolution with list indices, `set`/`insert`/`remove`/`move`, block scalars, atomic write at 0644 | ✅ |
| 065.2 | Fidelity tests on the bytes: header and trailing comments, blank lines, a `|` block scalar, CRLF, insert/remove/reorder, nested-block creation | ✅ |
| 065.3 | `internal/workflow/schema.go` — §8.2 as data, with the nesting contexts | ✅ |
| 065.4 | The drift test: the descriptor walked against `validateStep`, plus coverage of every `Step`/`Workflow`/`Lane`/`Merge`/`FieldDefinition` yaml field and every step type | ✅ |
| 065.5 | `Registry.Destination`, `Registry.Version`, `workflow.FileName`, `workflow.BuiltinSource` | ✅ |
| 065.6 | `internal/api/workflows_write.go` — `POST`, `PATCH`, the schema route, the version token, the 409, per-daemon write serialization | ✅ |
| 065.7 | `version` added to the list and definition responses | ✅ |
| 065.8 | `internal/apiclient` — op, schema-descriptor and version wire types; `CreateWorkflow`, `PatchWorkflow`, `WorkflowSchema` | ✅ |
| 065.9 | `internal/mcp` — write routes into `Excluded`, schema route into `routes`; parity test updated | ✅ |
| 065.10 | The TUI editor: schema-driven row list, nested descent, enum rows and the `(unset)` stop, the stale-write offer, `inputCapturing` while a field is focused | ✅ |
| 065.11 | The create/fork prompt, and the three new binding rows with their probes | ✅ |
| 065.12 | `scripts/m13-gate.sh`, committed executable and wired on all three platforms | ✅ |
| 065.13 | Records: §5.2, §12.3, §13.2, §13.4, §15 view 5, §19's M3 footnote; `docs/reference/api.md`, `docs/guides/tui.md`, `docs/guides/workflows.md`, `docs/reference/workflow-schema.md`; `CHANGELOG.md` | ✅ |

## What the tests prove

- **Fidelity, on the bytes.** `internal/workflow/edit_test.go` edits one scalar
  three steps into a file with header comments, an inline trailing comment,
  blank lines between blocks and a `prompt: |` block scalar, and asserts every
  other line is identical; separately for an insert into `parallel.steps`, a
  lane, a `merge` block, a removal and a reorder, and again for CRLF.
- **Nothing on failure.** `TestWorkflowPatchRejectionLeavesTheFileAlone` sends
  five op sets the endpoint refuses — a `manual` under `parallel.steps`, a
  `break` outside a loop, `if` on an `include`, `merge.agent` without
  `on_conflict: agent`, an unresolvable path — and asserts the §13.1 envelope
  with the file byte-identical after each.
- **No drift.** `TestSchemaMatchesValidation`, `TestSchemaCoversEveryStepField`,
  `TestSchemaCoversEveryStepType` and `TestStepTypesForMatchesNestingRules`.
- **Stale write.** Two reads, one write, then the second write with the first
  version → 409 carrying the current version in `details`.
- **MCP.** The parity test asserts the eight exclusions by name; the gate proves
  it against a real MCP client's tool list.
- **TUI, against the real handlers over `httptest`**
  (`internal/tui/workfloweditorlive_test.go`): create in a scope, edit through
  to a registry reload with the file's comments intact, a fork producing a
  project entry, and a refused value rendered against its field with the value
  still visible.
- **Gate m13**, on all three platforms: schema, create, comment-preserving
  patch, fork-and-shadow, 409, a refused patch, and the MCP surface.

## Out of scope

No CLI counterpart — `vincent workflow init|validate|render` already cover that
surface. No delete: the view gains no destructive action. Editing from the graph
(task 017 / task 053) stays out — the graph is a read-only projection today, and
making it bidirectional is a second hard problem stacked on the first.
