# 043 — Persisting where a task's workflow definition came from

**Status:** ✅ done (6/6)
**Issue:** [#145](https://github.com/lezli01/vincent/issues/145)
**Spec:** amends §5.2, §5.3, §13.2, §13.3, §14

## Problem

A task records `workflow_name` and `workflow_snapshot` and **nothing about
where that definition came from**.

Registry precedence is project → global → built-in (§5.2), and a task created
without naming a workflow resolves to `adhoc`. So a repository that versions
`.vincent/workflows/adhoc.yaml` silently replaces the built-in for every task
created in it — including every task created by someone who never chose a
workflow at all. The snapshot is durable, so the *steps* that ran are
recoverable, but the answer to "did this run the built-in, or did this
repository substitute its own?" is not recorded anywhere and cannot be
reconstructed afterwards: looking the name up again reports today's registry,
not the one the task was created against.

The TUI's workflow picker shows a scope badge during selection, which helps a
human who is choosing. It does nothing for the implicit CLI/API path, and
nothing at all six months later.

## What shipped

One nullable JSON column, `tasks.workflow_origin_json` (migration 0017), written
once at creation beside `workflow_snapshot` and never recomputed:

| Field | Meaning |
|---|---|
| `scope` | `builtin`, `global`, `project`, or `derived` |
| `file` | the source path **relative to its scope root** — `.vincent/workflows/adhoc.yaml`, `workflows/release.yaml`; absent for a built-in |
| `digest` | `sha256:<hex>` over the registry entry's source bytes as loaded |
| `parent_task_id` | set only for `derived`: the fan-out parent whose snapshot the lane's steps came from |

Surfaced on every task the API serves (`workflow_origin`), in the `task.created`
event, as the `origin` row of `vincent task show`, and beside the workflow name
in the TUI's task-detail header.

## Decisions

**1. Resolution is unchanged — the substitution is made *visible*, not
prevented.** *(2026-08-28)*

The issue proposed a reserved `builtin:adhoc` identifier and a rule that
built-ins are not shadowed by name. That reverses a recorded phase 2 decision
(`docs/history/v0-tasks.md`: "`adhoc` becomes a **built-in registry entry** at
lowest precedence (a real file named `adhoc` in either scope shadows it), so
creation is one uniform path, `workflow` stays optional") and the §5.2 built-in
contract, which covers all three built-ins rather than `adhoc` alone. The author
was asked and chose to keep shadowing.

`firstNonBlank(req.Workflow, project.DefaultWorkflow, workflow.AdhocName)` and
`Registry.Lookup`'s project → global → built-in walk are therefore untouched.
No reserved namespace, no `allow_shadow_builtin` declaration, no `builtin:adhoc`
selector.

The qualified selector was the expensive half. It would be a new name grammar
four resolution sites must honour at once — `POST /v1/tasks`, `default_workflow`,
a `fan_out` lane's `workflow:` and an `include` step's `workflow:` — and the
current name pattern `^[a-z0-9][a-z0-9._-]*$` admits no colon, so it is a schema
change stacked on a resolution change. Issue acceptance criteria 1, 2 and 3 are
dropped; criterion 8 is honoured as *visibility* — two same-name workflows are
told apart on the task row — rather than as a new selector grammar.

**Beat:** reserving the built-in names, which would have been a silent
behaviour change for any repository already shipping its own `adhoc.yaml`.

**2. Three fields: scope, scope-relative file, digest.** *(2026-08-28)*

No repository HEAD commit: capturing one means a git call inside `POST
/v1/tasks`, plus a defined answer for a dirty tree and for a workflow file that
is not committed at all — cost and edge cases out of proportion to "which
definition ran".

No separate include-provenance chain: the names are already there. Expansion
records `resolved_from` per step in the snapshot and the API already returns it
(`internal/api/tasks.go`, asserted by `internal/api/include_test.go`). What is
genuinely missing is a file and digest *per included workflow*, and that is not
being added.

The file is **relative to its scope root** because an audit row outlives the
checkout it was written in: `/Users/someone/src/app/.vincent/workflows/x.yaml`
is machine state, not provenance. `Entry.File` stays absolute, and `GET
/v1/workflows` goes on exposing it that way — that one is a live pointer to a
file on this machine, which is a different thing.

**3. The digest is of the source, frozen at creation.** *(2026-08-28)*

It hashes the registry entry's bytes as loaded and is never recomputed, so it
identifies **the file version this task was created from**, not the bytes the
engine executes. Those diverge immediately and by design: include expansion
(§7.9) and fan-out tree resolution (§7.6) both re-marshal the snapshot at
creation, and `edit + retry` rewrites a step inside it afterwards (§5.3, PR C
decision).

This deliberately contradicts the issue's criterion 5 ("snapshot digest is
computed from the exact bytes executed"). A digest that tracks the executed
bytes stops identifying a registry file the moment an operator edits one, which
is the question this column exists to answer; and `edit + retry` is already
independently audited through `step_runs.prompt_override` / `run_override`.
Criterion 6 ("edited workflow files never rewrite existing task provenance")
falls out for free — nothing recomputes the value, and neither `UpdateTask` nor
`TransitionTask` writes the column.

**No normalization before hashing.** Raw bytes as loaded. A canonical form (line
endings, trailing whitespace) exists nowhere else in this codebase, and a CRLF
checkout genuinely *is* different bytes on disk; a normalizer here would make
the digest claim two files agree when the daemon parsed different sources. The
consequence is stated rather than papered over: **the same committed file
digests differently on a Windows checkout with `autocrlf` than on a POSIX one.**
Scope and file still match, and those are what identify the *file*; the digest
identifies the *bytes*.

**4. Legacy rows report missing provenance honestly.** *(2026-08-28)*

The migration adds a nullable column and backfills nothing. NULL means "created
before origin was recorded" and renders as `unknown` with that wording — never a
synthesized scope, and never a re-lookup of today's registry, which would report
the very substitution this feature exists to catch as though it had always been
so. A NULL column scans to a nil pointer rather than a zero-valued origin: "not
recorded" and "recorded as an empty scope" are different claims.

**5. Surfaces: API, `vincent task show`, TUI detail header, `task.created`.**
*(2026-08-28)*

Not transcripts — those are the agent's own JSONL output and the daemon does not
author their content. Not "task plan": no such command or endpoint exists in
this repository (`internal/cli/task.go` has `add`/`ls`/`show`/`follow-up`/
`cancel`, and `internal/api/server.go` has no `/plan` route), so that bullet in
the issue names a surface that is not there.

The TUI header shows scope and file but **not** the digest: that line is a
glance, and half a hash compares against nothing. `vincent task show` and the
API carry the whole digest, which is where an audit is actually done.

The segment goes at the **end** of the header, after the branch. The detail
view is the home shell's bottom-left pane rather than a takeover, so its header
is roughly half the terminal wide and already truncates on a long title — the
`scripts/screenshots.sh` capture of `tui-diff` shows it cut mid-branch-name
before this change. Putting the origin earlier would have bought its visibility
by pushing the project or the branch out, and the branch has no other home in
the TUI. Nothing that was visible stops being visible; a wide terminal shows the
origin; and the audit answer was already `vincent task show` by this same
decision. `docs/assets/tui-diff.png` was re-captured and is unchanged in
content for exactly that reason, so the committed asset was left alone rather
than churned with capture noise.

**6. A fan-out lane child records `scope: derived`, naming its parent.**
*(2026-08-28)*

A lane's steps come from the parent's snapshot, resolved at the *parent's*
creation (§7.6); the child never reads the registry. Inheriting the parent's
file and digest would claim the child's steps came from a file they did not come
from, and leaving it NULL would read as a legacy row (decision 4). `derived`
with the parent id is the true statement. This is a design call made in this
task, not something the issue addresses.

## Tasks

- [x] **043.1** Migration `0017_workflow_origin.sql`, `store.WorkflowOrigin`,
  `Task.WorkflowOrigin`, the column in `insertTaskTx` and every task scan, and
  `workflow_origin` on the `task.created` payload.
- [x] **043.2** `internal/workflow/origin.go`: `Origin`, `Entry.Origin`,
  `SourceDigest`, and `Registry.GlobalDir`.
- [x] **043.3** `POST /v1/tasks` fills the origin from the resolved entry;
  `workflow_origin` on `taskResponse`; the matching wire type and renderers in
  `internal/apiclient`.
- [x] **043.4** `internal/taskrun/fanout.go` sets the derived origin on a lane.
- [x] **043.5** `vincent task show`'s `origin` row and the TUI detail header.
- [x] **043.6** Spec amendments (§5.2, §5.3, §13.2, §13.3, §14), the API/CLI reference
  pages, the workflows and TUI guides, and the changelog entry.

## What the tests prove

- **Store** (`internal/store/workfloworigin_test.go`) — an origin round-trips,
  a derived origin keeps its parent id and claims no file, a row with NULL
  `workflow_origin_json` scans as *unrecorded*, and the value survives a §6
  transition.
- **Workflow** (`internal/workflow/origin_test.go`) — the built-in `adhoc` and a
  project `adhoc.yaml` produce different scopes and different digests; a global
  entry's file reads `workflows/x.yaml`; the digest is stable across a reload of
  unchanged bytes and moves when the bytes do; `SourceDigest` hashes raw bytes,
  LF and CRLF included.
- **API — the issue's actual complaint** (`internal/api/origin_test.go`) — two
  projects, one with `.vincent/workflows/adhoc.yaml` and one without, each
  creating a task with `workflow` omitted. Both run; the first reports
  `scope: project` with that file and its digest, the second `scope: builtin`.
  That is criterion 8 satisfied as visibility.
- **API — provenance is immutable** — rewriting the workflow file after creation
  leaves the task's origin untouched, and so does a rewritten
  `workflow_snapshot`.
- **API — the digest names the file, not the snapshot** — a workflow with an
  `include` produces a task whose snapshot is the expanded form while the digest
  matches the caller's own source bytes.
- **Fan-out** (`internal/taskrun/fanout_test.go`) — a lane child's origin is
  `derived` naming the parent, not a copy of the parent's file and digest.
- **CLI/TUI** — `apiclient`'s renderers cover all four shapes including
  `unknown`; the TUI detail header carries each of them; and the CLI e2e run
  asserts the `origin` row on a real `vincent task show`.

No gate script changes: this adds no cross-process seam the seven existing gates
do not already cross.
