# 019 — Including one workflow in another (`type: include`)

**Status:** [~] in progress (9/10) · **Opened:** 2026-08-19

Make a workflow reusable inside another workflow: a step that runs another
registry workflow's steps in the caller's own task and worktree.

## Why this is not already `fan_out`

A `fan_out` lane may already name a registry workflow (§7.6), so "run workflow
B from workflow A" exists. It runs B as a **child task**: its own worktree, its
own `vincent/{id}-{slug}` branch, a merge back into the parent, a slot against
`fan_out.max_tasks`, and a row on the board. Three consequences make it the
wrong tool for a shared three-step fragment:

- B's step results are invisible to A. `.Steps.<id>` reads the *task's* rows,
  and B's rows belong to another task.
- B cannot appear inside a `loop` body or a `parallel` group: §8.2 rejects
  `fan_out` in both.
- The ceremony — a branch, a merge that can block on a conflict, a second row
  on the board — is out of proportion to "run the lint-and-test steps here".

What is missing is a **sequential, same-worktree include** whose steps are
indistinguishable from steps the caller typed inline.

## The mechanism: splice at creation

An `include` step is resolved **when the task is created**: the callee's steps
replace it in the caller's `workflow_snapshot`, each becoming an ordinary
top-level step with its own `step_index`. There is no include at run time — no
`step_runs` row, no cursor, no boundary.

That is the whole design, and everything below follows from it. The engine,
`internal/scheduler`, `internal/taskrun`, recovery (§12.4), `current_step`,
`.Steps` rendering and the guard-blindness rules are **unchanged**: they see a
flat step list, which is what they already saw.

```yaml
# .vincent/workflows/go-checks.yaml
name: go-checks
defaults: { max_retries: 0 }
steps:
  - { id: lint, type: command, run: go run mage.go lint }
  - { id: test, type: command, run: go run mage.go test }

# .vincent/workflows/feature.yaml
name: feature
steps:
  - { id: implement, type: agent, prompt: "{{ .Task.Description }}" }
  - { id: checks, type: include, workflow: go-checks }
  - { id: review, type: agent, prompt: "lint said: {{ .Steps.lint.Result }}" }
```

The created task's snapshot has four steps — `implement`, `lint`, `test`,
`review` — where `lint` and `test` carry `resolved_from: [go-checks]` and a
materialised `max_retries: 0`. `review` reads `.Steps.lint` because there is no
boundary at run time to hide it.

## Decisions

Recorded 2026-08-19, each with the alternative it beat.

### 1. Splice at creation, not a structure step at run time

The alternative was a `loop`-twin: `type: include` as a structure step owning
one `step_index`, its body's rows sharing that index, exactly as §7.5's group
and §7.8's loop do.

**Splice wins on the thing that matters most for reuse: what a callee may
contain.** A structure step forces a choice between forbidding a callee that
itself contains `loop`, `parallel` or `fan_out` — which excludes precisely the
fragments worth factoring out — and opening structure-inside-structure, which
§7.8 (task 016 decision 10) and §20 have both refused, because a loop's and a
group's position are derived from rows keyed by one `step_index` and two
derivations cannot share one.

Under splice the question does not arise: a callee's `loop` lands at top level
and is an ordinary top-level loop.

The cost is real and accepted: the include has no run-time identity, so it can
carry no `timeout`, no `max_retries` and no `if` (decision 11), and its
provenance has to be recorded per step (decision 6) rather than held by a
frame.

### 2. Resolution at creation only

The callee is read from the registry once, in the insert path, and written
into the snapshot. Never read again.

This is §5.3 restated, and the same reasoning `internal/workflow/fanout.go`
already carries for lanes: execution uses the snapshot precisely so that later
edits to workflow files cannot mutate an in-flight task, and a callee read from
the registry six hours into a run would be exactly that mutation, in the one
place nobody would look for it.

It is also what makes decisions 4, 5 and 7 possible: the expanded shape is
computable in the insert path, so every failure is a 400 in front of the person
typing.

### 3. No parameters

The callee reads `.Task.Fields` and `.Steps` exactly as if its steps had been
typed inline. There is no `with:` map and no declared `inputs:` block.

Beaten: a `with:` map merged over task fields (the sequential analogue of a
lane's `fields:`), and full declared inputs with names, defaults and
required-ness.

Nothing yet needs it: with no parameters, two calls to the same callee are the
same call, which is also why decision 5 can reject duplicates outright. The
trigger for revisiting is the first workflow that must include one callee twice
with different values — and it is the same trigger, so `with:` and repeat
inclusion should land together or not at all.

### 4. A step type, spelled `include`

`- { id: checks, type: include, workflow: go-checks }`.

Beaten: a bare `uses:` field with no `type` (every validation path branches on
`type` first, and a typeless step is a new shape in `Step`), and a file-level
`include:` list (which cannot say *where* in the sequence the fragment goes).

The word is `include` rather than `workflow` because `type: workflow, workflow:
go-checks` stutters, and because the config knob, the error strings, the docs
page and the provenance field all need one verb — and `workflow` is the
domain's most overloaded noun. Hence `include.max_depth`, "included workflow",
`docs/guides/workflows.md`.

### 5. A duplicate step id after splice is a 400

Step ids are unique across the whole workflow (§8.2) and name the transcript
file. A callee whose step id collides with one already in the expansion is a
creation-time error naming both paths.

Beaten: prefixing spliced ids with the call step's id (`checks.lint`). It would
allow repeat inclusion, but every `.Steps.<id>` reference **inside the callee's
own templates** would then have to be rewritten — mechanically editing Go
template text, which is fragile and, under `missingkey=error`, fails at run
time rather than at creation. Also beaten: prefixing only on collision, which
makes a callee's step ids depend on what its caller happens to contain.

The accepted consequence is that a given callee may appear at most once in one
expansion. See decision 3 for the trigger.

### 6. Provenance is a chain of workflow names

Each spliced step carries `resolved_from: [outer, …, inner]`, outermost first,
written by the resolver and never by hand — the rule `Lane.ResolvedFrom`
already states.

Beaten: a single innermost name (mirroring `Lane.ResolvedFrom` exactly), which
cannot say how a step from a nested include got where it is; and a chain of
call-site step ids, which is unambiguous but shows the caller's local labels
rather than the names a human recognises.

Grouping in any view is "contiguous steps sharing a prefix". The chain is
unique because decision 5 means a given callee appears at most once.

### 7. The callee's `defaults:` are materialised at creation

A fragment written with `defaults: {agent: codex, max_retries: 0}` keeps that
behaviour after splice. At creation the splice runs a **four-level** resolve —
step field, then task override, then *callee* defaults, then caller defaults —
and writes the winners onto each spliced step's own fields.

Beaten: dropping the callee's defaults and letting the caller's govern, which
silently changes what the fragment does — the failure mode this codebase
consistently refuses; and rejecting any callee that declares `defaults:`, which
makes reusability a property you must have designed for in advance.

Two facts make materialising exact rather than approximate:

- Task-level overrides are **immutable** (`priority` is the only mutable task
  field in v1, `internal/api/actions.go`), so the full §8.6 chain is knowable
  at creation.
- `agent.ResolveWithSources` is *agent-scoped* — a level whose agent differs
  from the resolved one contributes nothing but its agent field — so the
  (agent, model, effort) triple cannot be merged field by field and is resolved
  as a unit.

The four-level logic lives in the splice path alone. `agent.Resolve` keeps its
three levels and §8.6 keeps its four, so nothing that resolves a step at run
time learns a new rule. The cost is provenance: `GET /v1/tasks/{id}/resolution`
reports `SourceStep` for a value the callee's defaults supplied, and `edit +
retry` shows fields the author did not type. `resolved_from` is the
explanation.

### 8. The creation-time gate: cycles, depth, platforms

Three checks that cannot be decided at load — shadowing decides which files
even participate — and are exact at creation. The same split
`LaneCycleWarnings` already makes: a warning at registry load, a 400 at
creation.

- **Cycles** (A includes B includes A): 400 naming the path,
  `workflow cycle: a → b → a`, plus a load-time warning.
- **Depth**: `include.max_depth`, default 5, beside `fan_out.max_depth: 3` and
  `loop.max_iterations: 10`. No expanded-step bound: decision 5 already makes a
  diamond a 400, so an expansion cannot silently multiply.
- **Platforms**: if any transitively included callee's `platforms:` (§8.1.1)
  excludes this host, 400 at creation plus a load-time warning.

Beaten for platforms: recomputing the caller's declared `platforms:` from its
callees'. The caller's `platforms:` stays a property of the file as written, so
`Entry.RunsHere()` keeps one meaning. The consequence — `vincent workflow
validate` on a Windows box passes a caller that includes a POSIX-only fragment
— is the trade §8.1.1 already made ("checked for *shape*, never against the
validating host").

### 9. An include may appear anywhere a step may

Top level, inside a `parallel` group, inside a `loop` body, and inside a
`fan_out` lane's inline `steps:`.

This is the payoff of decision 1, and it means the existing nesting bans can
only be enforced **after** expansion. So the full §8.2 validation re-runs on
the expanded workflow at creation, with error paths naming both the call site
and the offending callee step. That re-validation is required anyway — id
uniqueness (decision 5), the cross-catalog check, the `on_input: require`
gate — so allowing includes inside bodies costs only the error-path plumbing.

Beaten: top-level-only, which would forfeit the main advantage of splice.

Note the resulting overlap: a `fan_out` lane can name a workflow two ways — the
lane's own `workflow:` and an `include` in its inline `steps:`. They mean
different things (a child task vs. steps in that child's own sequence), and
that is a sentence in §8.2, not a restriction.

### 10. A spliced `condition` ends the whole task's sequence

There is no include boundary at run time, so a `condition` inside a callee
whose `if:` renders false ends the caller's sequence too: the task is `done`
having skipped the caller's remaining steps.

Beaten: rejecting `condition` inside a callee. That would make some workflows
un-reusable for a reason invisible from the file being written, and it would
give `condition` a second meaning ("ends the include") that §7.7 does not have.

The related limitation is accepted rather than solved: `break` is valid only
inside a loop body, so a workflow whose top-level steps contain one fails to
load and can never be a callee — **loop-body fragments needing `break` cannot
be factored out**. The alternative was demoting that to a load-time warning
promoted to an error at creation unless the call site is inside a loop, which
weakens a clean load-time error for a case nobody has yet. The trigger is the
first fragment worth factoring out of a loop body.

### 11. An include carries `id`, `name`, `type` and `workflow` only

`timeout`, `max_retries`, `allow_failure` and `check` are rejected because
there is no run-time step for them to bind to — the include owns no
`step_runs` row, no attempt and no process.

`if:` is rejected too, which is the one that had a real alternative. Honouring
a guard on an include would mean distributing it onto every spliced step at
creation. That reads correctly for ordinary steps, but a spliced `condition` or
`break` already carries its own `if:` and rejects every other field, so
combining the two would mean rewriting Go template text — the string surgery
decision 5 refused — or a creation-time error about an interaction two files
apart.

So `include` joins `condition` and `break` as an exception to the §8.2 common
field table. A conditional include is spelled by guarding the callee's own
steps, or by a preceding `condition` step. The trigger for revisiting is the
first workflow that needs a whole fragment skipped on one condition and cannot
express it either way.

The include's `id` is still required and still joins the workflow-wide id
namespace at authoring time, even though it vanishes from the snapshot: it is
what the creation-time errors name.

### 12. The graph keeps includes collapsed

In the workflows view the graph renders the **authored** definition, so an
include is a node. It reuses `KindWorkflowRef` — the kind that already exists
for a `fan_out` lane naming a registry workflow, and which task 017 documented
as staying collapsed because "expanding it is navigation, not rendering".

In the task detail the snapshot is already expanded, so spliced steps are
ordinary steps and carry a `from <name>` badge, with the full chain in the step
detail. No grouping frame.

Beaten: expanding includes inline in the graph. Making includes the exception
would give one question two answers. The trigger is the first time someone
cannot tell what a workflow does without opening three files.

## Tasks

- [x] **019.1** `include.max_depth` in `internal/config` (default 5, validated
      ≥ 1), documented in `docs/reference/configuration.md`. ✓ 2026-08-19

- [x] **019.2** `StepInclude`, the `workflow:` and `resolved_from:` fields on
      `Step`, and §8.2 validation: `include` requires `workflow` and rejects
      every other field (decision 11); `workflow`/`resolved_from` are rejected
      on every other step type; `resolved_from` is machine-written. ✓ 2026-08-19

- [x] **019.3** `internal/workflow/include.go`: `Expand` — splice, cycle
      detection, depth bound, platform check, defaults materialisation
      (decision 7), duplicate-id detection (decision 5), provenance chains
      (decision 6). Plus `HasInclude` and load-time cycle warnings.
      *Depends: 019.2* ✓ 2026-08-19

- [x] **019.4** Wire expansion into task creation in `internal/api/tasks.go`,
      before fan-out tree resolution, with post-splice re-validation
      (decision 9). *Depends: 019.3* ✓ 2026-08-19

- [x] **019.5** `Entry.Includes` on the registry and `GET /v1/workflows`;
      `workflow`/`resolved_from` on the workflow-definition DTOs. ✓ 2026-08-19

- [x] **019.6** TUI: `KindWorkflowRef` for an include in the graph; the
      `from <name>` badge in the task detail. *Depends: 019.5* ✓ 2026-08-19

- [x] **019.7** Spec: new §7.9, the §8.2 table row and constraint bullets, and
      the §20 promotion note. ✓ 2026-08-19

- [x] **019.8** User docs: `reference/workflow-schema.md`,
      `reference/configuration.md`, `reference/api.md`, `guides/workflows.md`. ✓ 2026-08-19

- [x] **019.9** `scripts/m9-gate.sh`, committed executable, written to the
      POSIX ∩ pwsh intersection the CLAUDE.md gate notes describe. ✓ 2026-08-19

- [!] **019.10** Wire `m9` into `ci.yml`'s `gates` job on all three platforms.
      **Not doable from a cloud session:** its token has no `workflow` scope and
      so cannot write `.github/workflows/` by any route (#120, #122, #125).
      Until this lands, `m9` is not known to pass on any platform CI has not run
      it on. Walked green on Linux at 019.9, together with m1, m2, m5, m6, m7
      and m8 as a regression check.

      The edit is one step appended to the `gates` job, beside M8's:

      ```yaml
            # M9: includes (task 019). Command steps only, with `run:` bodies
            # in the same `exit N` / `git …` vocabulary m7 and m8 use, so the
            # matrix proves the shell as much as the feature.
            - name: M9 gate (includes)
              shell: bash
              run: ./scripts/m9-gate.sh
      ```
