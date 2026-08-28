# 044 — `vincent workflow render`, a template dry run

**Status:** ✅ done (6/6)
**Issue:** [#93](https://github.com/lezli01/vincent/issues/93)
**Spec:** amends §8.4, §12.1, §13.2, §18

## Problem

A workflow cannot be dry-run. `vincent workflow validate` parses each template
— `template.New(field).Parse(text)` in `checkStep` — and never executes one.
Execution is where the bugs are: `workflow.Render` applies `missingkey=error`
(§8.4), and it runs only from `internal/taskrun`. So

- `{{.Task.Titel}}` — a typo'd struct field,
- `{{.Task.Fields.ticket}}` on a task that sets no `ticket`,
- `{{.Steps.plan.Reslt}}` — a typo'd result field,

all validate cleanly and fail at run time. §18 documents the outcome; the only
way to *reach* it was to create a task and watch a step fail. The authoring
loop was therefore write → validate (passes) → create a task → wait for
admission → read a failed step → fix a typo → repeat, at a worktree, an
admission slot and usually an agent invocation per iteration.

## What shipped

```
vincent workflow render <file> [--task ID] [--project ID]
                               [--title S] [--description S] [--field k=v]...
                               [--agent A] [--model M] [--effort E] [--json]
```

It executes every body the file calls a template — `prompt`, `run`, `check`,
`instructions`, `if`, `for_each` items and lane guards — against a synthetic
§8.4 preview context, and prints what each step would send with the §8.6 triple
each agent step resolves to, each field tagged with the level that supplied it.

Offline with no flags, for the reason `validate` is: a pre-commit hook and a CI
runner with no agent CLI installed must both be able to run it. `--task` and
`--project` reach the daemon for a real task's facts and for registry lookups.
Exit 0 clean · 1 a template that does not execute · 2 no daemon answered a
`--task`/`--project`.

## Decisions

**1. The PR L rule is narrowed, not broken.** *(2026-08-28)*

PR L (`docs/history/v0-tasks.md:372`, T4.7 at :776, cited in
`internal/api/resolve.go`) and its spec sentence — §13.2, "Resolution is
server-side only: clients report it, never re-derive it" — are amended in place.
The rule becomes *no client re-implements the precedence*: `render` calls the
same `agent.ResolveWithSources` the handler calls, so §8.6 keeps exactly one
implementation, which is the property the decision was protecting.

Two facts make this safe rather than a relitigation. `validate` already
resolves locally — `validateCatalogs` calls `agent.Resolve` over levels 1 and 3
with the curated catalogs — so client-side resolution from a file is
established precedent. And level 4 read from the curated catalog agrees with
the probed one today: claude and codex both report no default of their own, and
cursor reports `auto` even on a failed probe. Where they could ever disagree
the offline answer is still honest, because the source label says which level
won and the value it names is the curated one.

`POST /v1/resolve` is deliberately not called, even with `--project`. It takes
a workflow **name** and resolves it through the registry; the file being
authored is frequently not in the registry yet, which is the same reason the
issue rejected extending that endpoint.

**Beat:** a daemon-side `POST /v1/workflows/render`. Rendering is pure, so
requiring a daemon for the offline case buys nothing and costs the pre-commit
use case the command exists for.

**2. `.Steps` is populated from the file's own step ids.** *(2026-08-28)*

One entry per declared step — walking nested bodies, so `parallel` sub-steps,
loop bodies and inline fan-out lanes are all present — each carrying a sentinel
`Status` and `Result`. This catches strictly more than a blanket sentinel
would: `{{.Steps.plan.Reslt}}` fails on the struct field and
`{{.Steps.pln.Result}}` fails on the unknown step id, which is a real authoring
bug `validate` cannot see.

**Beat:** binding `.Steps` empty, which under `missingkey=error` would report
every legitimate `{{.Steps.plan.Result}}` as a failure a real run would not hit.

**3. Forward references render clean.** *(2026-08-28)*

Restricting the map to the steps that would have completed at that point was
considered and rejected: §8.4's `(step_index, iteration, body position)` rule
interacts with `parallel` blindness, loop iterations and `allow_failure` in
ways that produce false positives, and a false positive exits 1 inside a
pre-commit hook. A preview that cries wolf is a preview nobody runs.

**4. Declared fields bind when they are **required**.** *(2026-08-28)*

A `fields:` entry (§8.1.2) with `required: true` binds to `<field.NAME>`,
because `POST /v1/tasks` guarantees a real task carries it. Optional declared
fields and undeclared names stay absent, so reading one without
`{{ with index .Task.Fields "x" }}` is an error — which is precisely the §8.4
defensive-read rule the command exists to enforce. `--field` overrides any of
them.

This narrows the issue's acceptance sentence: a workflow that *declares*
`ticket` as required and reads `{{.Task.Fields.ticket}}` renders clean with no
flags, and is correct to.

**5. An unresolvable `include` or named fan-out lane is reported, not
fatal.** *(2026-08-28)*

Offline there is no registry, so `Expand` and `ResolveTree` have no lookup, and
PR U put "which scope wins" in the daemon. Such a step prints as unresolved
with a pointer to `--project`, does not affect the exit code, and every other
step renders. With `--project` the callee is fetched through
`GET /v1/workflows/definition` — the daemon's own shadowing walk — and the
spliced steps render like any other.

**Beat:** resolving from sibling files on disk, which would answer differently
from a real run whenever shadowing applies.

**6. A guard is shown, never judged.** *(2026-08-28)*

`if:` and a lane's `if:` are rendered and printed, but a non-boolean result is
a **warning**, not an error: a sentinel can legitimately make a guard
non-boolean, and §7.7's `condition_error` is a run-time verdict on a real
value. Leaving guards out of the coverage set entirely was rejected — they go
through `workflow.Render` at run time and fail identically, so half the failure
surface would stay unreachable.

**7. `--agent/--model/--effort` are added to the issue's flag list.**
*(2026-08-28)*

§8.6 level 2 is otherwise unpreviewable offline; the resolver already takes an
override `agent.Level`, so this is a flag and a struct field. `--task` fills the
same level from the task's own overrides.

**8. `.Host` is the CLI host's real GOOS/GOARCH.** *(2026-08-28)*

The only honest offline answer: there is no daemon to ask. It is the one place
a preview and a remote daemon can differ, and the CLI reference says so.

**9. No new line on `update-workflows`' feature checklist.** *(2026-08-28)*

CLAUDE.md's standing rule says a landing workflow feature gains a checklist
line, because that checklist propagates **YAML features** into a project's
workflows. `render` has no schema surface: it is an authoring-loop tool. What it
gains instead is a step — the built-in's per-file validation loop now runs
`validate` **and** `render` over every file the pass touched, so a built-in
cannot ship a workflow whose own templates do not execute. Stated here because
the standing rule points the other way and this is the reasoned exception, not
an omission.

## Tasks

- [x] **044.1** `internal/workflow/preview.go` — the preview render context,
  the sentinel vocabulary, and the `PreviewSteps` walk over nested bodies.
- [x] **044.2** `internal/cli/workflow_render.go` — the subcommand, its
  `renderResult` JSON shape, and the human table.
- [x] **044.3** `--task`/`--project`: the four `apiclient.TaskDetail` fields the
  server DTO already served, the project binding, and registry-backed `Expand`
  and `ResolveTree`.
- [x] **044.4** The built-ins run `render` beside `validate`
  (`internal/workflow/builtin.go`), and `skills/vincent-workflows` teaches the
  two-step loop.
- [x] **044.5** Tests: the acceptance cases, `.Steps` binding, declared fields,
  guards, unresolved composition, exit codes, a `--task` live test against the
  real handlers, and a render pass over every shipped example and built-in.
- [x] **044.6** Spec amendments (§8.4, §12.1, §13.2, §18), the CLI, API and
  workflow-schema references, the workflows, scripting and troubleshooting
  guides, the features page, the README's daemon-free paragraph, and the
  changelog entry.

## What the tests prove

- **The acceptance case, with no daemon and no task**
  (`internal/cli/workflow_render_test.go`) — `{{.Task.Titel}}`,
  `{{.Task.Fields.ticket}}`, `{{.Steps.plan.Reslt}}` and `{{.Steps.pln.Result}}`
  each pass `workflow validate` and fail `workflow render`, naming the step and
  the reference. `--field ticket=ABC-1` renders the same file clean.
- **Declared fields** — a `required: true` field renders clean unflagged; an
  optional declared field read non-defensively fails.
- **Guards** — a guard reading a preview sentinel is rendered, shown, and
  warned about, exit 0.
- **Unresolved composition** — a file with an `include` renders its other
  steps, reports the include with a pointer to `--project`, and exits 0.
- **Exit codes** — 0 clean, 1 a render error and an invalid file, 2 `--task`
  with no daemon.
- **`.Steps` binding** (`internal/workflow/preview_test.go`) — ids inside
  `parallel`, `loop` and an inline fan-out lane are all present; a named lane
  contributes none and is reported instead; a loop body renders as the first
  iteration and a merge resolver sees one conflict.
- **`--task`, against the real handlers**
  (`internal/cli/workflowrenderlive_test.go`) — a task created in a real store
  and served by the real API binds its title, description, fields, branch, base
  branch and override triple, which is what keeps the four `TaskDetail` fields
  from drifting from the server DTO.
- **Every shipped example renders** (`internal/workflow/examples_test.go`) — the
  corpus that already parsed now also executes under the preview context, so a
  shipped example whose templates do not run is a test failure rather than a
  user's first task.
- **The built-ins render** (`internal/workflow/preview_test.go`) — all three go
  through the same pass, holding them to the bar CLAUDE.md sets.

No new gate script: the acceptance is a CLI verdict on a file, fully decidable
in Go, and `m2` already covers the daemon-backed paths this command does not
add to.
