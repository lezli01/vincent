{% raw %}

# Workflow schema

The complete YAML field reference. For a practical, pattern-oriented tour, see
[Writing workflows](../guides/workflows.md).

**Unknown keys are errors, not ignored.** A typo fails validation instead of
becoming a setting that silently never applied.

**The file**

- [File structure](#file-structure)
- [Top level](#top-level)
  - [`platforms`](#platforms)
  - [`fields`](#fields)
  - [`defaults`](#defaults)

**Steps**

- [Step fields](#step-fields) — common to every type
  - [`type: agent`](#type-agent)
  - [`type: command`](#type-command)
  - [`type: manual`](#type-manual)
  - [`type: parallel`](#type-parallel)
  - [`type: fan_out`](#type-fan_out) · [`merge` and conflicts](#merge-and-conflicts)
  - [`type: loop`](#type-loop) · [`.Loop`](#loop) · [body contents](#what-a-body-may-contain) · [ending](#ending-a-loop) · [human actions](#human-actions)
  - [`type: break`](#type-break)
  - [`type: include`](#type-include) — reuse another workflow
  - [Nesting rules](#nesting-rules) — which type may appear where

**Behaviour**

- [Conditions](#conditions)
  - [`if:` — skip a step](#if--skip-a-step-carry-on)
  - [`type: condition` — finish early](#type-condition--finish-early)
  - [`allow_failure:` — a failure that is data](#allow_failure--a-failure-that-is-data)
  - [Reading an earlier step](#reading-the-outcome-of-an-earlier-step)
  - [`check` is a field](#check-is-a-field-not-a-step-type)
- [Template context](#template-context)
- [Environment](#environment)
- [Resolution order](#resolution-order)
- [Validation rules](#validation-rules)

---

## File structure

```yaml
name: feature-pr
description: One line, shown in the picker.

fields:
  - name: ticket
    label: Ticket
    required: true
    pattern: '^OPS-[0-9]+$'

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
`{config_dir}/workflows/*.yaml` (global). Project shadows global by `name`.
Three built-in workflows are always present: `adhoc`, a single agent step;
`create-workflow`, a single agent step that writes another workflow file into
one of the two registries; and
[`update-workflows`](../guides/workflows.md#12-where-workflow-files-live),
which rewrites the workflows a project already versions against everything on
this page and validates each one.

## Top level

| Key | Type | Required | Notes |
|---|---|---|---|
| `name` | string | ✅ | How tasks refer to it. Unique per scope |
| `description` | string | | Shown in the TUI's workflow picker |
| `platforms` | list | | Where this workflow may run. Empty means anywhere — see [`platforms`](#platforms) |
| `fields` | list | | Ordered task-input declarations — see [`fields`](#fields) |
| `defaults` | map | | Inherited by every step |
| `steps` | list | ✅ | Runs in order, top to bottom. Must be non-empty |

## `platforms`

Vincent never translates a command step between shells, so a workflow that
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

## `fields`

A workflow can declare the task fields it expects. The TUI pre-renders these
rows as soon as the workflow is selected, while the daemon validates them for
every client when the task is created.

```yaml
fields:
  - name: ticket
    label: Ticket
    description: Issue tracker key, including its project prefix.
    type: string
    required: true
    pattern: '^OPS-[0-9]+$'
  - name: retries
    type: integer
  - name: threshold
    type: number
  - name: dry-run
    label: Dry run
    type: boolean
```

Definitions stay in source order. Each definition has:

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `name` | slug | ✅ | | Key in `.Task.Fields`; unique in this list |
| `label` | string | | `name` | Presentation text only |
| `description` | string | | | Help text shown by clients |
| `type` | `string` \| `integer` \| `number` \| `boolean` | | `string` | Editing and validation contract; stored value is still a string |
| `required` | bool | | `false` | Missing or whitespace-only values are rejected when true |
| `pattern` | string | | | Go RE2 expression, valid only for `string`; use `^` and `$` for a whole-value match |

Integers are base-10 whole numbers, numbers must be finite decimals, and
booleans are exactly `true` or `false`. Optional absent or empty values skip
type and pattern validation.

The task map remains **open**. A caller may send additional names; vincent
records them and exposes them to templates just like declared fields. Only the
declared names receive required, type, and pattern checks.

The selected workflow is the public boundary. Fields declared by an included
workflow or a named `fan_out` lane are not merged into the caller's form. A
composing workflow re-declares the inputs it exposes; lane `fields:` remains a
separate map of values bound for that lane.

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
| `retry_backoff` | duration | `0s` | all steps |
| `timeout` | duration | `60m` agent / `15m` command (config) | all steps |

Durations are Go duration strings: `45m`, `1h30m`, `90s`.

`max_retries` counts attempts **after** the first, so the default of 1 means up
to two attempts.

`retry_backoff` is how long to wait before each of them. The default of `0s` is
an immediate retry — which is right for a compile error the agent can see and
fix, and wrong for a flaky network call or a `git index.lock` held by another
process, where two guaranteed failures inside a few seconds spend the budget on
nothing. A non-zero value does **not** sleep: the task returns to `queued` with
a wait attached and gives up its slot, so a paced task never stops other work
from running. See [States](task-lifecycle.md#states) in the lifecycle reference for
what that looks like from outside. Neither field has a
`config.yaml` key: retry policy belongs to a workflow.

## Step fields

Common to every step:

| Key | Type | Required | Notes |
|---|---|---|---|
| `id` | slug | ✅ | Unique within the file, sub-steps included. How `.Steps` addresses it |
| `name` | string | | Display name; defaults to `id` |
| `type` | `agent` \| `command` \| `manual` \| `parallel` \| `fan_out` \| `condition` \| `loop` \| `break` \| `include` | ✅ | `check` is a *field*, not a type |
| `max_retries` | int | | Overrides `defaults` |
| `retry_backoff` | duration | | Wait before each retry; overrides `defaults`. `0s` means retry at once, which is the default. Not valid on `condition`, `break`, `loop` or `include` steps, which own no attempt |
| `timeout` | duration | | Per attempt; overrides `defaults`. On a `parallel` group or a `loop`, bounds the whole thing |
| `if` | template | | Guard: skip this step unless it renders `true`. See [Conditions](#conditions) |

`allow_failure: true` is available on `agent` and `command` steps only; it is
covered under [Conditions](#conditions), because it exists to give a guard
something to read.

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
| `check_timeout` | duration | | Defaults to the daemon's `defaults.command_timeout` (15m) — **not** the step's own `timeout`, and not workflow `defaults.timeout` |

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
| `check_timeout` | duration | | Defaults to the daemon's `defaults.command_timeout` (15m) |

```yaml
  - id: commit
    type: command
    run: 'git add -A && git commit -m "{{.Task.Title}}"'
    shell: sh
    env:
      CI: "true"
```

Default shells: `/bin/sh -c` on POSIX, `pwsh -NoProfile -Command` on Windows
(falling back to `powershell`). Vincent does not translate between them —
portability is the author's job.

`shell:` accepts exactly three values — **`bash` is not one of them**. Use `sh`
for portable POSIX syntax, or invoke bash explicitly with `run: bash -c '…'`.

| Value | Runs as |
|---|---|
| `sh` | `/bin/sh -c`; on Windows, whatever `sh` resolves to on `PATH` (Git Bash's, if it is there) |
| `pwsh` | `pwsh -NoProfile -Command` |
| `cmd` | `cmd /C` |

A pinned shell is never silently replaced: if it is not installed the step
fails with `shell_unavailable`.

Quote a `run:` containing a colon — `run: git commit -m "fix: thing"` is a YAML
parse error, since the parser reads `fix:` as a second mapping key.

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
`timeout`, `max_retries`, `retry_backoff`, `if:` and agent selection, resolved
exactly as at the top level. A sub-step that backs off takes the whole group
with it — the group waits for its in-flight siblings, then the task waits, and
the re-admitted group re-runs only what is left. Those two types are the whole list — see
[Nesting rules](#nesting-rules) for what is refused and why:

- `manual` — a gate releases the task's slot, and there is no such thing as
  half a task waiting at a gate;
- another `parallel`, and `loop` — both derive their position from rows sharing
  one `step_index`, and that derivation stays affordable one level deep;
- `fan_out` — the task parks in `awaiting_children`, and a group cannot park
  half a task;
- `condition` — a group is a set with no later steps to stop; guard the
  sub-step with `if:` instead;
- `break` — there is no loop here for one to end;
- `on_input: require` — the task has one pending question at a time, so a
  group of agents that each want to ask has nowhere to put the answers. This is
  judged on the **resolved** value, so a `defaults.on_input: require` reaching a
  silent sub-step is caught too.

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
| `if` | template | Guard. False means this lane is not spawned; siblings still run and the join still happens |
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
merged, `--no-ff`, in the order the lanes are declared. Order is the lane's
**declared** index, not its position among the lanes actually spawned, so a
guarded-off lane does not renumber the merge.

**Spawning parks the parent** in `awaiting_children` and releases its slot; the
scheduler re-admits it once every descendant has settled, and that second
admission runs the join. If the spawn is only partial, the lanes already created
are cancelled, so a retry starts from a clean slate rather than half a tree.

**When every lane is guarded off**, the step chooses nothing and advances. It
must not park: a parent in `awaiting_children` with no children would be
re-queued forever.

**The join gets one attempt** unless the step declares `max_retries` itself. The
two ways a join fails — a conflict, and a lane that did not finish — are both
"a human decides", and an automatic second merge would abort the first, hit the
same conflict and block anyway. The `on_conflict: agent` resolver below is
pinned to `retry_backoff: 0` for the same reason, so a workflow-wide
`defaults.retry_backoff` does not reach it: its attempts are the join's own, and
a resolver that does not resolve leaves the conflict for a human rather than
something a wait could fix.

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

A merge resolver is a full agent step and needs an `id`, since it gets step-run
rows of its own. It may **not** declare `on_input: require`: it runs mid-join,
and the worktree state it is resolving is not something a human can inspect
through a question.

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

### `type: loop`

Runs its body — a **sequence** — repeatedly, in the task's one worktree. No
branch, no child task, nothing to merge: that is `fan_out`. Where a `parallel`
group is a set run once, a loop is a sequence run more than once.

| Key | Type | Required |
|---|---|---|
| `steps` | list of steps | ✅ |
| `count` | int | exactly one of `count` / `for_each` |
| `for_each` | string, or list of strings | exactly one of `count` / `for_each` |
| `max_iterations` | int | — (default `loop.max_iterations`, 10) |

**Fix until green** — the archetype the feature exists for. Bounded by
construction, and post-test by construction, because the condition lives in the
body where it can see the body:

```yaml
  - id: green
    type: loop
    count: 5
    steps:
      - { id: suite, type: command, run: go test ./..., allow_failure: true, max_retries: 0 }
      - { id: passed, type: break, if: '{{ eq (index .Steps "suite").ExitCode 0 }}' }
      - id: repair
        type: agent
        prompt: |
          The suite is red:

          {{ (index .Steps "suite").Result }}

          Fix the underlying cause. Do not weaken, skip or delete a test.
```

**Once per item**, over a list a step discovered at run time:

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

`for_each:` takes a YAML sequence or a single scalar. Either way each entry is
rendered, trimmed and split on newlines with empty lines dropped, so
`[api, web]` and a command's multi-line output are the same mechanism.

**There is no `while:`.** A guard can read only what a run has already
produced, and on the first iteration the body has not run — so a `while:`
reading its own body is either an error or, worse, silently false, and one
reading a step *before* the loop reads a constant and spins to the ceiling.
`count:` plus `break` writes the same loop correctly.

#### `.Loop`

| Field | |
|---|---|
| `.Loop.Index` | the 1-based iteration, and **0** outside any loop, so a shared template can tell |
| `.Loop.Item` | the `for_each` item this pass runs on; empty for a `count:` loop |
| `.Loop.IsFirst` / `.Loop.IsLast` | |

#### What a body may contain

`agent`, `command`, `condition` and `break`. Rejected at load: `manual`,
`on_input: require`, `parallel`, `fan_out` and a nested `loop` — each for the
reason a `parallel` group rejects it. All of them park or end the task's
admission mid-body, and a loop's position is *derived* from its rows, which
have no way to say "iteration 3 of this loop is waiting on a human".

#### Ending a loop

- The driver runs out, or a `break` fires → the loop **succeeds** and the
  cursor advances.
- A `condition` inside the body is false → **that iteration** ends and the loop
  carries on. That is `continue`, and it needs no new step type: a loop body is
  a sequence, so "end the sequence" ends the pass.
- The loop cannot run within `max_iterations` → the task **blocks** with
  `loop_limit`. A `for_each` list longer than the ceiling blocks before the
  first iteration, naming the count.
- A body step exhausts its retries → the task blocks with **that step's** own
  reason. Use `allow_failure:` when a red probe is the point.

Retries and iterations are different things: `max_retries` is for a step that
*failed*, and an iteration is for a body that *succeeded and must run again*.
Each body step spends its own budget within each iteration.

#### Human actions

`skip` skips the **whole loop** and advances past it — there is no "skip this
iteration". `retry` resumes at the failed body step of the iteration it
stopped in, with a fresh budget; it does not restart at iteration 1. `edit +
retry` rewrites that body step in the task's snapshot, so it applies to
**every remaining iteration** — fix the prompt, let it keep going.

The same is true after a crash: position is derived from the rows on every
admission, so a restarted daemon resumes mid-iteration rather than redoing work
you may have waited an hour for.

> **A loop is one step: one index, one slot, one worktree, one timeline entry**,
> and its iterations are strictly sequential. Unlike `max_parallel`, it adds no
> concurrency your caps cannot see. What it does add is *spend*: ten iterations
> of a three-step body is thirty agent runs, which is why the default ceiling
> is 10.

### `type: break`

Ends the enclosing loop, successfully. Takes `id`, `name` and a required `if:`,
and nothing else — like `condition`, it starts no process, so it cannot time
out, be retried or write a transcript.

```yaml
      - { id: passed, type: break, if: '{{ eq (index .Steps "suite").ExitCode 0 }}' }
```

Only valid inside a loop body: elsewhere there is no loop for it to end.

### `type: include`

Runs another registry workflow's steps here, in this task and this worktree.
It is how a workflow becomes reusable.

| Field | | Required |
|---|---|:--:|
| `workflow` | the name of the workflow to include, resolved with the usual project > global > built-in shadowing | ✅ |

```yaml
# .vincent/workflows/go-checks.yaml
name: go-checks
defaults: { max_retries: 0 }
steps:
  - { id: lint, type: command, run: go run mage.go lint }
  - { id: test, type: command, run: go run mage.go test }
```

```yaml
# .vincent/workflows/feature.yaml
name: feature
steps:
  - { id: implement, type: agent, prompt: "{{ .Task.Description }}" }
  - { id: checks, type: include, workflow: go-checks }
  - { id: review, type: agent, prompt: "lint said: {{ .Steps.lint.Result }}" }
```

**The include disappears.** When the task is created, `checks` is replaced by
`lint` and `test`, and the task runs four steps. There is no include at run
time: no step of its own, no grouping, no boundary. That is why `review` can
read `.Steps.lint` — after the splice, those steps *are* `feature`'s steps.

It also means an include takes no other fields. `if`, `timeout`, `max_retries`,
`allow_failure` and `check` are all rejected, because there is no step for them
to bind to. To make an include conditional, guard the callee's own steps, or
put a [`type: condition`](#type-condition--finish-early) in front of it.

**Resolved once, when the task is created.** Editing `go-checks.yaml`
afterwards does not touch a task already running — the task's snapshot is its
execution truth. It also means everything that can go wrong is reported when
you create the task, not six hours in:

| | |
|---|---|
| The workflow is not found | `400` naming it |
| A cycle — `a` includes `b` includes `a` | `400` naming the path |
| More than `include.max_depth` levels (default 5) | `400` naming the bound |
| The callee brings a step id already in use | `400` naming both workflows |
| The callee's `platforms:` excludes this machine | `400` naming both |

**Step ids are shared.** The whole expansion is one namespace, so a callee
cannot bring an id the caller already uses — and a given workflow can be
included at most once per caller. Ids are never rewritten or prefixed: the
callee's own templates say `.Steps.<id>`, and renaming its steps would break
them.

**The callee's `defaults:` come with it.** `go-checks` above was written with
`max_retries: 0`, and its steps keep that after being spliced into a workflow
whose defaults say otherwise. The order is
[the usual one](#resolution-order) with the included workflow inserted below
the task: the step's own field, then the task's `--agent`/`--model`/`--effort`,
then the *included* workflow's defaults, then the including workflow's, then
the daemon's.

**A `condition` inside a callee ends the whole run.** There is no include
boundary for it to end instead, so a fragment that finishes early finishes the
caller too. And a fragment containing a `break` cannot be factored out at all,
because `break` is only valid inside a loop body and so a workflow whose top
level has one does not load.

In the TUI a workflow's graph shows an include as a single collapsed node
labelled with the workflow it pulls in; a task's detail view badges each
spliced step with `from <workflow>`.

### Nesting rules

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

`include` is ✅ everywhere because it is not there when the workflow runs — what
matters is what it *expands to*, and that is checked against this same table
once the steps are in place. A fragment containing a `loop`, included into a
loop body, is refused when the task is created with the message a hand-written
nested loop would get.

A lane's inline steps become a child task's own flat snapshot, so they are a
workflow body in their own right: the "top level" column is the one that applies
to them, and their step ids live in their own namespace.

Every ❌ has one cause. A `parallel` group and a `loop` iteration run inside a
**single admission of a single task**, and each rejected type needs a task state
saying "one member of this structure is parked" — which §6 has no room for.
`on_input: require` is refused inside both for the same reason: `awaiting_input`
holds one pending request for the whole task. Refusal is judged on the
**resolved** value, so `defaults.on_input: require` reaching a silent member is
caught at load.

## Conditions

A workflow can decide at run time what to do next. Three fields do it.

### `if:` — skip a step, carry on

Any step may carry a guard. It renders like every other template here and must
produce, after trimming, exactly `true` or `false` — nothing else counts, and
`yes`, `1` and an empty string are all errors rather than a guess.

```yaml
  - id: changelog
    type: agent
    if: '{{ eq (index .Task.Fields "changelog") "yes" }}'
    prompt: Update CHANGELOG.md.
```

A false guard **skips that step and the workflow continues**. The step still
appears in the task's step list, in state `skipped` with reason `condition` —
so you can tell it apart from a step you skipped by hand — and downstream
templates can see it in `.Steps`.

On a **fan-out lane** and on a **`parallel` sub-step**, the same `if:` means
"do not start this one": the other lanes and sub-steps still run, and the join
still happens. A set has no "later" to skip to, so a false guard subsets it.

Guards are re-evaluated every single time — on each attempt, on a retry, and
after a daemon restart. Nothing is cached. If you fix a workflow and retry a
blocked step whose guard is now false, the step is skipped, which is the point.

### `type: condition` — finish early

A step whose entire body is the guard. True continues; **false ends the run and
the task is `done`**. It takes `id`, `name` and `if:` and nothing else — no
`run`, no `timeout`, no `max_retries` — because it starts no process.

```yaml
  - id: nothing-to-do
    type: condition
    if: '{{ ne (index .Steps "probe").ExitCode 0 }}'
```

The steps after it are never considered and record nothing; the one row it
leaves is in state `stopped`, which is where the detail view shows the run
ended.

There is no "stop and block for a human" option, because you already have one:
a `command` step that exits nonzero. What this adds is *stop and succeed*.

A `condition` step is valid at the top level and inside a lane's workflow. It
is rejected inside a `parallel` group — a group is a set, so there is no
sequence there to end.

### `allow_failure:` — a failure that is data

Without this, a guard can only read what a human typed when creating the task.
A command step that exits nonzero **blocks the task**, so no step that
succeeded ever has a nonzero exit code to branch on.

`allow_failure: true` changes that for one step: once its retry budget is
spent, the workflow advances instead of blocking. The row keeps its `failed`
state and its reason — the failure happened — and that row is what the next
guard reads.

```yaml
  - id: probe
    type: command
    run: git diff --quiet HEAD~1
    allow_failure: true
    max_retries: 0                # a probe should not retry
  - id: stop-if-clean
    type: condition
    if: '{{ ne (index .Steps "probe").ExitCode 0 }}'
```

It only swallows failures **the step itself produced** — a nonzero exit, a
failed `check`, an agent error, a timeout, a transcript-cap kill. It never
swallows vincent failing to *run* the step: a missing CLI, an expired login, a
template error. Branching on "the agent is not installed" as though it were a
test result is not a thing a workflow should be able to do.

It is deliberately not available in `defaults:`. A file-wide "failures do not
block" is a footgun, and it should cost you one line per step that wants it.

### Reading the outcome of an earlier step

Guards mostly read `.Steps`, which after this change carries a little more:

| What | Reads as |
|---|---|
| A step that succeeded | `{{ eq (index .Steps "x").Status "succeeded" }}` |
| A step skipped by its guard | `{{ eq (index .Steps "x").Status "skipped" }}` |
| A step that failed under `allow_failure` | `{{ ne (index .Steps "x").ExitCode 0 }}` |
| A body step of the loop pass you are in | `{{ (index .Steps "probe").Result }}` — inside a loop, earlier body steps of the same iteration are visible; a repeated step id resolves to its **latest** iteration |
| The platform | `{{ ne .Host.OS "windows" }}` |
| A field typed at creation | `{{ eq (index .Task.Fields "ship") "yes" }}` |

A step's *own* failed attempt is not in `.Steps` while it is retrying — use
`.LastFailure` for that.

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

`prompt`, `run`, `check`, `instructions` and `if` are Go `text/template`, rendered
with `missingkey=error` — a bad reference fails the step *before* any process
starts.

| Variable | Fields |
|---|---|
| `.Task` | `ID`, `Title`, `Description`, `Fields` (map), `BaseBranch`, `BranchName` |
| `.Project` | `Name`, `Path` (the original repo root), `DefaultBranch` |
| `.Workflow` | `Name`, `Description` |
| `.Step` | `ID`, `Name`, `Index`, `Attempt` (1-based) |
| `.Loop` | `Index` (1-based iteration, **0** outside any loop), `Item`, `IsFirst`, `IsLast`. See [`type: loop`](#type-loop) |
| `.Issue` | the GitHub issue the task was created from: `Number` (**0** when there is none), `Repo` (`owner/name`), `Title`, `Body`, `URL`, `State`, `Labels` (a list), `Author`, `Assignee`, `Milestone`, `MilestoneNumber`. See [`.Issue`](#issue) |
| `.Steps` | **completed** steps by id → `{Status, Result, ExitCode}`. `Status` is `succeeded`, `approved` (a passed gate), `skipped` (a false guard) or `failed` — the last only once the workflow has moved past it, which happens only under `allow_failure`. `interrupted` never appears |
| `.Host` | `OS`, `Arch` — the machine the daemon runs on. This is the per-step platform gate: `{{ ne .Host.OS "windows" }}` |
| `.Worktree` | `Path` |
| `.LastFailure` | retries only: `{Reason, Output}`; empty otherwise |
| `.Conflicts` | the conflicted file paths, on an `on_conflict: agent` [merge resolver](#merge-and-conflicts) only. Empty everywhere else, so a prompt may read it defensively anywhere |

### `.Issue`

A task can be created from a GitHub issue — from the TUI's new-task form, or
with `vincent task add --github-issue N`. When it was, `.Issue` carries that
issue and a prompt can use it directly:

```yaml
  - id: fix
    type: agent
    prompt: |
      Fix GitHub issue #{{ .Issue.Number }} — {{ .Issue.Title }}

      {{ .Issue.Body }}

      Labels:{{ range .Issue.Labels }} {{ . }}{{ end }}
      Discussion: {{ .Issue.URL }}
```

`.Issue.Number` is **0** when no issue is linked, exactly the way `.Loop.Index`
is 0 outside a loop, so one workflow serves both kinds of task:

```yaml
    prompt: |
      {{ if .Issue.Number }}Fix issue #{{ .Issue.Number }}: {{ .Issue.Title }}
      {{ else }}{{ .Task.Description }}{{ end }}
```

`Labels` is a real list, so `range` works on it. Everything else is a string.

The issue is a **snapshot**, read once when the task was created. Nothing
re-reads it, so editing the issue on GitHub afterwards does not change what a
later step renders — and rendering never touches the network, which is why a
step cannot fail here because GitHub is down. A [fan-out](#type-fan_out) lane
inherits its parent's issue, the same way it inherits `.Task.Fields`.

Creating a task from an issue also prefills the task's title (`#N ` and the
issue title), its description (the issue body plus a `GitHub issue #N: <url>`
line) and any declared [`fields:`](#fields) named exactly `issue`, `labels`,
`assignee` or `milestone`. All of that is editable before the task is created;
`.Issue` is the untouched copy.

A declared `issue` field carries the issue **number** — bare, without the `#`
the title carries — and it is how a `command` step gets at it: step bodies see
§8.5's environment, not this context, so `{{ index .Task.Fields "issue" }}` is
the way into a `run:`. Declare it `integer` (or `number`, or `string`); a
`boolean` `issue` is left empty like any other type mismatch.

Visibility follows one rule — a step appears once the engine has advanced past
it — and three consequences follow from it:

- a step's **own** failed attempt is never in `.Steps["itself"]` mid-retry;
  `.LastFailure` is that channel;
- members of a `parallel` group are **invisible to each other**, since
  concurrent siblings have never been advanced past;
- inside a `loop` body, earlier steps of the current iteration **are** visible
  to later ones, and under repetition an id resolves to its **latest**
  iteration.

`.Steps.<id>.Result` is the agent's final result text for agent steps, or the
last **200** lines of stdout for command steps. That is how one step's output
feeds the next — and, with `for_each:`, how one step's output becomes a loop's
item list. A producer meant to feed a loop should filter at the source rather
than lean on that tail:

```yaml
  - id: fix
    type: agent
    prompt: |
      A previous step reported:

      {{.Steps.survey.Result}}

      Fix exactly those items.
```

`.Task.Fields` remains an open string map per task. A declared **optional**
field, or an undeclared field that may be absent, must be read defensively or
rendering fails:

```yaml
{{with index .Task.Fields "ticket"}}Ticket: {{.}}{{end}}
```

A declared required field may be read directly because task creation rejects a
missing value before a snapshot can run.

## Environment

Agent, command and check steps run with cwd set to the worktree, inherit the
daemon's environment, and additionally receive:

```
VINCENT_TASK_ID        VINCENT_TASK_TITLE     VINCENT_PROJECT_NAME
VINCENT_PROJECT_PATH   VINCENT_WORKTREE       VINCENT_BRANCH
VINCENT_BASE_BRANCH    VINCENT_STEP_ID        VINCENT_STEP_ATTEMPT
VINCENT_WORKFLOW
```

`env:` on a step is merged on top of these — it is a command-step field, so an
agent step has none. Nothing can remove a `VINCENT_*` variable: they are facts
about the run, not inherited state.

`VINCENT_TASK_ID` and `VINCENT_STEP_ID` are what
[`vincent status`](cli.md#vincent-status) uses to address the step it is being
run from, which is why every step type that runs a process gets them.

## Resolution order

For each agent step, `agent`, `model` and `effort` resolve first-hit-wins:

1. the explicit **step** field
2. the **task**-level override chosen at creation (`--agent`, `--model`,
   `--effort`) — replaces workflow `defaults`, never an explicit step field
3. the **included** workflow's `defaults`, for a step that came from a
   [`type: include`](#type-include) — innermost first when includes nest
4. the including workflow's **`defaults`**
5. the **adapter** default (usually empty: the CLI decides)

**Agent-scoped inheritance:** `model` and `effort` only inherit from a level
whose resolved agent matches the step's. Switching agent without setting them
resets them to the new adapter's default rather than leaking a claude alias onto
a codex step.

`POST /v1/resolve` answers "what does this resolve to, and which level won" for
every step — that is what the TUI's new-task form renders, and no client
re-derives it.

## Validation rules

`vincent workflow validate <file>` checks all of this locally — no daemon, no
network, no agent CLI installed. Two values it cannot ask a daemon for, and
substitutes deterministically so a file that validates on a laptop validates on
a bare CI runner:

- **agent catalogs** are the curated, compiled-in ones rather than a live
  `claude --help`. Probing only ever *adds* values, so a verdict can soften on a
  real daemon but never harden;
- **the loop ceiling** is the built-in default (10), not your `config.yaml`.

One thing it cannot check at all: whether a [`type: include`](#type-include)
resolves. Which file a name reaches depends on the project's registry, and a
project workflow may shadow the very name that was missing or that closed a
cycle — so includes are resolved and checked when a task is created, and a
`400` there is the report.

Exit status is 1 for an invalid file; `--json` emits the findings structurally.

- `steps` non-empty; step `id`s unique **across the whole file**, sub-steps
  of a `parallel` group included; `type` known.
- A `parallel` group has at least one sub-step and a `max_parallel` of at
  least 1; its sub-steps are not `manual`, not `parallel`, not `condition`,
  and do not resolve to `on_input: require`.
- A `condition` step has an `if:` and nothing else — `timeout`, `max_retries`
  and `allow_failure` included. `allow_failure` is valid on `agent` and
  `command` steps only. A `condition` step in **last** position is a *warning*:
  the task is done whether it continues or stops.
- A `loop` has at least one body step and **exactly one** driver: `count` (at
  least 1, and at most the effective ceiling — the step's own `max_iterations`,
  else `loop.max_iterations`) or `for_each`. Its body steps join the file-wide
  id namespace, and are not `manual`, `parallel`, `fan_out` or another `loop`,
  and do not resolve to `on_input: require`. A `loop` rejects `max_retries` and
  `allow_failure`: it has no attempt of its own. A `break` has an `if:` and
  nothing else, and is valid only inside a loop body. `count`, `for_each` and
  `max_iterations` are rejected on every other step type.
- A `fan_out` step has at least one lane; lane ids are unique within the step
  and are slugs; each lane has exactly one of `workflow` and `steps`.
  `merge.agent` is required by, and only valid with, `on_conflict: agent`; it is
  a full agent step, needs an `id`, and may not declare `on_input: require`. A
  lane's inline steps have their own id namespace, because each lane becomes its
  own task. `resolved_from` is written by task creation and is an error by hand.
- An `include` step has a `workflow` and nothing else. `workflow` is rejected on
  every other type, and `resolved_from` — written by task creation — is an error
  beside it. Whether the name resolves is *not* checked here: see the note
  above.
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
- [Example workflows](../../examples) — five working files.
- [Agent CLIs](../guides/agents.md) — what each adapter honors.

{% endraw %}
