{% raw %}

# Writing workflows

A **workflow** is a YAML file describing an ordered list of steps. Every task
runs exactly one, in its own git worktree on its own branch, and the daemon is
what runs it.

This page is the practical guide: what to write, in what order, and why the
schema is shaped the way it is. Keep the
[Workflow schema](../reference/workflow-schema.md) beside it when you need every
field and default in table form.

Five ready-to-copy files live in [`examples/`](../../examples), and the binary
will install one for you:

```sh
vincent workflow init my-flow --from feature-pr   # or drop --from for a skeleton
```

If you would rather read one working workflow than a guide, start with
[`feature-pr.yaml`](../../examples/feature-pr.yaml) and come back here for the
parts you want to change.

There is a third way in, if you would rather not read YAML at all: the
[workflows screen](tui.md#authoring--i-a-f) creates, edits and forks these
files through structured forms. It knows the schema below, so it will not offer
you a field the step you are editing cannot carry. Everything on this page is
still what the file says — the forms write the same YAML, and keep the comments
you put in it.

## Agent-assisted authoring

The portable [vincent Workflows skill](../../skills/vincent-workflows/SKILL.md)
helps supporting coding agents design and validate these files:

```sh
npx skills add lezli01/vincent --skill vincent-workflows -g
```

It asks only for decisions that change the workflow, including possible human
gates and interaction needs. It prefers deterministic command steps and native
control flow, and reserves prompt-based agent steps for work that actually
requires reasoning.

---

## Contents

**Getting started**

- [1. Before you start](#1-before-you-start) — [1.1 What a workflow is](#11-what-a-workflow-is) · [1.2 Where files live](#12-where-workflow-files-live) · [1.3 Validating](#13-validating-a-file) · [1.4 Live reload](#14-live-reload)
- [2. Your first workflow](#2-your-first-workflow) — [2.1 Minimum file](#21-the-minimum-viable-file) · [2.2 Add a check](#22-add-a-check) · [2.3 Add a gate](#23-add-a-gate) · [2.4 Run it](#24-run-it)

**The file**

- [3. File anatomy](#3-file-anatomy) — [3.1 Top-level keys](#31-top-level-keys) · [3.2 `defaults`](#32-defaults) · [3.3 Steps and ids](#33-steps-and-ids) · [3.4 What a step inherits](#34-what-a-step-inherits)
- [4. The nine step types](#4-the-nine-step-types) — [4.1 Choosing](#41-choosing-a-step-type) · [4.2 `agent`](#42-agent) · [4.3 `command`](#43-command) · [4.4 `manual`](#44-manual) · [4.5 `parallel`](#45-parallel) · [4.6 `fan_out`](#46-fan_out) · [4.7 `condition`](#47-condition) · [4.8 `loop`](#48-loop) · [4.9 `break`](#49-break) · [4.10 `include`](#410-include) · [4.11 Nesting rules](#411-where-each-type-may-appear)

**Making steps talk to each other**

- [5. Templates and data flow](#5-templates-and-data-flow) — [5.1 Template fields](#51-which-fields-are-templates) · [5.2 The context](#52-the-template-context) · [5.3 Step to step](#53-passing-one-steps-output-to-the-next) · [5.4 Task fields](#54-task-fields) · [5.5 Environment](#55-environment-variables) · [5.6 Reporting status](#56-reporting-status-from-a-step)
- [6. Control flow](#6-control-flow) — [6.1 `if:`](#61-if--skip-a-step-and-carry-on) · [6.2 `allow_failure:`](#62-allow_failure--a-failure-that-is-data) · [6.3 `condition`](#63-condition--finish-early-and-succeed) · [6.4 Loops in depth](#64-loops-in-depth) · [6.5 Guard recipes](#65-guard-recipes)

**Making it reliable**

- [7. Verification with `check`](#7-verification-with-check) — [7.1 Why](#71-why-every-writing-step-wants-one) · [7.2 How it runs](#72-how-a-check-runs) · [7.3 Inverted](#73-inverting-a-check) · [7.4 Limits](#74-what-a-check-is-not)
- [8. Failure, retries and timeouts](#8-failure-retries-and-timeouts) — [8.1 Success](#81-what-counts-as-success) · [8.2 Retries](#82-retries) · [8.3 Timeouts](#83-timeouts) · [8.4 Running out](#84-when-a-step-runs-out-of-attempts) · [8.5 Interruption](#85-interruption-is-not-failure)
- [9. Agents, models and permissions](#9-agents-models-and-permissions) — [9.1 Resolution](#91-resolution-order) · [9.2 Adapters](#92-what-each-adapter-can-do) · [9.3 Permission modes](#93-permission-modes) · [9.4 Mid-run questions](#94-mid-run-questions-on_input)
- [10. Portability](#10-portability) — [10.1 Shells](#101-which-shell-runs-a-command-step) · [10.2 Portable commands](#102-writing-portable-command-steps) · [10.3 `platforms:`](#103-platforms--declare-what-you-did-not-port) · [10.4 Per-step gates](#104-gating-one-step-by-platform)
- [11. Concurrency, cost and limits](#11-concurrency-cost-and-limits)

**Shipping**

- [12. Patterns that work](#12-patterns-that-work)
- [13. Reviewing a workflow before you commit it](#13-reviewing-a-workflow-before-you-commit-it)
- [14. Troubleshooting](#14-troubleshooting)

---

# Getting started

## 1. Before you start

### 1.1 What a workflow is

A workflow is a **recipe**, not a script. It describes steps; the daemon
decides when they run, retries them, times them out, records every attempt, and
survives its own restart mid-step. You write what should happen; vincent owns
how.

Three properties are worth internalising before you write one, because the
schema follows from them:

1. **One task, one worktree, one branch.** Every step of a run sees the same
   working tree, and what a step leaves on disk is what the next step finds.
   State flows through the filesystem and through `{{.Steps}}`, never through a
   shared agent conversation.
2. **Every agent step is a fresh session.** Nothing is resumed between steps or
   between attempts. If step 3 needs to know what step 1 found, step 1's result
   has to be in the prompt — see [§5.3](#53-passing-one-steps-output-to-the-next).
3. **A step that runs out of attempts blocks the task.** Nothing is silently
   abandoned; a human is asked. That is why `check:`
   ([§7](#7-verification-with-check)) is worth writing: it decides what "the
   step worked" means.

### 1.2 Where workflow files live

| Scope | Location | Precedence |
|---|---|---|
| **Project** | `.vincent/workflows/*.yaml` inside the repo | Highest — shadows global |
| **Global** | `{config_dir}/workflows/*.yaml` | Shadows built-in |
| **Built-in** | `adhoc`, `create-workflow` and `update-workflows` — always present | Lowest |

`{config_dir}` is `%APPDATA%\vincent` on Windows, `~/Library/Application
Support/vincent` on macOS and `~/.config/vincent` on Linux; the full table is in
[Files and directories](../reference/files.md).

Details that matter in practice:

- Both `.yaml` and `.yml` are read, from the directory itself — **not**
  recursively, so a `subdir/` inside `workflows/` is ignored.
- **Only regular files, up to 1 MiB.** A symlink, named pipe, socket or device
  named `*.yaml` is not a workflow source and is never opened or followed, and a
  file over 1 MiB is not read whole. Either one is listed as an invalid entry
  saying why, exactly like a file that fails to parse — the rest of the scope
  keeps working. Both bounds are fixed; there is no setting for them.
- Workflows are addressed by their `name:` field, not by filename. A file named
  `ci.yaml` declaring `name: feature-pr` is the workflow `feature-pr`.
  Keeping the two the same is a convention worth following, not a rule.
- **Shadowing is per name.** A project file named `adhoc` replaces the built-in
  for that project only.
- **`create-workflow` writes the other kind of file.** It is the second built-in:
  one agent step that designs a workflow from the task's description and installs
  it under the name its required `workflow_name` field gives, guided by the
  [`vincent-workflows`](https://github.com/lezli01/vincent/tree/master/skills/vincent-workflows)
  skill, which is built into its prompt. It may **stop and ask** a design
  question it cannot settle from the repository — the task parks in
  `awaiting_input` until you answer, so check on it rather than queueing it and
  walking away. Its boolean task field `global` picks the registry — unset or `false` writes
  `{repo}/.vincent/workflows` for the task's own project, `true` writes
  `{config_dir}/workflows`. Either way the file lands in the live registry rather
  than in the task's worktree, so the daemon reloads it immediately and it is
  **not** part of the task's diff.
- **`update-workflows` maintains the ones you already have.** It is the third
  built-in, and the one to run when vincent has gained features since your
  workflows were written. It reads every workflow the task's project versions
  under `.vincent/workflows`, brings each up to the current schema and the same
  authoring skill — a command step where an agent step was running a build, a
  guard where a prompt said "if X then Y", a `check:` where a prompt only
  claimed success, a declared field where a value was buried in the task
  description, a [`vincent status`](#56-reporting-status-from-a-step) line in
  the steps you sit and wait on — and then validates every file. It changes
  *how* a workflow is expressed, never what it does: names, fields, manual
  gates and external effects are off limits, and a workflow it cannot improve
  conservatively is left alone and reported. Unlike `create-workflow`, its
  deliverable **is** the task's diff — these files are versioned by the
  repository, so you review the rewrite on the task's branch and merging it is
  what makes the new versions live. A workflow file you have never committed is
  not in the worktree and is not touched; the global registry is out of scope.
  It takes no task fields, never asks you anything (`on_input: deny`), and
  finishes with nothing to do on a project that has no workflows of its own.
- Two files in **one scope** declaring the same `name:` is an error, resolved
  deterministically: the first in filename order keeps the name, and the loser
  is listed as invalid rather than silently dropped.

`vincent workflow ls` prints the merged registry with a scope badge per entry;
`--project <id>` includes that repository's own files.

**Which definition actually ran.** Shadowing means a name is not, on its own,
an answer — a repository's `.vincent/workflows/adhoc.yaml` wins over the
built-in `adhoc`, including for tasks created without naming a workflow at all.
Every task therefore records where its definition came from, and
[`vincent task show`](../reference/cli.md#vincent-task-show) prints it as
`origin`:

```
workflow  adhoc
origin    project .vincent/workflows/adhoc.yaml sha256:0f4a1c1e…
```

The scope, the file relative to that scope's root, and a digest of the bytes the
registry loaded. It is captured at creation and never updated, so editing the
file afterwards does not rewrite the history of tasks that already ran it. The
TUI's task-detail header carries the same thing without the digest, and the API
serves it as `workflow_origin`.

**Getting a file into one of those directories.**
[`vincent workflow init <name>`](../reference/cli.md#vincent-workflow-init)
writes one and prints the path — the skeleton by default, or a shipped example
with `--from`, embedded in the binary so it works from any directory. The
default (global) scope needs no daemon; `--project <id>` does, because only the
daemon knows where that repository is. It never overwrites: an existing path, or
a sibling in the same scope already declaring that `name:`, is refused. Taking a
name from a *lower* scope is the shadowing above working as intended, so it
warns and writes.

That is the offline route: instant, free, and always the same file.
`create-workflow` above is the other one — it *designs* a workflow from a
description, at the cost of a daemon, an agent CLI, tokens, wall-clock time and
a run that may stop to ask you a design question. Reach for `init` when you know
roughly what you want to write.

### 1.3 Validating a file

```sh
vincent workflow validate .vincent/workflows/feature-pr.yaml
```

This runs **entirely locally**: no daemon, no network, no agent CLI installed.
That is deliberate, so it works in a pre-commit hook and in CI. It exits 1 on an
invalid file and prints `line N: path: message` for each finding.

Two things it uses local stand-ins for, because it cannot ask a daemon:

- **Agent catalogs** are the curated, compiled-in ones rather than whatever a
  live `claude --help` reports. Probing only ever *adds* values, so a verdict
  can soften on a real daemon but never harden.
- **The loop ceiling** is the built-in default (10), not your `config.yaml`. A
  workflow that validates on your laptop has to validate on a CI runner with no
  config file at all.

Add `--json` for machine-readable findings.

### 1.4 Live reload

The daemon watches both workflow directories and reloads on save. There is no
restart and no "apply" step.

- A file that fails to parse is **reported as invalid and kept visible**; the
  previously loaded version keeps running.
- Editing a workflow never affects a task already created from it. A task
  captures the file's source as its **snapshot** at creation, so an in-flight
  or historical run is immutable. Fixing a file fixes the *next* task — to fix
  a blocked one, use `edit + retry`, which rewrites the step inside that task's
  own snapshot.

---

## 2. Your first workflow

### 2.1 The minimum viable file

`vincent workflow init tidy` writes a commented one-agent-step starting point
into `{config_dir}/workflows/tidy.yaml` and prints the path. What follows is
smaller still, to show what is actually *required*:

```yaml
name: tidy
description: Run the formatter and commit the result.

steps:
  - id: format
    type: command
    run: gofmt -w .

  - id: commit
    type: command
    run: 'git add -A && git commit -m "chore: gofmt" --allow-empty'
```

That is a valid workflow: a `name`, and a non-empty `steps` list where every
step has a unique `id` and a `type`. Save it as
`.vincent/workflows/tidy.yaml` and it is available to that repo's tasks within
a second.

### 2.2 Add a check

An agent step succeeds when its process exits 0 and its event stream reports no
error. That is the *agent's* claim about its own work. A `check:` turns it into
something a machine verifies:

```yaml
name: tidy
description: Fix the lint findings, and prove it.

steps:
  - id: fix
    type: agent
    prompt: |
      Fix every finding reported by `golangci-lint run` in this repository.
      Do not disable a linter or add a nolint directive to silence one.
    check: go build ./... && golangci-lint run

  - id: commit
    type: command
    run: 'git add -A && git commit -m "fix: lint findings" --allow-empty'
```

If the check fails, the attempt fails, and the retry gets the failure appended
to its prompt automatically. That loop — write, verify, be told exactly what
broke, try again — is most of the value of running an agent under a workflow
rather than by hand.

### 2.3 Add a gate

Anything that leaves the machine belongs behind a human:

```yaml
  - id: review
    type: manual
    instructions: |
      Read the diff for task #{{.Task.ID}} on branch {{.Task.BranchName}}.
      Approve to push.

  - id: publish
    type: command
    run: git push -u origin {{.Task.BranchName}}
    max_retries: 0
```

The task parks in `awaiting_gate` and **releases its concurrency slot** while it
waits, so a workflow waiting on you is not occupying a runner.

### 2.4 Run it

```sh
vincent workflow validate .vincent/workflows/tidy.yaml
vincent task add --project 1 --workflow tidy --title "Clear the lint backlog"
```

or press `n` in the TUI and pick it from the workflow list. Follow it with
`vincent task show <id>`, or open the TUI's detail view for live output.

---

# The file

## 3. File anatomy

### 3.1 Top-level keys

```yaml
name: feature-pr                 # required — how tasks refer to it
description: One line, shown in the picker.
platforms: [posix]               # optional — where this file may run (§10.3)

fields:                          # optional — ordered task inputs (§5.4)
  - name: ticket
    label: Ticket
    required: true
    pattern: '^OPS-[0-9]+$'
  - name: environment
    type: enum
    values: [dev, staging, prod]
    default: staging

defaults:                        # optional — inherited by every step
  agent: claude
  max_retries: 2
  timeout: 45m

steps:                           # required, non-empty — runs top to bottom
  - id: implement
    type: agent
    prompt: |
      Implement {{.Task.Title}}.
```

| Key | Required | Notes |
|---|---|---|
| `name` | ✅ | Unique per scope. No whitespace and no `/` or `\` |
| `description` | | One line; shown in the TUI picker and `vincent workflow ls` |
| `platforms` | | Empty means anywhere — [§10.3](#103-platforms--declare-what-you-did-not-port) |
| `fields` | | Expected task inputs — [§5.4](#54-task-fields) |
| `defaults` | | Workflow-wide fallbacks — [§3.2](#32-defaults) |
| `steps` | ✅ | Runs strictly in order |

**Unknown keys are errors, not ignored.** A misspelled `max_reties:` fails
validation instead of becoming a setting that silently never applied. The same
is true of a field on the wrong step type: `run:` on an agent step is an error,
not a hint.

### 3.2 `defaults`

`defaults:` holds workflow-wide fallbacks. Every key in it is also settable per
step, where the step wins.

| Key | Applies to | Falls back to |
|---|---|---|
| `agent` | agent steps | the daemon's configured default |
| `model` | agent steps | the adapter's own default |
| `effort` | agent steps | the adapter's own default (cursor has none) |
| `permission_mode` | agent steps | `full-auto` |
| `on_input` | agent steps | `wait` |
| `input_timeout` | agent steps | `defaults.input_timeout` in config — 24h |
| `max_retries` | all steps | `1` |
| `retry_backoff` | all steps | `0s` — an immediate retry ([§8.2](#82-retries)) |
| `timeout` | all steps | `defaults.agent_timeout` (60m) or `defaults.command_timeout` (15m) in config |
| `container` | command steps and checks | the daemon's [`container:`](../reference/configuration.md#container) block, merged per field |

Durations are Go duration strings: `90s`, `45m`, `1h30m`. A bare number is a
validation error.

`container` is the one mapping here rather than a scalar, and it merges over the
daemon's block key by key — see
[the schema page](../reference/workflow-schema.md#defaults) for the fields and
[`container`](../reference/configuration.md#container) for what they do.

Three deliberate absences:

- **`check` is not in `defaults`.** A check is specific to what a step produced;
  a workflow-wide one would run after steps it makes no sense for.
- **`shell` and `env` are not in `defaults`.** They belong to command steps, and
  a file-wide shell pin hides which steps actually need it.
- **`allow_failure` is not in `defaults`** ([§6.2](#62-allow_failure--a-failure-that-is-data)).
  A file-wide "failures do not block" is a footgun; it should cost one line per
  step that wants it.

### 3.3 Steps and ids

Every step needs an `id` and a `type`. Ids are slugs: lowercase letters,
digits, `-`, `_` and `.`, starting with a letter or digit.

Ids are the file's public surface — they are how `{{.Steps}}` addresses a
result, how a transcript file is named, and how the TUI labels a row. Two rules
follow:

- **Ids are unique across the whole file**, including sub-steps of a `parallel`
  group and body steps of a `loop`. Those share their structure step's index
  and are told apart by id alone.
- **A lane's inline steps have their own namespace**, because each lane becomes
  a separate task with its own flat snapshot ([§4.6](#46-fan_out)).

`name:` is an optional display name and defaults to the id.

### 3.4 What a step inherits

Reading top to bottom, a value on an agent step resolves like this:

```
step field  →  task-level override (chosen at creation)  →  defaults:  →  adapter default
```

with one refinement: **`model` and `effort` only inherit from a level whose
resolved agent matches the step's.** Switching agent without setting them resets
them to the new adapter's default rather than leaking a claude alias onto a
codex step. The full rules are in [§9.1](#91-resolution-order).

---

## 4. The nine step types

### 4.1 Choosing a step type

| You want to… | Type |
|---|---|
| Have an agent write, edit or review code | [`agent`](#42-agent) |
| Run a build, test, lint or git command | [`command`](#43-command) |
| Stop and wait for a person | [`manual`](#44-manual) |
| Run several independent verifications at once, in one worktree | [`parallel`](#45-parallel) |
| Split one deliverable into disjoint pieces, each on its own branch | [`fan_out`](#46-fan_out) |
| Finish the run early, successfully | [`condition`](#47-condition) |
| Repeat a sequence until it converges, or once per item | [`loop`](#48-loop) |
| End the enclosing loop | [`break`](#49-break) |
| Reuse another workflow's steps here | [`include`](#410-include) |

`check` is a **field** on agent and command steps, not a type of its own
([§7](#7-verification-with-check)). Four types — `parallel`, `fan_out`, `loop`
and `condition`/`break` — are *structure*: they organise other steps rather than
running anything themselves. `include` is a fifth kind of thing again: it is
resolved away before the task runs.

### 4.2 `agent`

Runs an agent CLI headlessly in the worktree. Requires `prompt`.

```yaml
  - id: implement
    type: agent
    agent: claude                # else defaults.agent, else the daemon default
    model: sonnet                # optional; adapter-native id or alias
    effort: high                 # optional; cursor has no effort concept
    permission_mode: full-auto   # full-auto (default) | restricted
    on_input: wait               # wait (default) | deny | require
    prompt: |
      Implement {{.Task.Title}}.

      {{.Task.Description}}
    check: go build ./... && go test ./...
    check_timeout: 15m
```

**Succeeds when** the process exits 0, **and** the event stream produced a
terminal result rather than an error, **and** any declared `check` exits 0.

Agent steps do **not** take `run`, `shell`, `env` or `instructions` — those
belong to other types, and setting one is a validation error rather than a
silently ignored key.

### 4.3 `command`

Runs a shell command in the worktree. Requires `run`.

```yaml
  - id: commit
    type: command
    run: 'git add -A && git commit -m "{{.Task.Title}}"'
    shell: sh                    # optional pin: sh | pwsh | cmd
    env:
      CI: "true"
    check: git log -1 --format=%s
```

**Succeeds when** the command exits 0 **and** any declared `check` exits 0.

The rendered `run` string is handed to one shell as a single script, so shell
syntax — `&&`, pipes, redirection — works, subject to which shell you get
([§10.1](#101-which-shell-runs-a-command-step)). `shell:` accepts exactly `sh`,
`pwsh` or `cmd`; `bash` is not a value.

Command steps do not take `prompt`, `agent`, `model`, `effort`,
`permission_mode`, `on_input` or `instructions`.

> **Quote a `run:` that contains a colon.** `run: git commit -m "fix: thing"` is
> a YAML parse error, not a command — the parser sees `fix:` as a second mapping
> key. Wrap the whole value in single quotes, or use a block scalar:
>
> ```yaml
>     run: 'git commit -m "fix: thing"'
>     run: >-
>       git commit -m "fix: thing"
> ```

### 4.4 `manual`

Stops and waits for a person. Requires `instructions`.

```yaml
  - id: review
    type: manual
    instructions: |
      Read the diff for #{{.Task.ID}} before it ships.
      Reject if any test was weakened rather than fixed.
```

The task enters `awaiting_gate` and **releases its concurrency slot** — the
actor goroutine ends, and the task is re-admitted when a human acts. Approving
advances to the next step; rejecting blocks the task with reason `rejected`.

`instructions` is a template, so a gate can tell the reviewer what they are
looking at. A manual step takes no `check`, no agent fields, and no
`allow_failure` — there is no failure of its own to allow. It *may* carry an
`if:` guard, which is how a gate becomes conditional.

### 4.5 `parallel`

Runs its sub-steps at the same time, in the task's **one** worktree. Requires
`steps`.

```yaml
  - id: verify
    type: parallel
    max_parallel: 4              # default: parallel.max_parallel (4)
    timeout: 30m                 # bounds the whole group
    steps:
      - { id: test,      type: command, run: go test ./... }
      - { id: lint,      type: command, run: golangci-lint run }
      - { id: typecheck, type: command, run: go vet ./... }
```

Verification is what this is for. Tests, a linter and a type check do not
interact — they read the tree and report an exit code. In sequence they cost the
sum of three waits; in a group they cost the longest.

**The group is one step**: one index, one concurrency slot, one entry in the
timeline, one thing to retry.

- It succeeds when **every** sub-step succeeds.
- A failure does **not** cancel the others. The group waits for everything it
  started, then blocks with the first failure in declaration order — so you get
  all three verdicts, not just the first bad one.
- A retry re-runs **only what failed**, including after a human retry. A passing
  test suite is not re-run because the linter was unhappy.

Sub-steps are ordinary `agent` and `command` steps with their own `check`,
`timeout`, `max_retries`, `retry_backoff`, `if:` and agent selection, resolved
exactly as at the top level. They may not be any other type — see
[§4.11](#411-where-each-type-may-appear).

> **Sub-steps share one working tree.** Two of them writing the same file is a
> bug in your workflow. Vincent isolates worktrees between *tasks*, not
> processes inside one task.

> **`max_parallel` is not covered by your concurrency caps.** Those count tasks
> ([§11](#11-concurrency-cost-and-limits)); the whole group lives inside one.
> A board showing "1 running" can be a machine running four compilers. Size it
> for the hardware.

### 4.6 `fan_out`

Turns each lane into a **real child task** — its own worktree, branch, slot,
gates, retries and transcripts — then merges those branches back into this
task's own. Requires `lanes`.

```yaml
  - id: build
    type: fan_out
    merge:
      on_conflict: block         # block (default) | agent
    lanes:
      - id: api
        workflow: implement-module      # a registry workflow…
        fields: { module: api }
      - id: docs
        steps:                          # …or inline steps
          - { id: write, type: agent, prompt: "Document the API." }
```

Use it when one deliverable has parts with no reason to wait for each other —
two modules that do not touch, or code in one place and docs in another. A
single task cannot do that: it has one worktree, one branch and one cursor.

**A lane** carries `id` plus exactly one of `workflow` or `steps`, and
optionally:

| Key | Meaning |
|---|---|
| `if` | Guard. False means this lane is not spawned; siblings still run and the join still happens |
| `fields` | Merged over the parent task's fields, the lane winning |
| `agent` / `model` / `effort` | Override the inherited selection for this lane's whole subtree |
| `priority` | Same, for scheduler priority |

**What a lane inherits.** Its **base branch is the parent's branch**, which is
how the work lands where it belongs. Priority and the agent overrides propagate
too: a fan-out inside an urgent task would otherwise queue behind unrelated work
and make the urgent task *slower* than not fanning out.

**Nesting.** A lane's workflow may itself contain a `fan_out`, to any depth. The
bounds are `fan_out.max_depth` (3) and `fan_out.max_tasks` (64), both checked
when the task is created — a cycle or an oversized tree is a `400` naming what
is wrong, not something you discover as two hundred worktrees. Guarded lanes
count toward those limits, because a guard is evaluated at run time and the
limits are enforced before that.

**Ordering lanes.** A lane may name the sibling lanes it `needs:`. It spawns
only once every lane it names is done *and merged*, so its worktree is cut from
a branch that already carries their commits — the dependency is delivered as
code, not just as ordering.

```yaml
    lanes:
      - { id: api,  workflow: implement-module }
      - { id: db,   workflow: implement-module }
      - { id: wire, workflow: implement-module, needs: [api, db] }
```

The step works the graph out for itself, in rounds; you never name a wave.
There is one branch, so `needs` means *happens-after*, not isolation: `wire`
also sees anything else that merged in the same round.

**Trading reproducibility for throughput.** By default a round is a barrier:
`wire` waits not only for `api` and `db` but for every other lane of their
round, however unrelated. `schedule: eager` drops that wait — a lane is merged
and its dependents spawned as soon as *its own* `needs` are done and merged:

```yaml
  - id: build
    type: fan_out
    schedule: eager             # barrier (default) | eager
    lanes:
      - { id: api,  workflow: implement-module }
      - { id: slow, workflow: implement-module }
      - { id: wire, workflow: implement-module, needs: [api] }
```

The cost is real and worth understanding before you reach for it: **`eager`
makes a lane's starting tree timing-dependent.** Under a barrier `wire` always
starts from the same tree, so re-running the task gives the same result. Under
`eager` it starts when `api` merges, and whether `slow` happened to merge by
then depends on the clock — so a re-run can hand `wire` a different tree. The
parent branch's commit topology varies between runs too, even when the
delivered tree does not. `barrier` is the reproducible default; use `eager` for
a DAG of genuinely independent modules, not for one whose lanes touch shared
files.

Everything else behaves identically under both modes, and a lane list with no
`needs` runs as a barrier whatever you write — one round either way.

**Lanes you cannot list in advance.** Give the step `for_each:` and a single
`lane:` template instead of `lanes:`, and a planning step decides both how many
lanes there are and which of them must land first:

```yaml
  - id: plan
    type: agent
    prompt: "Inspect the repo. Emit one JSON object per work unit, with id and needs."
  - id: build
    type: fan_out
    max_lanes: 24
    for_each: '{{ (index .Steps "plan").Result }}'
    lane:
      id:       '{{ .Item.id }}'
      needs:    '{{ .Item.needs }}'
      workflow: implement-module
      fields:   { module: '{{ .Item.id }}' }
```

Each line of `for_each` must be a JSON object, and `.Item` is that object. The
lane's `workflow:` has to be a literal — it is resolved once, when the task is
created — but everything else may vary per item. Set `max_lanes:`: a list
nobody has produced yet cannot be counted at creation, so that ceiling and
`fan_out.max_tasks` are checked at spawn, before a single worktree exists.

**How the step runs.** Spawning parks the parent in `awaiting_children` and
releases its slot; the scheduler brings it back once every descendant has
settled — or, under `schedule: eager`, once any lane has — and that admission
merges what finished and spawns whatever those merges made ready. Lanes are merged `--no-ff`, one at a time, in **declared**
order, stopping at the first conflict. A lane list with no `needs:` is one such
round — spawn, park, merge, done. You still get one branch to review.

Watch the children with `vincent task ls --include-children`, or press `L` on
the parent in the TUI. They are hidden from the board by default; the parent's
`awaiting_children (2 blocked)` summary is how you learn one of them needs you.

**When lanes block.** A blocked lane never settles, so the join stays open and
the parent stays parked. Fix what they failed on, then retry the **parent** —
`r` in the TUI, `vincent task retry <parent>`, `POST /v1/tasks/{id}/retry`:
one call re-admits every blocked lane under it, at any depth for a nested
fan-out, and the parent stays where it is until they finish. A lane you
**cancelled** is not re-admitted by anything; that one still fails the join with
`lane_failed`.

**When every lane is guarded off**, the step chooses nothing and simply
advances. A fan-out whose conditions all said "not this time" decided
correctly.

#### `merge` and conflicts

| Key | Values | Notes |
|---|---|---|
| `on_conflict` | `block` (default) \| `agent` | |
| `agent` | an agent step | Required by, and only valid with, `on_conflict: agent` |

**`block`** stops the task with `merge_conflict` and leaves the worktree
conflicted, so you resolve it in place, stage the files, and retry — the join
commits your resolution and merges what is left. That is the default
deliberately: an agent silently resolving a semantic conflict, with the merge
commit landing unread, is the one outcome that turns a time-saving feature into
a correctness liability.

**`agent`** tries an agent resolution first. It is an ordinary agent step —
same fields, same validation, same executor — run in the parent's worktree, and
the conflicted files are in its context as `{{.Conflicts}}`. If it fails, or its
check fails, or conflict markers survive it, you get the same block.

```yaml
    merge:
      on_conflict: agent
      agent:
        id: resolve
        prompt: |
          Resolve the merge conflict in:
          {{ range .Conflicts }}- {{ . }}
          {{ end }}
          Keep both sides' intent. Do not delete a test to make it merge.
        check: go build ./... && go test ./...
```

A merge resolver may not declare `on_input: require`: it runs mid-join, and the
worktree state it is resolving is not something a human can inspect through a
question.

**If a lane is cancelled or ends without finishing**, the step blocks with
`lane_failed` and merges **nothing of that round**. Fix the lane — it is an
ordinary task, so retry it — then retry the parent. There is deliberately no
"merge the rest anyway": a partial round looks exactly like a complete one to
everything downstream. Rounds that already merged stay merged — they were on
the branch before this round failed, and un-doing integrated work is not
something vincent does — and no further lane spawns, while lanes already
running are left to finish. With no `needs:` there is only ever one round, so
this is exactly "nothing is merged".

> **A fan-out fills your caps, it does not exceed them.** Every lane is a task
> competing for `max_parallel_tasks`. What it buys is parallelism you would
> otherwise start by hand.

> **N lanes leave N worktrees** on disk until someone archives the tree.
> `vincent gc` and `vincent doctor` exist for this, and you will meet it early
> if you fan out routinely.

### 4.7 `condition`

A step whose entire body is a guard. True continues; **false ends the run and
the task is `done`**. Requires `if:`, and takes nothing else.

```yaml
  - id: nothing-to-do
    type: condition
    if: '{{ ne (index .Steps "probe").ExitCode 0 }}'
```

It takes no `run`, no `timeout`, no `max_retries` and no `allow_failure`,
because it starts no process: there is nothing to time out, retry, or allow to
fail.

The steps after it are never considered and record nothing. The one row it
leaves is in state `stopped`, which is where the detail view shows the run
ended.

There is deliberately no "stop and block for a human" variant, because you
already have one: a `command` step that exits nonzero. What `condition` adds is
*stop and succeed*.

A `condition` in **last** position is a warning, not an error — the task is
`done` whether it continues or stops, so the step cannot do anything a missing
step would not.

### 4.8 `loop`

Runs its body — a **sequence** — repeatedly, in the task's one worktree.
Requires `steps` and exactly one of `count:` / `for_each:`.

Where a `parallel` group is a set run once, a loop is a sequence run more than
once. It produces no branch, no child task and nothing to merge; that is
`fan_out`.

```yaml
  - id: green
    type: loop
    count: 5                     # or: for_each: […]
    max_iterations: 10           # optional ceiling; default loop.max_iterations
    steps:
      - { id: suite,  type: command, run: go test ./..., allow_failure: true, max_retries: 0 }
      - { id: passed, type: break,   if: '{{ eq (index .Steps "suite").ExitCode 0 }}' }
      - id: repair
        type: agent
        prompt: |
          The suite is red:

          {{ (index .Steps "suite").Result }}

          Fix the underlying cause. Do not weaken, skip or delete a test.
```

Loops have enough surface to deserve their own section:
[§6.4](#64-loops-in-depth).

### 4.9 `break`

Ends the enclosing loop, **successfully**. Requires `if:`, takes `id` and
`name`, and nothing else.

```yaml
      - { id: passed, type: break, if: '{{ eq (index .Steps "suite").ExitCode 0 }}' }
```

Like `condition`, it starts no process, so it cannot time out, be retried, or
write a transcript. It is valid **only** inside a loop body — elsewhere there is
no loop for it to end, and that is a validation error rather than a no-op.

### 4.10 `include`

Runs another workflow's steps here, in **this** task and **this** worktree.
This is how you stop copy-pasting the same three verification steps into every
workflow you own.

```yaml
# .vincent/workflows/go-checks.yaml — the fragment
name: go-checks
defaults: { max_retries: 0 }
steps:
  - { id: lint, type: command, run: go run mage.go lint }
  - { id: test, type: command, run: go run mage.go test }
```

```yaml
# .vincent/workflows/feature.yaml — a workflow that uses it
name: feature
steps:
  - { id: implement, type: agent, prompt: "{{ .Task.Description }}" }
  - { id: checks, type: include, workflow: go-checks }
  - { id: review, type: agent, prompt: "lint said: {{ .Steps.lint.Result }}" }
```

Create a task on `feature` and it runs **four** steps: `implement`, `lint`,
`test`, `review`. The include is not one of them. It is replaced by the steps it
names when the task is created, and after that nothing can tell the difference —
which is why `review` reads `.Steps.lint` as if you had typed it there yourself.

Three things follow from that, and they are the whole of what you need to know:

**The include takes no other fields.** No `if:`, no `timeout`, no
`max_retries` — there is no step at run time for them to apply to. To make one
conditional, guard the fragment's own steps, or put a `condition` in front of
it.

**Step ids must not collide.** The expansion is one namespace, so `go-checks`
cannot bring a `lint` if `feature` already has one, and you cannot include the
same fragment twice. Name a fragment's steps as if they were going to sit
beside someone else's, because they are.

**The fragment keeps its own `defaults:`.** `go-checks` says `max_retries: 0`,
and its steps still get 0 after being spliced into a workflow that says 3. A
fragment behaves the way it was written to behave. Your task-level `--agent`
still wins over both.

Everything else is an error when you create the task, with a message naming
what is wrong: a workflow that isn't there, a cycle (`a` includes `b` includes
`a`), more than `include.max_depth` levels (5 by default), a colliding step id,
or a fragment whose `platforms:` rules out this machine. Nothing fails halfway
through a run because of an include.

> **Careful with `condition` in a fragment.** There is no include boundary at
> run time, so a `condition` that stops the fragment stops the *whole task* —
> the steps after the include never run. And a fragment that needs a `break`
> cannot be factored out at all, since `break` only validates inside a loop
> body.

Editing `go-checks.yaml` never affects a task that is already running: the task
took its copy when it was created. New tasks pick up the new version.

### 4.11 Where each type may appear

| Type | Top level | In a `parallel` group | In a `loop` body | In a lane's inline steps |
|---|:--:|:--:|:--:|:--:|
| `agent` | ✅ | ✅ | ✅ | ✅ |
| `command` | ✅ | ✅ | ✅ | ✅ |
| `manual` | ✅ | ❌ | ❌ | ✅ |
| `parallel` | ✅ | ❌ | ❌ | ✅ |
| `fan_out` | ✅ | ❌ | ❌ | ✅ |
| `condition` | ✅ | ❌ | ✅ | ✅ |
| `loop` | ✅ | ❌ | ❌ | ✅ |
| `break` | ❌ | ❌ | ✅ | ❌ |
| `include` | ✅ | ✅ | ✅ | ✅ |

`include` is allowed everywhere because it is gone by the time anything runs.
What gets checked against this table is what it *expands to* — so a fragment
containing a `loop`, included into a loop body, is refused when you create the
task, with the message a hand-written nested loop would get.

A lane's inline steps are a workflow body in their own right — they become a
child task's flat snapshot — so the "top level" column is the one that applies
to them.

The ❌s all come from one fact: a `parallel` group and a `loop` iteration run
**inside a single admission of a single task**, and each rejected type needs a
task state that says "one member of this structure is parked". There is no such
state, and inventing one would mean a task that is half-waiting.

- `manual` — a gate ends the actor goroutine and releases the slot.
- `fan_out` — the task parks in `awaiting_children`; a parked parent has no row
  saying which iteration or which member it parked in.
- `parallel` and `loop` inside each other — both derive their position from rows
  sharing one `step_index`, and that derivation stays affordable exactly one
  level deep.
- `condition` inside a `parallel` group — a group is a *set*, so "end the
  sequence" has nothing to name. Subsetting a group is what `if:` on a sub-step
  already does.
- `on_input: require` inside either — `awaiting_input` holds one pending request
  for the whole task, so a structure whose members each want to ask has nowhere
  to put the answers. This is judged on the **resolved** value, so a
  `defaults.on_input: require` reaching a silent sub-step is caught too.

---

# Making steps talk to each other

## 5. Templates and data flow

### 5.1 Which fields are templates

Five fields are Go [`text/template`](https://pkg.go.dev/text/template):

| Field | On |
|---|---|
| `prompt` | agent steps |
| `run` | command steps |
| `check` | agent and command steps |
| `instructions` | manual steps |
| `if` | any step, plus fan-out lanes |

`for_each:` entries on a `loop` are templates too.

**Rendering uses `missingkey=error`**, and rendering happens *before* any
process starts. A typo fails the step with `template_error` rather than
rendering a silent hole into a prompt, and nothing has run when it does.

Templates are parsed at **load** time as well, so a syntax error is a validation
finding on the file rather than a surprise at run time. What load cannot check
is whether `.Steps.foo` exists — that is a run-time fact.

### 5.2 The template context

| Variable | Fields |
|---|---|
| `.Task` | `ID`, `Title`, `Description`, `Fields` (map), `BaseBranch`, `BranchName` |
| `.Project` | `Name`, `Path` (the original repo root, not the worktree), `DefaultBranch` |
| `.Workflow` | `Name`, `Description` |
| `.Step` | `ID`, `Name`, `Index`, `Attempt` (1-based) |
| `.Steps` | completed steps by id → `{Status, Result, ExitCode}` — [§5.3](#53-passing-one-steps-output-to-the-next) |
| `.Loop` | `Index` (1-based, **0** outside any loop), `Item`, `IsFirst`, `IsLast` — [§6.4](#64-loops-in-depth) |
| `.Issue` | the GitHub issue the task was created from: `Number` (**0** when there is none), `Repo`, `Title`, `Body`, `URL`, `State`, `Labels` (a list), `Author`, `Assignee`, `Milestone`, `MilestoneNumber` — [reference](../reference/workflow-schema.md#issue) |
| `.Host` | `OS`, `Arch` — the **daemon's** GOOS/GOARCH — [§10.4](#104-gating-one-step-by-platform) |
| `.Worktree` | `Path` |
| `.LastFailure` | retry attempts only: `{Reason, Output}`; empty otherwise |
| `.Conflicts` | the conflicted files, on a `merge.agent` resolver only; empty everywhere else |

Two are worth calling out because they are easy to reach for wrongly:

- **`.Project.Path` is the original repository**, not where the step is running.
  The step's working directory is `.Worktree.Path`.
- **There is no `.Now`.** A guard reading wall-clock time makes a run
  non-reproducible, which is the property declared lane order exists to
  preserve.

### 5.3 Passing one step's output to the next

`.Steps` is what makes a multi-step workflow more than a list. Each entry is:

| Field | Value |
|---|---|
| `Status` | `succeeded`, `approved`, `skipped`, or `failed` |
| `Result` | the agent's final result text (agent steps), or the last **200** lines of stdout (command steps) — **stdout only**, never stderr |
| `ExitCode` | the process exit code, where there was one |

```yaml
  - id: survey
    type: agent
    prompt: List every place this API is called. One per line, no commentary.

  - id: fix
    type: agent
    prompt: |
      A previous step reported:

      {{.Steps.survey.Result}}

      Update exactly those call sites.
```

Splitting work this way beats one prompt asking for both halves: each half is
individually retryable, and the first half's output is a thing the second half
has to satisfy.

**What appears in `.Steps`, and when:**

- A step that **succeeded**, was **approved** at a gate, or was **skipped** by
  its guard appears as soon as the run moves past it.
- A step that **failed** appears only once the engine has advanced past it —
  which happens only under `allow_failure`
  ([§6.2](#62-allow_failure--a-failure-that-is-data)). That is the whole point
  of that field.
- A step's **own** failed attempt is never in `.Steps["itself"]` mid-retry.
  `.LastFailure` is already that channel, and two spellings of one fact is how
  a context rots.
- **Members of a `parallel` group are invisible to each other**, by the same
  rule: nothing is visible until the engine has advanced past it, and
  concurrent siblings never have.
- **Inside a loop body, earlier steps of the current iteration are visible to
  later ones.** Under repetition an id resolves to its **latest** iteration.
- `interrupted` never appears — it is not an outcome.

**`Result` is a tail, not a stream.** The last **200 lines or 256 KiB** of
stdout, whichever binds first, is generous for a summary and useless as a data
channel — and a list has to fit both: a hundred JSON objects of 3 KiB each is
well under the line count and over the byte one, and the earliest of them are
what goes. Both bounds cut by dropping whole **leading** lines, so an over-long
[`for_each:`](#64-loops-in-depth) list arrives shorter rather than with a
half-written item at its edge; only a single line longer than 256 KiB on its
own is cut mid-line. A step meant to feed `for_each:` should still filter at
the source rather than lean on the tail.

**`Result` is stdout, and only stdout.** That is what makes it usable as a data
channel at all: `git` writes `Switched to branch …` and its fetch progress to
stderr on success, `curl` writes its progress meter there, and tools announce
deprecations there. None of it reaches `.Result`, so a step whose stdout is a
list can also be noisy. It is also the place to put anything meant for a human
reading the run rather than for the template consuming it — a header, a count,
a note — since stderr still reaches the transcript, the step's summary and the
`<previous-attempt-failure>` block a retry sees.

### 5.4 Task fields

`.Task.Fields` is an open `map[string]string` supplied when the task is created.
It is how one workflow serves many jobs — a ticket id, a module name, a "ship
it: yes/no" flag. A workflow can predeclare the fields it expects so the TUI
can render the right rows before a task exists:

```yaml
fields:
  - name: ticket
    label: Ticket
    description: Issue tracker key.
    required: true
    pattern: '^OPS-[0-9]+$'
  - name: retries
    type: integer
  - name: confidence
    type: number
  - name: dry-run
    label: Dry run
    type: boolean
  - name: environment
    type: enum
    required: true
    values: [dev, staging, prod]
    default: staging
  - name: reviewers
    type: enum
    multiple: true
    values: [ana, bo, cy]
```

The definition order is the form order. `string` is the default type;
`integer` accepts base-10 whole numbers, `number` accepts finite decimal values,
and `boolean` accepts exactly `true` or `false`. `pattern` is a Go RE2 expression
for strings only — anchor it with `^` and `$` when the whole value must match.
`label` and `description` only improve presentation.

`enum` is the type for a closed set. Reach for it instead of
`pattern: '^(dev|staging|prod)$'`: the members are *published*, so New task can
draw a picker and `GET /v1/workflows` can hand the list to any other client,
which a regex can never do. `multiple: true` lets more than one be picked; the
value is then the members joined with `,` in declared order (`dev,prod`), which
the daemon normalizes to, so the same selection is always the same string in a
template and a branch name. A member may not contain `,`, and `pattern` and
`enum` cannot be combined.

Any field may carry a `default:`, written as its own YAML scalar — `default: 3`
on an integer, `default: true` on a boolean. The daemon fills in a **required**
field's default when a caller omits the key, so a scripted `vincent task add`
does not have to restate it; an **optional** field's default is seeded by the
TUI but never invented server-side, so an optional key you omit is still absent
from `.Task.Fields` and `{{ with index .Task.Fields "x" }}` keeps working the
way it always did.

The daemon checks required/type/pattern/membership rules at task creation,
including for CLI and API callers. The selected root workflow owns the contract: fields from
included workflows and named fan-out lane workflows are not automatically
merged, so a composing workflow re-declares any input it exposes.

Declarations do **not** close the map. Extra fields remain accepted, recorded,
inherited by lanes, and visible to templates. Both of these travel together:

```sh
vincent task add --project 1 --workflow feature-pr --title "Ship it" \
  --field ticket=OPS-42 --field owner=ana
```

Here `ticket` may be declared and validated while `owner` is additional
metadata; both are stored as strings in `.Task.Fields`.

Generated input, or a value with newlines in it, goes in as a JSON object
instead — `--fields-file PATH`, or `-` for stdin. A `--field` of the same name
still wins, so one document can be reused across runs that vary one input; see
[Supplying task fields](scripting.md#supplying-task-fields).

Because rendering is strict, an **optional** field must be read defensively or
the step fails:

```yaml
    prompt: |
      Implement {{.Task.Title}}.
      {{ with index .Task.Fields "ticket" }}Related ticket: {{ . }}{{ end }}
```

`{{ index .Task.Fields "ticket" }}` on its own is correct only for a field the
workflow *requires*, where failing loudly is what you want.

### 5.5 Environment variables

<a id="the-vincent-environment"></a>

Agent steps, command steps and checks run with the working directory set to the
worktree and receive these on top of the daemon's own environment:

| Variable | |
|---|---|
| `VINCENT_TASK_ID` | |
| `VINCENT_TASK_TITLE` | |
| `VINCENT_PROJECT_NAME` | |
| `VINCENT_PROJECT_PATH` | the original repo root |
| `VINCENT_WORKTREE` | the working directory |
| `VINCENT_BRANCH` | |
| `VINCENT_BASE_BRANCH` | |
| `VINCENT_STEP_ID` | |
| `VINCENT_STEP_ATTEMPT` | 1-based |
| `VINCENT_WORKFLOW` | |

These are the portable way to reach task context from a script you would rather
keep out of the YAML:

```yaml
  - id: notify
    type: command
    run: ./scripts/notify.sh
    env:
      SLACK_CHANNEL: "#builds"
```

A command step's `env:` is layered on top and wins. What the daemon passes down
in the first place is itself configurable — see
[`environment`](../reference/configuration.md#environment) — but neither that
policy nor a step's `env:` can remove a `VINCENT_*` variable: those are facts
about the run, not inherited state.

An **agent** step gets the same block, without the `env:` layer — `env:` is a
command-step field. That is what lets an agent call
[`vincent status`](#56-reporting-status-from-a-step) from its own shell tool: the
command reads `VINCENT_TASK_ID` and `VINCENT_STEP_ID` and needs nothing else.

### 5.6 Reporting status from a step

A running step can say what it is doing, in a sentence, by running
[`vincent status`](../reference/cli.md#vincent-status) from inside itself:

```yaml
  - id: suite
    type: command
    run: |
      vincent status "running the store suite"
      go test ./internal/store/...
```

The message shows up live on the board's `STATUS` column and on the attempt line
in the [TUI](tui.md), and the last value set before the step ends stays on the
finished attempt. It is the answer to "what is this actually doing" for a step
that has been running for twenty-five minutes, and to "why did that fail" in
terms a `failure_reason` like `check_failed` cannot reach.

For an **agent** step, ask for it in the prompt. The daemon deliberately does
not append any instruction of its own, so an agent reports its status only
because you asked:

```yaml
  - id: implement
    type: agent
    prompt: |
      Implement {{.Task.Title}}.

      Before each significant phase of work, run:
        vincent status "<one short line about what you are doing now>"
      Keep it under ten words. If you get stuck or something fails, set it to
      what is actually wrong — "3 tests red in internal/store", not "working".
```

Worth knowing when you write that instruction:

- Only `agent` and `command` steps can report. `manual`, `parallel`, `fan_out`,
  `condition`, `loop` and `break` run no process, so they have no voice and
  their status stays empty.
- The message is flattened to one line, stripped of control characters and
  truncated to 256 bytes. Being wordy never fails the step.
- Two messages inside one second coalesce to the later one, so an agent that
  narrates in a tight loop costs nothing. The first message after a quiet
  period is always immediate.
- It is **not** a failure reason and nothing renders it as one. It is also not
  visible to `.Steps` or to an `if:` guard — free text an agent chose at run
  time is not something a workflow should branch on.

---

## 6. Control flow

A workflow can decide at run time what to do next. Three fields and two step
types do it, and they compose.

### 6.1 `if:` — skip a step and carry on

Any step may carry a guard, and so may a fan-out lane. It renders like every
other template here and must produce, after trimming, exactly `true` or
`false`.

```yaml
  - id: changelog
    type: agent
    if: '{{ eq (index .Task.Fields "changelog") "yes" }}'
    prompt: Update CHANGELOG.md.
```

**A false guard skips that step and the workflow continues.** The step still
appears in the task's step list, in state `skipped` with skip reason
`condition` — so you can tell it apart from a step you skipped by hand — and
downstream templates can see it in `.Steps`.

**On a set rather than a sequence**, `if:` means *do not start this one*: on a
fan-out lane and on a `parallel` sub-step, the others still run and the join
still happens. A set has no "later" to skip to, so a false guard subsets it.

**Nothing renders but `true` or `false`.** `yes`, `1`, an empty string and
`<no value>` are all `condition_error`, not a guess. That strictness is the
point: the two things a broken guard usually produces are an empty string and a
truthy word, and both mean the guard is reading something that is not there —
the one case where guessing a verdict is worse than refusing to have one.
`condition_error` is also the one failure that is **not** retried: re-rendering
a guard cannot answer differently. Fix the workflow and retry.

**Guards are re-evaluated every single time** — on each attempt, on a retry, and
after a daemon restart. Nothing is cached. Fix a workflow and retry a blocked
step whose guard is now false, and the step is skipped. That is the point.

### 6.2 `allow_failure:` — a failure that is data

Without this, a guard can only read what a human typed when the task was
created. A command step that exits nonzero **blocks the task**, so no step that
survived long enough to be read ever has a nonzero exit code to branch on.

`allow_failure: true` changes that for one step: once its retry budget is spent,
the workflow **advances instead of blocking**. The row keeps its `failed` state
and its reason — the failure happened, it just did not stop the run — and that
row is what the next guard reads.

```yaml
  - id: probe
    type: command
    run: git diff --quiet HEAD~1
    allow_failure: true
    max_retries: 0               # a probe has nothing to gain from retrying
  - id: stop-if-clean
    type: condition
    if: '{{ ne (index .Steps "probe").ExitCode 0 }}'
```

**It only swallows failures the step itself produced:** a nonzero exit, a failed
`check`, an agent error, a timeout, a transcript-cap kill. It never swallows
vincent failing to *run* the step — a missing CLI, an expired login, a template
error, an unavailable shell — nor vincent failing to *record* it
(`transcript_io_error`, `agent_protocol_error`). Branching on "the agent is not
installed", or on "the disk filled up", as though either were a test result is
not something a workflow should be able to do.

It is valid on `agent` and `command` steps only, sub-steps of a group included,
and it is not available in `defaults:`.

### 6.3 `condition` — finish early and succeed

Where `if:` skips one step, a [`condition` step](#47-condition) ends the run:
false means the sequence stops here and the task is `done`.

The pairing that makes both useful is **probe, then decide, in two steps**:

```yaml
  - id: probe
    type: command
    run: git diff --quiet {{.Task.BaseBranch}}...HEAD
    allow_failure: true
    max_retries: 0

  - id: only-if-changed
    type: condition
    if: '{{ ne (index .Steps "probe").ExitCode 0 }}'

  - id: publish
    type: command
    run: git push -u origin {{.Task.BranchName}}
```

Trying to do both in one step means a step whose failure is sometimes a failure
and sometimes an answer, which nothing downstream can tell apart.

### 6.4 Loops in depth

Three shapes need a loop, and `max_retries` was never any of them: a retry
re-runs *the same step* on *its own* failure, with no way to say "run the probe,
and if it is red run a **different** step, then probe again".

#### 6.4.1 Converge — fix until green

The archetype the feature exists for:

```yaml
  - id: green
    type: loop
    count: 5
    steps:
      - { id: suite,  type: command, run: go test ./..., allow_failure: true, max_retries: 0 }
      - { id: passed, type: break,   if: '{{ eq (index .Steps "suite").ExitCode 0 }}' }
      - id: repair
        type: agent
        prompt: |
          The suite is red:

          {{ (index .Steps "suite").Result }}

          Fix the underlying cause. Do not weaken, skip or delete a test.
```

Read it in three lines: the probe is `allow_failure`, so its red exit is *data*
rather than a block; the `break` reads that data and ends the loop the first
time it is green; the repair only runs when the break did not fire. The `count:`
is the budget — five repairs and no more.

#### 6.4.2 Repeat — the same thing, N times

Prove a flake is gone without ten copy-pasted steps and ten step ids:

```yaml
  - id: soak
    type: loop
    count: 10
    steps:
      - { id: race, type: command, run: go test -race ./internal/taskrun }
```

#### 6.4.3 Iterate — once per item

Over a list a step discovered at run time:

```yaml
  - id: changed
    type: command
    run: git diff --name-only {{.Task.BaseBranch}}...HEAD -- '*.go' | grep -v _test.go
    allow_failure: true

  - id: review-each
    type: loop
    for_each: '{{ .Steps.changed.Result }}'
    max_iterations: 25
    steps:
      - { id: read, type: agent, prompt: 'Review {{ .Loop.Item }} against CLAUDE.md.' }
```

`fan_out` looks like it should fit here and does not: its lane list has to be
static in the snapshot, which is exactly what lets it check for cycles and cap
the tree at creation.

#### 6.4.4 The two drivers

| Driver | Shape | Iterations |
|---|---|---|
| `count:` | an integer | exactly that many, unless a `break` fires |
| `for_each:` | a template string, or a list of them | one per item |

`for_each:` accepts both spellings and treats them identically: **each entry is
rendered, trimmed, and split on newlines with empty lines dropped**. So
`[api, web]` and a command's multi-line output are one mechanism, not two. The
scalar spelling exists because the motivating case is
`for_each: '{{ .Steps.changed.Result }}'`, and wrapping that in a one-element
sequence would be ceremony.

Exactly one driver per loop. Both, or neither, is a validation error.

#### 6.4.5 `.Loop`

| Field | |
|---|---|
| `.Loop.Index` | the 1-based iteration, and **0** outside any loop, so a shared template can tell |
| `.Loop.Item` | the `for_each` item this pass runs on; empty for a `count:` loop |
| `.Loop.IsFirst` / `.Loop.IsLast` | booleans |

`Item` is a **string**, deliberately. Every other value in the context is a
string, and a structured item would push a new type through the render context,
the API and the TUI for a case nobody has yet.

#### 6.4.6 There is no `while:`

A guard can read only what a run has already produced. On the first iteration
the body has not run, so a `while:` reading its own body either errors out
loudly or — worse — quietly renders false and the loop never runs; and one
reading a step *before* the loop reads a constant and spins to the ceiling.

`count:` plus `break` is the same loop written correctly: post-test by
construction, with the condition in the body where it can see the body, and
bounded by a ceiling `while:` would have needed anyway.

#### 6.4.7 `continue` is spelled `condition`

A `condition` step inside a loop body ends **that iteration**, not the run — a
loop body is a sequence, and "end the sequence" ends the pass. The same word
means the same thing everywhere; what changes is what it is attached to.

#### 6.4.8 How a loop ends

| Cause | Result |
|---|---|
| The driver runs out, or a `break` fires | The loop **succeeds** and the run advances |
| A `condition` in the body is false | **That iteration** ends; the loop carries on |
| The loop cannot run within the ceiling | The task **blocks** with `loop_limit` |
| A body step exhausts its retries | The task blocks with **that step's** own reason |

The ceiling is the step's own `max_iterations:` when it declares one, else
`loop.max_iterations` from config (default 10). A `count:` above it is a
**validation error at load** — the last moment it can be reported to the person
typing it. A `for_each:` list longer than it blocks with `loop_limit` before the
first iteration, naming the count, rather than quietly doing the first ten:
running out of tries is not a decision the workflow made, and advancing would
tell every later guard the work is finished.

Retries and iterations are different things. `max_retries` is for a step that
*failed*; an iteration is for a body that *succeeded and must run again*. Each
body step spends its own budget within each iteration. A `loop` itself takes no
`max_retries` and no `allow_failure` — it has no attempt of its own, and "allow"
on a loop could only mean "a `loop_limit` block advances anyway", which is the
silent success the design refused.

#### 6.4.9 Human actions on a loop

| Action | Effect |
|---|---|
| `skip` | Skips the **whole loop** and advances past it. There is no "skip this iteration" |
| `retry` | Resumes at the failed body step **of the iteration it stopped in**, with a fresh budget. It does not restart at iteration 1 |
| `edit + retry` | Rewrites that body step in the task's snapshot, so the fix applies to **every remaining iteration** |

The same is true after a crash: a loop's position is derived from its rows on
every admission, so a restarted daemon resumes mid-iteration rather than redoing
an hour of work.

> **A loop is one step**: one index, one slot, one worktree, one timeline entry,
> and its iterations are strictly sequential. It adds no concurrency your caps
> cannot see. What it adds is *spend* — ten iterations of a three-step body is
> thirty agent runs, which is why the default ceiling is 10.

### 6.5 Guard recipes

| What you want to ask | Write |
|---|---|
| Did step `x` succeed? | `{{ eq (index .Steps "x").Status "succeeded" }}` |
| Was step `x` skipped by its guard? | `{{ eq (index .Steps "x").Status "skipped" }}` |
| Did probe `x` fail (under `allow_failure`)? | `{{ ne (index .Steps "x").ExitCode 0 }}` |
| Is this not Windows? | `{{ ne .Host.OS "windows" }}` |
| Did the creator ask for it? | `{{ eq (index .Task.Fields "ship") "yes" }}` |
| Is this the first pass of a loop? | `{{ .Loop.IsFirst }}` |
| Is this step inside a loop at all? | `{{ gt .Loop.Index 0 }}` |
| Is a field present at all? | `{{ if index .Task.Fields "ticket" }}true{{ else }}false{{ end }}` |

`.Steps.x.Status` is valid shorthand for `(index .Steps "x").Status` when the id
is a bare identifier; the `index` form is required for ids containing `-` or
`.`.

---

# Making it reliable

## 7. Verification with `check`

### 7.1 Why every writing step wants one

`check` is a **field** on `agent` and `command` steps, not a step type. It is
the difference between "the agent said it was done" and "the compiler agrees".

```yaml
  - id: implement
    type: agent
    prompt: …
    check: go build ./... && go test ./...
    check_timeout: 15m
```

**If a step produces something checkable, check it.** If a step genuinely
produces nothing checkable, that is usually a sign it is doing two things.

### 7.2 How a check runs

- After the step body, in the worktree, with the same environment as a command
  step ([§5.5](#55-environment-variables)) and under the same shell rules
  ([§10.1](#101-which-shell-runs-a-command-step)).
- Its output is captured to the step's transcript.
- **Non-zero fails the attempt** with reason `check_failed`, and the retry gets
  the failure appended to its prompt automatically:

```
<previous-attempt-failure attempt="1">
reason: check command failed (exit 1)
--- output (last 200 lines) ---
…
</previous-attempt-failure>
```

- `check_timeout:` bounds it, defaulting to the daemon's
  `defaults.command_timeout` (15m) — **not** to the step's own `timeout` and not
  to workflow `defaults.timeout`. A long agent step with a short check is the
  usual shape, so set `check_timeout:` explicitly when the check is the slow
  half.

### 7.3 Inverting a check

When the deliverable *is* a failure, invert it:

```yaml
  - id: reproduce
    type: agent
    prompt: Write a test that fails, reproducing the bug described above.
    check: '! go test ./...'
```

Without this, an agent asked to "add a failing test and fix it" reliably writes
a test that passes against its own fix. See
[`fix-and-test.yaml`](../../examples/fix-and-test.yaml). Note that `!` is POSIX
shell syntax — a file relying on it should declare `platforms: [posix]` or pin
`shell: sh`.

### 7.4 What a check is not

- It is not a second step: it has no id, no row of its own, and no retry budget
  separate from its step's.
- It cannot be set in `defaults:`.
- It is not available on `manual`, `parallel`, `fan_out`, `condition`, `loop` or
  `break` steps. To verify after a group, put a `command` step behind it.

---

## 8. Failure, retries and timeouts

### 8.1 What counts as success

| Type | Succeeds when |
|---|---|
| `agent` | the process exits 0, **and** the stream produced a terminal result rather than an error, **and** any `check` exits 0 |
| `command` | the command exits 0, **and** any `check` exits 0 |
| `manual` | a person approves |
| `parallel` | every sub-step succeeds |
| `fan_out` | every lane finishes and every branch merges |
| `loop` | the driver runs out or a `break` fires |
| `condition` | its guard is true (false ends the run, also successfully) |

### 8.2 Retries

```yaml
defaults:
  max_retries: 2      # attempts AFTER the first — so up to three
```

The default is `1`, i.e. up to two attempts. `0` means one attempt and no
retry, which is right for probes and for anything with a side effect you would
not want repeated — a push, a release, a comment on an issue.

Set it per step where the step's cost says so:

```yaml
  - id: publish
    type: command
    run: git push -u origin {{.Task.BranchName}}
    max_retries: 0
```

Retries are for failures a retry can plausibly fix. `condition_error` is
excluded by construction, and an `interrupted` attempt does **not** consume
budget. `parallel` groups and `loop` steps carry no retry budget of their own —
their members do.

**`retry_backoff:` paces them.** By default a retry is immediate, which is right
for a compile error the agent can see and fix, and wrong for anything transient
— a flaky network call, a `git index.lock` held by another process — where two
guaranteed failures inside a few seconds spend the budget on nothing:

```yaml
  - id: smoke
    type: command
    run: ./scripts/smoke.sh
    max_retries: 2
    retry_backoff: 30s   # wait before each retry; 0s (the default) is immediate
```

A non-zero value does **not** sleep. The task goes back to `queued` with the
resume time attached, **gives up its concurrency slot** so other work keeps
running, and re-runs the step by itself when the wait is over — the same
mechanism a usage limit uses, and it needs nothing from you. The board shows
`queued → 14:20` and the detail header `queued · retry backoff → 14:20`.

The wait decides *when* an attempt happens, never *whether* there is one. The
attempt still counts against `max_retries`, and a step out of budget blocks at
once however long its backoff. So a task that keeps reappearing on a backoff is
on its way to blocking; see
[Troubleshooting](troubleshooting.md#retry_backoff--also-do-nothing-but-for-a-different-reason)
for telling it apart from a quota wall.

It is settable in `defaults:` and per step, where the step wins — including
`retry_backoff: 0` on one step to opt it out of a workflow-wide default. It is
refused on `condition`, `break`, `loop` and `include` steps, exactly as
`max_retries` is, and the [`on_conflict: agent`](#merge-and-conflicts) merge
resolver never waits: its attempts belong to the join.

### 8.3 Timeouts

`timeout:` bounds **one attempt**, and can be set in `defaults:` or per step.
Without either it is the daemon's `defaults.agent_timeout` (60m) for agent steps
and `defaults.command_timeout` (15m) for everything else.

On a `parallel` group or a `loop`, `timeout:` bounds the whole structure rather
than one member.

Two related bounds that are *not* the step timeout:

- **`check_timeout:`** — [§7.2](#72-how-a-check-runs).
- **`input_timeout:`** — bounds each wait in `awaiting_input`, per request. The
  step's own timeout clock **pauses** while a question is pending: it measures
  agent work, not human latency.

### 8.4 When a step runs out of attempts

The task **blocks**, carrying that step's failure reason, and waits for a human.
Nothing is silently abandoned and nothing downstream runs. From there you can
retry, skip, edit + retry, or cancel.

The one exception is [`allow_failure:`](#62-allow_failure--a-failure-that-is-data),
which turns the step's own failures into an advance.

The full reason vocabulary — `check_failed`, `nonzero_exit`, `agent_error`,
`timeout`, `loop_limit`, `merge_conflict`, `lane_failed`, and the rest — is in
[Task lifecycle → failure reasons](../reference/task-lifecycle.md#failure-reasons).
A reason means the same thing wherever it originated.

### 8.5 Interruption is not failure

Vincent is crash-first: every transition is persisted before it is acted on. A
daemon that stops mid-step marks the run `interrupted`, kills verified orphan
processes, and re-runs the step as a **fresh attempt that does not consume a
retry**.

This is safe because every agent step is a fresh session over a worktree whose
committed state survives. **You help by having agents commit incrementally** —
say so in the prompt. A step that does an hour of work and commits at the end
loses the hour; one that commits as it goes loses minutes.

---

## 9. Agents, models and permissions

### 9.1 Resolution order

For each agent step, `agent`, `model` and `effort` resolve first-hit-wins:

1. the explicit **step** field;
2. the **task-level override** chosen at creation (`--agent`, `--model`,
   `--effort`) — this replaces workflow `defaults`, never an explicit step
   field;
3. workflow **`defaults`**;
4. the **adapter** default — usually empty, meaning the CLI decides.

**Agent-scoped inheritance.** `model` and `effort` only inherit from a level
whose resolved agent matches the step's. A step that switches agent without
setting them gets the new adapter's defaults rather than a claude alias leaking
onto a codex step.

```yaml
defaults:
  agent: claude
  model: sonnet

steps:
  - id: implement
    type: agent          # → claude / sonnet
    prompt: …
  - id: review
    type: agent
    agent: codex         # → codex / codex's default model, NOT sonnet
    effort: high
    prompt: …
```

`POST /v1/resolve` answers "what does this resolve to, and which level won" for
every step in a workflow. That is what the TUI's new-task form renders; no
client re-implements the precedence. For a file you are still editing —
which the registry has frequently not picked up yet —
`vincent workflow render <file>` prints the same triple offline, from the same
resolver.

### 9.2 What each adapter can do

Adapters differ, and the differences are documented rather than smoothed over. A
capability an adapter lacks is ignored at run time; it is never emulated.

| | claude | codex | cursor |
|---|:--:|:--:|:--:|
| Mid-run questions (`on_input`) | ✅ | — | — |
| Reports cost | ✅ | — | — |
| `effort:` | ✅ | ✅ | **—** (it lives in the model id) |
| Model catalog | ✅ | — (account-dependent; free text) | ✅ (probed over the network) |
| `restricted` on Windows | ✅ | ✅ | **—** (the task is refused at creation; it never downgrades) |

`vincent workflow validate` catches a model or effort value belonging to
*another* adapter's catalog — claude's `sonnet` or `max` reaching a codex step
is an error. A value in **no** catalog is a warning and passes: the CLI is the
final authority at run time, so free-text models and future CLI values are not
blocked. What validation cannot catch is a model your account lacks.

Full details per adapter: [Agent CLIs](agents.md).

### 9.3 Permission modes

```yaml
permission_mode: full-auto     # default
```

`full-auto` is the default and **the agent can run arbitrary commands as you**.
The worktree isolates collisions between tasks, not privileges. This is a
documented design decision, not an oversight — see the
[Security model](../security-model.md).

`restricted` maps to each adapter's own confinement. Use it for steps that have
no business running commands — a docs pass, a review. Three things to know:

- An adapter that **cannot** restrict on your platform never runs the step
  unrestricted. Vincent knows this without asking the CLI, so **creating** such
  a task is refused with a `400` naming the step and the agent; a task that
  reaches the engine anyway — the workflow changed, or the data directory came
  from another OS — fails it with `restricted_unsupported`. A restricted mode
  that quietly is not restricted is worse than none.
- It changes agent behaviour in ways that read as bugs if you did not ask for
  them: claude turns every non-allowlisted tool into a permission prompt. That
  is why the shipped examples are all `full-auto`.
- **It bounds the filesystem and the shell, not vincent.** A restricted step
  still gets [vincent's own MCP tools](mcp.md) in full, so it can create, cancel
  and archive tasks. Offering the tool list and denying every call would be
  worse; if a step should not reach vincent either, that is
  [`mcp.wire_steps: false`](../reference/configuration.md#mcp), which is a daemon
  setting rather than a step one.

### 9.4 Mid-run questions (`on_input`)

An input-capable adapter (claude today) can surface a **structured** question
from the agent mid-step. Only machine-readable requests from the event stream
qualify — vincent never guesses that some output text "looks like a question".

```yaml
defaults:
  on_input: wait        # wait (default) | deny | require
  input_timeout: 24h    # bounds each wait; also settable per step
```

**`wait`** — the task moves to `awaiting_input` and **keeps its concurrency
slot**: the agent process is alive, idle on its stdin. The step's timeout clock
pauses. The board pins the task, rings the terminal bell, and the answer form
opens on `enter`. Answering resumes the same session where it stopped.

**`deny`** — for runs that must stay strictly unattended. Vincent answers
immediately through the adapter: questions get a canned "no user is available;
decide with your best judgment", and permission requests are denied. The task
never leaves `running`.

**`require`** — `wait`, plus a promise that a human is part of the run. Use it
when the conversation *is* the workflow:

```yaml
  - id: clarify
    type: agent
    on_input: require
    prompt: Ask me whatever you need before you start, then implement it.
```

`wait` and `deny` both degrade quietly on an agent with no control channel: the
step runs, nothing is asked, and the agent decides alone. That is usually what
you want — but not for a step built around asking you which of three designs to
take. There, an agent that cannot ask does not degrade, it guesses, and nothing
in the run says so. `require` makes that explicit, and you notice it in four
places:

- **In the file.** Pinning `agent: codex` or `agent: cursor` on a requiring step
  is a validation error — those CLIs have no control channel in any version.
- **At task creation.** The agent picker greys out an agent that cannot answer
  questions, and `POST /v1/tasks` refuses one with a 400 naming the step. A
  workflow whose requiring steps leave their agent to the task is marked *needs
  an interactive agent* in the picker.
- **Never on a guess.** An agent that is not installed, or whose probe did not
  answer, is *unknown* — and unknown never refuses anything. Only a definite
  "this one cannot" blocks you.
- **At run time.** If the answer changes underneath you — claude upgraded past
  the version family vincent has verified the protocol against — the step fails
  with `input_unsupported` instead of running an unattended conversation.

`require` is not valid inside a `parallel` group, inside a `loop` body, or on a
`merge.agent` resolver: `awaiting_input` holds one pending request for the whole
task. A step's own `on_input` still wins over `defaults:`, so a
mostly-interactive workflow can mark one long cleanup step `on_input: deny` and
leave it unattended.

---

## 10. Portability

Windows, macOS and Linux all run vincent, and **vincent never translates a
command step between shells**. Portability of command steps is the author's job
— or an explicit declaration that you did not do it.

### 10.1 Which shell runs a command step

`run` and `check` are rendered, then handed to one shell as a single script:

| Platform | Shell |
|---|---|
| POSIX | `/bin/sh -c "<rendered>"` |
| Windows | `pwsh -NoProfile -Command "<rendered>"`, falling back to Windows PowerShell (`powershell`) |

Pin one with `shell:` when a step is only ever meant to run one way:

| Value | Runs as |
|---|---|
| `sh` | `/bin/sh -c` (on Windows: whatever `sh` is on `PATH` — Git Bash's, if you put it there) |
| `pwsh` | `pwsh -NoProfile -Command` |
| `cmd` | `cmd /C` |

Those three are the whole vocabulary. **`bash` is not a value** — use `sh` for
portable POSIX syntax, or invoke bash explicitly with
`run: bash -c '…'` when you genuinely need bash-only features. A pinned shell is
never silently replaced: if it is not installed, the step fails with
`shell_unavailable`.

### 10.2 Writing portable command steps

If a file must run everywhere, spell its command steps in the intersection of
`sh` and `pwsh`:

| Works in both | Does not |
|---|---|
| `git …`, and any binary on `PATH` | `test -f x`, `[ -f x ]` |
| `&&` and `\|\|` between simple commands | `for` / `if` / `case` |
| `exit N` as the whole command | `touch`, `seq`, `cat`, `grep` |

Two tricks worth knowing, both borrowed from vincent's own acceptance gates:

- A step that must **write a file** portably: `git config -f <file> <key> <value>`.
- A step that must **pass or fail on a condition**: use one command whose own
  exit code says so. `git config --get <key>` exits 1 on a missing key.

`exit N` must be the *whole* command body. In pwsh, `&&` takes pipelines, so
`… && exit 0` parses as a command named `exit` and fails.

If that list feels like a straitjacket, it is — which is what
[§10.3](#103-platforms--declare-what-you-did-not-port) is for.

### 10.3 `platforms:` — declare what you did not port

A workflow that pipes `cat` into `wc` is a POSIX workflow. Say so once, at the
top of the file:

```yaml
name: posix-tools
platforms: [posix]
```

| Token | Matches |
|---|---|
| `linux` | Linux |
| `darwin` | macOS |
| `windows` | Windows |
| `posix` | Every non-Windows host — the shorthand for "needs a POSIX shell" |

Any combination is allowed: `[linux, darwin]` is `[posix]` spelled out, and
`[posix, windows]` is the same as omitting the key. Tokens are matched exactly —
`macos` and `Linux` are validation errors, not silent non-matches. Duplicates
are errors too.

On a host the list does not admit:

- the workflow is still **listed** — by `vincent workflow ls` with status
  `unsupported`, and in the TUI's workflow view, with the platforms it needs;
- it cannot be **selected**: the new-task picker refuses it and `POST /v1/tasks`
  rejects it with a 400 naming the restriction and the host;
- a task that somehow already carries it — a data directory moved between
  machines — blocks at admission with `platform_unsupported`, before any step
  runs.

`vincent workflow validate` checks the tokens but **never** the host, so a
POSIX-only workflow validates the same on a Windows CI runner as on Linux. That
is what makes it usable as a portable pre-commit check.

The restriction is **whole-workflow**. Omitting the key means "runs anywhere",
which is the right answer for most files.

### 10.4 Gating one step by platform

There is no per-step `platforms:`, and none is needed: `.Host` plus an ordinary
guard says the same thing with skip semantics that are already defined.

```yaml
  - id: unix-only-cleanup
    type: command
    if: '{{ ne .Host.OS "windows" }}'
    run: rm -rf ./tmp
```

`.Host` is the **daemon's** GOOS and GOARCH, because the daemon is what runs the
steps. Use `platforms:` when the whole file should not be *offered*; use `if:`
when one step should be *skipped*.

---

## 11. Concurrency, cost and limits

Four different limits bound a run. They are easy to confuse, and only the first
counts tasks.

| Limit | Counts | Default | Set in |
|---|---|---|---|
| `max_parallel_tasks` | concurrently running **tasks** | 3 | `config.yaml`, plus a per-project cap |
| `parallel.max_parallel` | sub-steps of **one group** at once | 4 | `config.yaml`, or `max_parallel:` on the step |
| `fan_out.max_depth` / `max_tasks` | depth and size of a **fan-out tree** | 3 / 64 | `config.yaml`, checked at task creation |
| `loop.max_iterations` | iterations of **one loop** | 10 | `config.yaml`, or `max_iterations:` on the step |

What follows from that:

- **A `parallel` group hides work from the board.** One task at
  `max_parallel: 8` starts eight processes while the board shows a single
  running task. Size it for the machine.
- **A `fan_out` fills your caps rather than exceeding them.** Every lane is a
  task competing for `max_parallel_tasks` like any other.
- **A `loop` adds no concurrency at all** — its iterations are strictly
  sequential. What it adds is spend: ten iterations of a three-step body is
  thirty agent runs.
- **Steps that release their slot**: `manual` (`awaiting_gate`) and `fan_out`
  while its children run (`awaiting_children`). A step waiting on a mid-run
  question (`awaiting_input`) **keeps** its slot, because the process is alive.

Three more bounds worth knowing when a run behaves oddly:
`transcript_max_bytes` (512MB per attempt, then `transcript_limit`);
`usage_limit`, which is not a failure — the task waits `queued` until the
agent's quota window reopens, consuming no retry; and
[`max_task_cost_usd`](../reference/configuration.md#max_task_cost_usd), off by
default, which blocks a task with `cost_limit` once its spend across every
attempt passes a ceiling you set. That last one counts **one task**, so each
fan-out lane carries its own budget, and it only sees agents that report cost.

---

# Shipping

## 12. Patterns that work

**Split what an agent does badly in one go.** A survey step whose output feeds a
fix step beats one prompt asking for both: `.Steps` carries the first result
into the second, and each half is individually retryable.

**Make the deliverable checkable.** `check: go build ./... && go test ./...` on
the step that writes code turns a claim into a verdict. A step with nothing
checkable is usually doing two things.

**Invert the check when failure is the point.** `check: '! go test ./...'` on a
step whose job is to produce a red test — see
[`fix-and-test`](../../examples/fix-and-test.yaml).

**Gate before anything leaves the machine.** Put a `manual` step in front of any
step that pushes, publishes, deploys or deletes. The gate costs one keystroke
and is the difference between "an agent wrote to a branch" and "an agent wrote
to production".

**Guard the expensive step, not the whole workflow.** Splitting one file into
`with-changelog` and `without-changelog` moves the decision to task creation —
the one moment the facts that decide it do not exist yet. An `if:` on the step
keeps one workflow and lets the run decide.

**Probe, then decide, in two steps.** `allow_failure` on a cheap command that
answers a question, then a `condition` or an `if:` that reads its exit code.

**Converge with a loop, not with retries.** `max_retries` re-runs the same step;
converging needs probe → decide → repair, which is a loop body.

**Ask agents to commit incrementally.** It is what makes crash recovery cheap,
and it costs one sentence in the prompt.

**Set `max_retries: 0` on anything with a side effect.** A push, a release, a
comment on an issue: retrying it does something twice.

**Use `restricted` for steps that only read and write text.** A docs pass has no
business running commands; the mode says so and the adapter enforces it.

## 13. Reviewing a workflow before you commit it

- [ ] `vincent workflow validate <file>` passes with **no warnings**.
- [ ] `vincent workflow render <file>` renders every step. Validation only
      checks that a template parses; this runs it, which is where a typo'd
      `{{.Task.Titel}}` or an unsupplied `.Task.Fields` key surfaces.
- [ ] Every step that produces something checkable has a `check:`.
- [ ] Prompts say what "done" means, not just what to attempt.
- [ ] Any step that pushes, publishes or deletes sits **after** a `manual` gate
      and carries `max_retries: 0`.
- [ ] Optional `.Task.Fields` reads are wrapped in `{{ with }}`.
- [ ] Command steps work in the shell your teammates have — or the file declares
      `platforms:`.
- [ ] Loops have a `break` or a `count:` you would be happy to pay for.
- [ ] Step ids read like nouns you would want to see in a timeline.
- [ ] The file name matches the `name:` field.

Wiring it into CI is two lines, and neither needs a daemon:

```sh
for f in .vincent/workflows/*.yaml; do vincent workflow validate "$f" || exit 1; done
for f in .vincent/workflows/*.yaml; do vincent workflow render   "$f" || exit 1; done
```

## 14. Troubleshooting

| Symptom | Cause |
|---|---|
| `unknown field "…"` at load | A typo, or a field on the wrong step type. Unknown keys are never ignored |
| `template does not parse` | A syntax error in `prompt`/`run`/`check`/`instructions`/`if`. Caught at load |
| `template_error` at run time | A reference to something that is not there — usually an optional `.Task.Fields` key read without `{{ with }}`, or a `.Steps` id that has not completed |
| `condition_error` | A guard rendered something other than `true`/`false`. Not retried — fix the file and retry |
| `shell must be one of sh, pwsh, cmd` | `shell: bash` — see [§10.1](#101-which-shell-runs-a-command-step) |
| `count N exceeds the M-iteration ceiling` | Raise `max_iterations:` on the step, or `loop.max_iterations` in config |
| `break steps are only valid inside a loop body` | A `break` at the top level or in a group |
| `agent "codex" can never take mid-run input` | `on_input: require` resolved to an adapter with no control channel |
| A model name is an error, not a warning | It belongs to *another* adapter's catalog. A model in no catalog only warns |
| Workflow listed but not offered | `platforms:` does not admit this host — [§10.3](#103-platforms--declare-what-you-did-not-port) |
| The workflow ran an old version | A task uses the snapshot captured at creation. Use `edit + retry`, or create a new task |
| A group hangs long after one sub-step failed | By design: the group waits for everything it started, then reports the first failure in declaration order |

Anything not on this list is probably a task-level rather than a
workflow-level problem: see
[Troubleshooting](troubleshooting.md) and
[Task lifecycle](../reference/task-lifecycle.md).

---

## See also

- [Workflow schema](../reference/workflow-schema.md) — every field and default,
  in tables.
- [Example workflows](../../examples) — five working files.
- [Agent CLIs](agents.md) — what each adapter can and cannot do.
- [Task lifecycle](../reference/task-lifecycle.md) — what `blocked` means and
  what you can do from there.
- [Configuration](../reference/configuration.md) — the daemon-side defaults
  every workflow inherits.
- [Security model](../security-model.md) — what `full-auto` really allows.

{% endraw %}
