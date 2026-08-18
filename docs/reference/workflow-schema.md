# Workflow schema

The complete YAML field reference. The practical, prose version is
[Writing workflows](../guides/workflows.md); the normative one is
[spec §8](../spec.md).

**Unknown keys are errors, not ignored.** A typo fails validation instead of
becoming a setting that silently never applied.

- [File structure](#file-structure)
- [Top level](#top-level)
- [`platforms`](#platforms)
- [`defaults`](#defaults)
- [Step fields](#step-fields)
- [Template context](#template-context)
- [Environment](#environment)
- [Resolution order](#resolution-order)
- [Validation rules](#validation-rules)

---

## File structure

```yaml
name: feature-pr
description: One line, shown in the picker.

defaults:
  agent: claude
  max_retries: 2
  timeout: 45m

steps:
  - id: implement
    type: agent
    prompt: |
      Implement {{.Task.Title}}.
    check: go build ./... && go test ./...
```

Files live in `.vincent/workflows/*.yaml` (project scope), or
`{config_dir}/workflows/*.yaml` (global). Project shadows global by `name`. One
built-in workflow, `adhoc`, is always present: a single agent step.

## Top level

| Key | Type | Required | Notes |
|---|---|---|---|
| `name` | string | ✅ | How tasks refer to it. Unique per scope |
| `description` | string | | Shown in the TUI's workflow picker |
| `platforms` | list | | Where this workflow may run. Empty means anywhere — see [`platforms`](#platforms) |
| `defaults` | map | | Inherited by every step |
| `steps` | list | ✅ | Runs in order, top to bottom. Must be non-empty |

## `platforms`

vincent never translates a command step between shells, so a workflow that
pipes `cat` into `wc` is a POSIX workflow. Declaring that keeps it from being
offered on a host it cannot run on:

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

Any combination is allowed: `[linux, darwin]` is `[posix]` spelled out,
`[posix, windows]` is the same as omitting the key. Tokens are matched exactly
— `macos` and `Linux` are validation errors, not silent non-matches.

On a host the list does not admit:

- the workflow is still **listed** — by `vincent workflow ls` (status
  `unsupported`) and in the TUI's workflow view, with the platforms it needs;
- it cannot be **selected**: the new-task picker refuses it and
  `POST /v1/tasks` rejects it with a 400;
- a task that somehow already carries it — a data directory moved between
  machines — blocks at admission with `platform_unsupported`, before any step
  runs.

`vincent workflow validate` checks the tokens but never the host, so a
POSIX-only workflow validates the same on a Windows CI runner as it does on
Linux.

The restriction covers the whole workflow; there is no per-step `platforms`.

## `defaults`

Every key here is also settable per step, where it wins.

| Key | Type | Default | Applies to |
|---|---|---|---|
| `agent` | `claude` \| `codex` \| `cursor` | daemon default | agent steps |
| `model` | string | adapter default | agent steps |
| `effort` | string | adapter default | agent steps (**ignored by cursor**) |
| `permission_mode` | `full-auto` \| `restricted` | `full-auto` | agent steps |
| `on_input` | `wait` \| `deny` \| `require` | `wait` | agent steps; `require` also gates which agents may run the step |
| `input_timeout` | duration | `24h` (config) | agent steps |
| `max_retries` | int | `1` | all steps |
| `timeout` | duration | `60m` agent / `15m` command (config) | all steps |

Durations are Go duration strings: `45m`, `1h30m`, `90s`.

`max_retries` counts attempts **after** the first, so the default of 1 means up
to two attempts.

## Step fields

Common to every step:

| Key | Type | Required | Notes |
|---|---|---|---|
| `id` | slug | ✅ | Unique within the file, sub-steps included. How `.Steps` addresses it |
| `name` | string | | Display name; defaults to `id` |
| `type` | `agent` \| `command` \| `manual` \| `parallel` \| `fan_out` | ✅ | `check` is a *field*, not a type |
| `max_retries` | int | | Overrides `defaults` |
| `timeout` | duration | | Per attempt; overrides `defaults`. On a `parallel` group, bounds the whole group |

### `type: agent`

Runs an agent CLI headlessly in the worktree.

| Key | Type | Required | Notes |
|---|---|---|---|
| `prompt` | string | ✅ | Go `text/template` |
| `agent` | string | | `claude`, `codex` or `cursor` |
| `model` | string | | Adapter-native id or alias |
| `effort` | string | | Adapter-native. Cursor has none — it lives in the model id |
| `permission_mode` | string | | `full-auto` (default) or `restricted` |
| `on_input` | string | | `wait` (default), `deny`, or `require`. `wait`/`deny` have no effect on codex or cursor; `require` refuses them |
| `input_timeout` | duration | | Bounds each wait in `awaiting_input`, per request |
| `check` | string | | Shell command that must exit 0 for the attempt to succeed |
| `check_timeout` | duration | | Defaults to the command timeout |

```yaml
  - id: implement
    type: agent
    agent: claude
    model: sonnet
    effort: high
    permission_mode: full-auto
    prompt: |
      Implement {{.Task.Title}}.
    check: go build ./... && go test ./...
    check_timeout: 15m
```

Succeeds when the process exits 0, **and** the event stream produced a terminal
result (not an error), **and** any declared `check` exits 0.

Every agent step is a **fresh session** — no conversation is resumed between
steps or attempts. State flows through the worktree and through `{{.Steps}}`.

### `type: command`

Runs a shell command in the worktree.

| Key | Type | Required | Notes |
|---|---|---|---|
| `run` | string | ✅ | Go `text/template`, then handed to a shell |
| `shell` | `sh` \| `pwsh` \| `cmd` | | Pin the shell; default is the platform's |
| `env` | map | | Extra environment for this step |
| `check` | string | | Must exit 0 for the attempt to succeed |
| `check_timeout` | duration | | Defaults to the command timeout |

```yaml
  - id: commit
    type: command
    run: 'git add -A && git commit -m "{{.Task.Title}}"'
    shell: bash
    env:
      CI: "true"
```

Default shells: `/bin/sh -c` on POSIX, `pwsh -NoProfile -Command` on Windows
(falling back to `powershell`). vincent does not translate between them —
portability is the author's job.

### `type: manual`

Stops and waits for a person.

| Key | Type | Required |
|---|---|---|
| `instructions` | string | ✅ |

```yaml
  - id: review
    type: manual
    instructions: |
      Read the diff for #{{.Task.ID}} before it ships.
```

The task enters `awaiting_gate` and **releases its concurrency slot**. Approving
advances; rejecting moves the task to `blocked`.

### `type: parallel`

Runs its sub-steps at the same time, in the task's one worktree.

| Key | Type | Required |
|---|---|---|
| `steps` | list of steps | ✅ |
| `max_parallel` | int | — (default `parallel.max_parallel`, 4) |

```yaml
  - id: verify
    type: parallel
    max_parallel: 4
    timeout: 30m          # bounds the whole group
    steps:
      - { id: test,      type: command, run: go test ./... }
      - { id: lint,      type: command, run: golangci-lint run }
      - { id: typecheck, type: command, run: go vet ./... }
```

Sub-steps are ordinary `agent` and `command` steps: each has its own `check`,
`timeout`, `max_retries` and agent selection, resolved exactly as at the top
level. What they may **not** be:

- `manual` — a gate releases the task's slot, and there is no such thing as
  half a task waiting at a gate;
- another `parallel` — groups do not nest;
- `on_input: require` — the task has one pending question at a time, so a
  group of agents that each want to ask has nowhere to put the answers.

The group is one step: one index, one slot, one entry in the timeline. It
succeeds when every sub-step succeeds. A failure does **not** cancel the
others — the group waits for everything it started, then blocks with the first
failure in declaration order. A retry re-runs only what failed, including
after a human retry: sub-steps that already succeeded are not run again.

> **`max_parallel` is not governed by your concurrency caps.** Those count
> *tasks* (`max_parallel_tasks`, §11); a group runs inside one of them. One
> task at `max_parallel: 8` will happily start eight compilers while the board
> shows a single running task. Size it for the machine, not for the board.

Sub-steps share a working tree. Two of them writing the same file is a bug in
your workflow — vincent isolates worktrees between *tasks*, not processes
inside one.

### `type: fan_out`

Turns each lane into a **real child task** with its own worktree and branch,
then merges those branches back into this task's own.

| Key | Type | Required |
|---|---|---|
| `lanes` | list of lanes | ✅ |
| `merge` | map | — |

```yaml
  - id: build
    type: fan_out
    merge:
      on_conflict: block          # block (default) | agent
    lanes:
      - id: api
        workflow: implement-module   # a registry workflow
        fields: { module: api }
      - id: docs
        steps:                        # …or inline steps
          - { id: write, type: agent, prompt: "Document the API." }
```

A **lane** carries `id` and exactly one of `workflow` or `steps`, plus:

| Key | Type | Notes |
|---|---|---|
| `fields` | map | Merged over the parent task's fields; the lane wins |
| `agent` / `model` / `effort` | string | Override the inherited selection for this lane's whole subtree |
| `priority` | int | Same, for scheduler priority |

A lane's workflow may itself contain a `fan_out`, to any depth. The bounds are
`fan_out.max_depth` (3) and `fan_out.max_tasks` (64), both checked when the
task is created — a cycle or an oversized tree is a `400` naming what is
wrong, not something you discover as two hundred worktrees.

**What a lane inherits.** Its base branch is the parent's branch, which is how
the work lands where it belongs. Priority and the agent overrides propagate
too — a fan-out inside an urgent task would otherwise queue behind unrelated
work and make the urgent task *slower* than not fanning out.

**One branch is still delivered.** The step does not finish until every lane is
merged, `--no-ff`, in the order the lanes are declared.

#### `merge` and conflicts

| Key | Type | Notes |
|---|---|---|
| `on_conflict` | `block` \| `agent` | Default `block` |
| `agent` | agent step | Required by, and only valid with, `on_conflict: agent` |

`block` stops the task with `merge_conflict` and leaves the worktree
conflicted, so you resolve it in place, stage the files, and retry — the join
commits your resolution and merges what is left.

`agent` tries an agent first. It is an ordinary agent step, `check` and all,
and the conflicted files are in its template context as `{{.Conflicts}}`. If
it fails, or its check fails, or conflict markers survive it, you get the same
block.

```yaml
    merge:
      on_conflict: agent
      agent:
        id: resolve
        prompt: |
          Resolve the merge conflict in: {{ range .Conflicts }}{{.}} {{ end }}
        check: go build ./... && go test ./...
```

> **A fan-out fills your caps, it does not exceed them.** Each lane is a task
> and competes for `max_parallel_tasks` like any other. What it buys is
> parallelism you would otherwise have to start by hand.

> **N lanes leave N worktrees** on disk until someone archives the tree. That
> is what `vincent gc` and `vincent doctor` are for, and you will meet it
> before you meet anything else in this feature.

### `check` is a field, not a step type

It runs after the step body, in the worktree, with the same environment as a
command step, and its output is captured to the transcript. Non-zero fails the
attempt, and the retry gets the failure appended to its prompt automatically:

```
<previous-attempt-failure attempt="1">
reason: check command failed (exit 1)
--- output (last 200 lines) ---
…
</previous-attempt-failure>
```

Invert it when failure is the deliverable: `check: '! go test ./...'`.

## Template context

`prompt`, `run`, `check` and `instructions` are Go `text/template`, rendered
with `missingkey=error` — a bad reference fails the step *before* any process
starts.

| Variable | Fields |
|---|---|
| `.Task` | `ID`, `Title`, `Description`, `Fields` (map), `BaseBranch`, `BranchName` |
| `.Project` | `Name`, `Path` (the original repo root), `DefaultBranch` |
| `.Workflow` | `Name`, `Description` |
| `.Step` | `ID`, `Name`, `Index`, `Attempt` (1-based) |
| `.Steps` | **completed** steps by id → `{Status, Result, ExitCode}` |
| `.Worktree` | `Path` |
| `.LastFailure` | retries only: `{Reason, Output}`; empty otherwise |

`.Steps.<id>.Result` is the agent's final result text for agent steps, or the
last 100 lines of stdout for command steps. That is how one step's output feeds
the next:

```yaml
  - id: fix
    type: agent
    prompt: |
      A previous step reported:

      {{.Steps.survey.Result}}

      Fix exactly those items.
```

`.Task.Fields` is free-form per task, so **optional** fields must be read
defensively or rendering fails:

```yaml
{{with index .Task.Fields "ticket"}}Ticket: {{.}}{{end}}
```

## Environment

Command and check steps run with cwd set to the worktree, inherit the daemon's
environment, and additionally receive:

```
VINCENT_TASK_ID        VINCENT_TASK_TITLE     VINCENT_PROJECT_NAME
VINCENT_PROJECT_PATH   VINCENT_WORKTREE       VINCENT_BRANCH
VINCENT_BASE_BRANCH    VINCENT_STEP_ID        VINCENT_STEP_ATTEMPT
VINCENT_WORKFLOW
```

`env:` on a step is merged on top of these.

## Resolution order

For each agent step, `agent`, `model` and `effort` resolve first-hit-wins:

1. the explicit **step** field
2. the **task**-level override chosen at creation (`--agent`, `--model`,
   `--effort`) — replaces workflow `defaults`, never an explicit step field
3. workflow **`defaults`**
4. the **adapter** default (usually empty: the CLI decides)

**Agent-scoped inheritance:** `model` and `effort` only inherit from a level
whose resolved agent matches the step's. Switching agent without setting them
resets them to the new adapter's default rather than leaking a claude alias onto
a codex step.

`POST /v1/resolve` answers "what does this resolve to, and which level won" for
every step — that is what the TUI's new-task form renders, and no client
re-derives it.

## Validation rules

`vincent workflow validate <file>` checks all of this locally — no daemon, no
network, no agent CLI installed.

- `steps` non-empty; step `id`s unique **across the whole file**, sub-steps
  of a `parallel` group included; `type` known.
- A `parallel` group has at least one sub-step and a `max_parallel` of at
  least 1; its sub-steps are not `manual`, not `parallel`, and do not
  resolve to `on_input: require`.
- A `fan_out` step has at least one lane; lane ids are unique within the step;
  each lane has exactly one of `workflow` and `steps`. `merge.agent` is
  required by, and only valid with, `on_conflict: agent`. A lane's inline
  steps have their own id namespace, because each lane becomes its own task.
- Every template parses.
- `platforms` entries are known tokens, with no duplicates. The list is checked
  for shape, never against the validating host.
- Durations parse as Go durations; `on_input` is `wait`, `deny` or `require`.
- Unknown keys are errors.
- `agent` names a known adapter.
- A step with `on_input: require` does not resolve to an adapter that can
  never take mid-run input (codex, cursor). The error points at the `agent`
  field that supplied the value — the step's own, or `defaults.agent`.
- The resolved `(agent, model, effort)` triple is checked **cross-catalog**:
  - a value in the resolved adapter's own catalog → valid;
  - a value found only in **another** adapter's catalog → **error** (claude's
    `sonnet` or `max` reaching a codex step);
  - a value in no catalog at all → **warning**, and it passes. The CLI is the
    final authority at run time, so free-text models and future CLI values are
    not blocked.

Validation never probes an agent CLI. It consults the live option cache when the
daemon has primed it and the curated catalogs otherwise — probing only ever adds
values, so a verdict can soften but never harden.

Warnings surface structurally: on registry entries, on the validate response, on
the task-creation response, and in the daemon log.

---

## See also

- [Writing workflows](../guides/workflows.md) — the guide.
- [Example workflows](../../examples) — four working files.
- [Agent CLIs](../guides/agents.md) — what each adapter honors.
