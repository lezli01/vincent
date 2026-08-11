# Writing workflows

A workflow is a YAML file describing an ordered list of steps. Every task runs
one, in its own git worktree on its own branch. This guide is the practical
companion to [spec §8](versions/v0/spec.md), which is the normative reference
when the two disagree.

Four ready-to-copy workflows live in [`examples/`](../examples). Start there.

---

## Where files live

| Scope | Location | Wins |
|---|---|---|
| Project | `.vincent/workflows/*.yaml` inside the repo | ✅ shadows global |
| Global | `{config_dir}/workflows/*.yaml` | |
| Built-in | `adhoc` — one agent step, always available | |

`{config_dir}` is `%APPDATA%\vincent` on Windows, `~/Library/Application
Support/vincent` on macOS, `~/.config/vincent` on Linux. `vincent workflow ls`
shows the merged registry with its scope badges.

The daemon watches both directories and reloads on save — there is no restart
and no "apply" step. A file that fails to parse is reported as invalid and the
previously loaded version keeps running.

Check a file before the daemon does:

```sh
vincent workflow validate .vincent/workflows/feature-pr.yaml
```

That runs locally: no daemon, no network, no agent CLI installed. It is meant
for pre-commit hooks and CI.

---

## The shape of a file

```yaml
name: feature-pr                 # required; how tasks refer to it
description: One line, shown in the picker.

defaults:                        # optional; every step inherits these
  agent: claude
  max_retries: 2
  timeout: 45m

steps:                           # required; runs in order, top to bottom
  - id: implement                # required, unique within the file
    type: agent                  # agent | command | manual
    prompt: |
      Do the thing.
```

Unknown keys are rejected rather than ignored, so a typo is an error at
validation time instead of a setting that silently never applied.

## Three step types

**`agent`** runs an agent CLI headlessly in the worktree. Needs `prompt`.

```yaml
  - id: implement
    type: agent
    agent: claude                # else defaults.agent, else the daemon default
    model: sonnet                # optional; adapter-native
    effort: high                 # optional; not all adapters have one
    permission_mode: full-auto   # full-auto (default) | restricted
    prompt: |
      Implement {{.Task.Title}}.
```

**`command`** runs a shell command in the worktree. Needs `run`.

```yaml
  - id: commit
    type: command
    run: 'git add -A && git commit -m "{{.Task.Title}}"'
    shell: bash                  # optional pin; default is the platform's
    env:
      CI: "true"
```

**`manual`** stops and waits for a person. Needs `instructions`.

```yaml
  - id: review
    type: manual
    instructions: |
      Read the diff for #{{.Task.ID}} before it ships.
```

There is no fourth type. `check` is a **field** on agent and command steps,
not a step of its own — see below.

## Checks are how you stop an agent grading its own homework

An agent step succeeds when the process exits 0 and its stream reports no
error. That is a claim by the agent about its own work. A `check` turns it
into something the machine verifies:

```yaml
  - id: implement
    type: agent
    prompt: …
    check: go build ./... && go test ./...
    check_timeout: 15m
```

The check runs after the step, in the worktree. Non-zero fails the attempt,
and the retry gets the failure appended to its prompt automatically:

```
<previous-attempt-failure attempt="1">
reason: check command failed (exit 1)
--- output (last 200 lines) ---
…
</previous-attempt-failure>
```

That loop — write, verify, be told exactly what failed, try again — is most of
the value of running an agent under a workflow rather than by hand. **If a
step produces something checkable, check it.**

A check can be inverted when the deliverable is a failure. `examples/fix-and-test.yaml`
uses `! go test ./...` for a step whose job is to produce a red test.

## Templates

Prompts, `run`, `check` and `instructions` are Go `text/template`.

| Variable | Contents |
|---|---|
| `.Task` | `ID`, `Title`, `Description`, `Fields`, `BaseBranch`, `BranchName` |
| `.Project` | `Name`, `Path`, `DefaultBranch` |
| `.Workflow` | `Name`, `Description` |
| `.Step` | `ID`, `Name`, `Index`, `Attempt` (1-based) |
| `.Steps` | completed steps by id → `{Status, Result, ExitCode}` |
| `.Worktree` | `Path` |
| `.LastFailure` | retries only: `{Reason, Output}` |

`.Steps` is what makes a multi-step workflow more than a list. Feeding one
step's output into the next is how you split work an agent does badly in one
go:

```yaml
  - id: fix
    type: agent
    prompt: |
      A previous step reported:

      {{.Steps.survey.Result}}

      Fix exactly those items.
```

**Rendering uses `missingkey=error`**, so a typo fails the step *before* any
process starts rather than rendering a silent hole into a prompt. Task fields
are free-form, so read optional ones defensively:

```yaml
{{with index .Task.Fields "ticket"}}Ticket: {{.}}{{end}}
```

## Retries and timeouts

```yaml
defaults:
  max_retries: 2      # attempts after the first
  timeout: 45m        # per attempt
```

Both can be set per step. A step that exhausts its retries blocks the task,
which then waits for a human — nothing is silently abandoned.

## Choosing an agent

Set `agent:` on a step, in `defaults:`, or per task at creation. The
resolution order is step → task override → workflow defaults → adapter default
(§8.6), and **`model` and `effort` only inherit from a level whose agent
matches** — so a claude alias never leaks onto a codex step.

Adapters differ, and the differences are documented rather than smoothed over:

| | claude | codex | cursor |
|---|---|---|---|
| Mid-run questions | ✅ | — | — |
| Reports cost | ✅ | — | — |
| `effort:` | ✅ | ✅ | **—** (it lives in the model id) |
| `restricted` on Windows | ✅ | ✅ | **—** (step fails, never downgrades) |

`vincent workflow validate` catches an effort value belonging to another
adapter's catalog. It cannot catch a model your account lacks — the CLI is the
final authority there, and you will find out at run time.

## Permission modes

`full-auto` is the default and **the agent can run arbitrary commands as you**
(§16). The worktree isolates collisions between tasks, not privileges.

`restricted` maps to each adapter's own confinement. Use it for steps that
have no business running commands — a docs pass, a review. Note that an
adapter which cannot restrict on your platform **fails the step** rather than
running it unrestricted, because a restricted mode that quietly isn't
restricted is worse than none.

## A checklist before you commit one

- `vincent workflow validate` passes with no warnings.
- Every step that produces something checkable has a `check`.
- Prompts say what "done" means, not just what to attempt.
- Any step that pushes, publishes or deletes sits **after** a `manual` gate.
- Optional `.Task.Fields` reads are wrapped in `{{with}}`.
- Commands work in the shell your teammates have — `&&` is fine everywhere;
  `test -f` is not.
