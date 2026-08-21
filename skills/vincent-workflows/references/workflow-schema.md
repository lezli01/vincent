# Vincent workflow schema

Use this compact reference while authoring or reviewing. The repository's
`docs/reference/workflow-schema.md` remains authoritative if Vincent has evolved
since this skill was installed.

## Compatibility and authority

This file is a schema snapshot, not a promise that every released Vincent
binary supports every listed feature. Establish the target before writing:

1. Inside a Vincent source checkout, prefer its current
   `docs/reference/workflow-schema.md`.
2. Run `vincent version` and record the exact output when a binary is available.
3. Run that binary's `vincent workflow validate`; its result is the final local
   compatibility verdict.

If the validator rejects a referenced feature, report the mismatch and ask
whether to upgrade Vincent or author against the installed version. A generic
YAML parser cannot answer this question. When another host or version will run
the workflow, treat local validation as provisional until that target binary
also accepts the file.

## File and top-level fields

Project workflows normally live in `.vincent/workflows/*.yaml`.

```yaml
name: feature
description: Implement and verify a feature.
platforms: [posix]
fields:
  - name: ticket
    type: string
    required: true
defaults:
  max_retries: 0
steps:
  - id: test
    type: command
    run: go test ./...
```

| Key | Type | Required | Purpose |
|---|---|:---:|---|
| `name` | string | yes | Unique registry name |
| `description` | string | no | Picker and workflow description |
| `platforms` | list | no | Whole-workflow host restriction |
| `fields` | list | no | Ordered task-input declarations |
| `defaults` | map | no | Values inherited by steps |
| `steps` | nonempty list | yes | Ordered workflow body |

`platforms` accepts `linux`, `darwin`, `windows`, and `posix`; `posix` means
non-Windows. It restricts the whole workflow. For a per-step platform guard,
use `.Host.OS` in `if:`. Vincent never translates shell syntax.

Each field declaration supports:

| Key | Values | Notes |
|---|---|---|
| `name` | slug | Required and unique; key in `.Task.Fields` |
| `label` | string | Presentation only; defaults to `name` |
| `description` | string | Help shown by clients |
| `type` | `string`, `integer`, `number`, `boolean` | Defaults to `string`; stored value remains a string |
| `required` | boolean | Whitespace-only counts as absent |
| `pattern` | Go RE2 string | Only for strings; use `^...$` for whole values |

Undeclared fields remain allowed. Optional absent values skip type and pattern
validation. Read an optional value defensively:

```gotemplate
{{with index .Task.Fields "ticket"}}Ticket: {{.}}{{end}}
```

## Defaults and common fields

Workflow `defaults` may set `agent`, `model`, `effort`, `permission_mode`,
`on_input`, `input_timeout`, `max_retries`, and `timeout`. Step values win.
`max_retries` counts attempts after the first: `0` means one attempt and `1`
means at most two. Durations use Go syntax such as `90s`, `45m`, or `1h30m`.

Every runtime step has a unique `id` and a `type`; `name`, `max_retries`,
`timeout`, and `if` are common optional fields where the step type permits
them. `allow_failure` is limited to `agent` and `command`. Some structural
types deliberately reject timeout or retry fields; see the table below.

## Step selection and fields

| Type | Required body | Important fields | Role |
|---|---|---|---|
| `command` | `run` | `shell`, `env`, `check`, `check_timeout`, `allow_failure` | Deterministic shell work |
| `agent` | `prompt` | agent selection, input policy, `check`, `allow_failure` | Reasoning or synthesis |
| `manual` | `instructions` | none | Binary human approval or judgment |
| `condition` | `if` | nothing else | False finishes the run successfully |
| `parallel` | `steps` | `max_parallel`, group `timeout` | Same-worktree concurrent commands/agents |
| `fan_out` | `lanes` | `merge` | Child tasks, branches, worktrees, then merge |
| `loop` | `steps` and one driver | `count` or `for_each`, `max_iterations`, group `timeout` | Bounded sequential repetition |
| `break` | `if` | nothing else | End the enclosing loop successfully |
| `include` | `workflow` | nothing else | Splice another registry workflow at task creation |

### Command

`run`, `check`, and `env` execute in the worktree. Default shells are
`/bin/sh -c` on POSIX and PowerShell on Windows. `shell` accepts exactly `sh`,
`pwsh`, or `cmd`; `bash` is not a valid value, though `run` may invoke it
explicitly. Pinning an unavailable shell fails instead of falling back.

Quote a `run` scalar containing a colon:

```yaml
- id: commit
  type: command
  run: 'git commit -m "fix: validate input"'
  max_retries: 0
```

### Agent

An agent step supports `agent` (`claude`, `codex`, or `cursor`), `model`,
`effort`, `permission_mode` (`full-auto` or `restricted`), `on_input` (`wait`,
`deny`, or `require`), `input_timeout`, `check`, and `check_timeout`. Cursor
ignores `effort`. Every attempt is a fresh agent session.

Success requires a successful process and terminal event plus a successful
`check`, when present. A retry receives `.LastFailure` automatically, but does
not resume the previous conversation.

### Manual

`manual` takes only templated `instructions`. It enters `awaiting_gate` and
releases the task's concurrency slot. Approval continues; rejection blocks.
It is a binary gate: it cannot return free-form text, a selected option, or a
credential. Downstream steps can observe approval status but receive no new
human-supplied value.

Use the mechanisms according to when and how a value is needed:

| Need | Mechanism |
|---|---|
| Typed value or choice known at task creation | Declared task `field` |
| Binary review or authorization between steps | `manual` |
| Answer required by the same live agent session | `on_input: require` on a supported adapter |

Vincent has no generic between-step data-entry step. Do not model a runtime
choice as a manual gate and then assume `.Steps` contains the answer.

### Parallel

Parallel substeps may be only `agent` or `command`, including compatible steps
spliced by `include`. They share one worktree, so concurrent writes must be
disjoint. Siblings cannot read one another through `.Steps`. A group waits for
everything it starts; retries rerun only failed substeps. Size `max_parallel`
for the machine because it counts processes inside one task, not task slots.

### Fan-out

Each lane has an `id`, exactly one of `workflow` or inline `steps`, and may have
`if`, `fields`, `agent`, `model`, `effort`, or `priority`. Each spawned lane is
a real child task with an isolated worktree and branch. The parent parks in
`awaiting_children`, releases its slot, and merges successful lane branches in
declared order.

```yaml
- id: modules
  type: fan_out
  lanes:
    - id: api
      workflow: implement-module
      fields: { module: api }
    - id: docs
      steps:
        - id: write-docs
          type: agent
          prompt: Document the implemented API.
  merge:
    on_conflict: block
```

`merge.on_conflict` is `block` by default or `agent`. Agent resolution requires
`merge.agent`, a complete agent step with its own `id`; it may use
`.Conflicts`, must leave no markers, and cannot require mid-run input.

### Loop and break

A loop has a nonempty `steps` sequence and exactly one of:

- `count`: positive fixed iteration count; or
- `for_each`: a YAML list or templated scalar split into trimmed nonempty lines.

Set `max_iterations` explicitly when the chosen bound differs from the default
10. Bodies may contain `agent`, `command`, `condition`, `break`, and compatible
included steps. They may not contain `manual`, `parallel`, `fan_out`, another
`loop`, or a step resolving to `on_input: require`.

`break` requires `if` and is valid only in a loop. A false `condition` inside a
loop ends the current iteration (continue). Exhausting the loop driver succeeds;
exceeding the maximum blocks. Retry budgets belong to body steps, not the loop.

### Include

An include has only `id`, `type: include`, and `workflow`. At task creation it
is replaced by the referenced workflow's steps. Those steps share the caller's
id namespace and keep the included workflow's defaults. Vincent's local
validator cannot resolve registry-dependent include names; task creation does.

## Nesting rules

| Type | Top level | `parallel` | `loop` body | Inline lane |
|---|:---:|:---:|:---:|:---:|
| `agent` | yes | yes | yes | yes |
| `command` | yes | yes | yes | yes |
| `manual` | yes | no | no | yes |
| `parallel` | yes | no | no | yes |
| `fan_out` | yes | no | no | yes |
| `condition` | yes | no | yes | yes |
| `loop` | yes | no | no | yes |
| `break` | no | no | yes | no |
| `include` | yes | yes* | yes* | yes* |

`include` is accepted only when its expanded steps are valid at that location.
Step ids are unique across the whole file, including parallel and loop bodies;
inline fan-out lanes have their own child-task namespaces.

## Conditions, failures, and checks

`if` must render, after trimming, exactly `true` or `false`. False skips that
step and continues. A `condition` contains only `id`, optional `name`, `type`,
and required `if`; false stops the remaining run successfully. A `break`
similarly contains only identification, type, and `if`.

Use `allow_failure: true` when a failed command or agent is intentional data
for a later decision. Give probes `max_retries: 0`. It advances only after
failures the process itself produced; startup, template, CLI availability, and
authentication failures still block.

`check` is a post-step shell command, not a step type. Its nonzero exit fails
the attempt and participates in retries. `check_timeout` defaults to the daemon
command timeout, independently of the main step timeout.

## Template context

`prompt`, `run`, `check`, `instructions`, and `if` use Go `text/template` with
`missingkey=error`.

| Value | Useful fields |
|---|---|
| `.Task` | `ID`, `Title`, `Description`, `Fields`, `BaseBranch`, `BranchName` |
| `.Project` | `Name`, `Path`, `DefaultBranch` |
| `.Workflow` | `Name`, `Description` |
| `.Step` | `ID`, `Name`, `Index`, `Attempt` |
| `.Loop` | `Index`, `Item`, `IsFirst`, `IsLast` |
| `.Steps` | completed step ids → `Status`, `Result`, `ExitCode` |
| `.Host` | `OS`, `Arch` |
| `.Worktree` | `Path` |
| `.LastFailure` | `Reason`, `Output` during retries |
| `.Conflicts` | conflicted paths during an agent merge resolution |

Only completed steps are visible in `.Steps`. Parallel siblings are not
visible to one another. Within a loop, later body steps see earlier steps from
the current iteration; repeated ids resolve to the latest iteration. Command
`Result` is the last 200 stdout lines, so filter producer output at its source.

Common guards:

```gotemplate
{{ eq (index .Task.Fields "ship") "yes" }}
{{ ne (index .Steps "probe").ExitCode 0 }}
{{ eq (index .Steps "review").Status "approved" }}
{{ ne .Host.OS "windows" }}
```

## Validate

Run locally without a daemon, network, or agent installation:

```sh
vincent version
vincent workflow validate .vincent/workflows/example.yaml
vincent workflow validate .vincent/workflows/example.yaml --json
```

Exit 0 means valid; exit 1 means invalid. Validation checks known keys and
types, templates, ids, durations, agent/model compatibility, nesting, bounds,
and platform tokens. Unknown model values can be warnings because the live CLI
is the final authority. Include resolution is deferred until task creation.

Treat a warning as a review item. The file being syntactically valid YAML is
not evidence that Vincent accepts or safely executes it. Report the exact
Vincent version with the verdict so a future reader can reproduce the result.
