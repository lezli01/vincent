# Writing workflows

A workflow is a YAML file describing an ordered list of steps. Every task runs
one, in its own git worktree on its own branch. This guide is the practical
companion to [spec §8](../versions/v0/spec.md), which is the normative reference
when the two disagree; the field-by-field version is the
[workflow schema](../reference/workflow-schema.md).

Four ready-to-copy workflows live in [`examples/`](../../examples). Start there.

- [Where files live](#where-files-live)
- [The shape of a file](#the-shape-of-a-file)
- [Three step types](#three-step-types)
- [Checks](#checks-are-how-you-stop-an-agent-grading-its-own-homework)
- [Templates](#templates)
- [Retries and timeouts](#retries-and-timeouts)
- [Choosing an agent](#choosing-an-agent)
- [Permission modes](#permission-modes)
- [Mid-run questions](#mid-run-questions)
- [Writing portable command steps](#writing-portable-command-steps)
- [Patterns that work](#patterns-that-work)
- [A checklist before you commit one](#a-checklist-before-you-commit-one)

---

## Where files live

| Scope | Location | Wins |
|---|---|---|
| Project | `.vincent/workflows/*.yaml` inside the repo | ✅ shadows global |
| Global | `{config_dir}/workflows/*.yaml` | |
| Built-in | `adhoc` — one agent step, always available | |

`{config_dir}` is `%APPDATA%\vincent` on Windows, `~/Library/Application
Support/vincent` on macOS, `~/.config/vincent` on Linux (the full table is in
[Files and directories](../reference/files.md)). `vincent workflow ls` shows the
merged registry with its scope badges; add `--project <id>` to include that
repository's own files.

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

The full picture, including what "restricted" means for each CLI, is in
[Agent CLIs](agents.md) and the [Security model](../security-model.md).

## Mid-run questions

An input-capable adapter (claude today) can surface a structured question from
the agent mid-step. Only machine-readable requests from the agent's event stream
qualify — vincent never guesses that some output text "looks like a question".

```yaml
defaults:
  on_input: wait        # wait (default) | deny
  input_timeout: 24h    # bounds each wait; also settable per step
```

- **`wait`** — the task moves to `awaiting_input`, **keeping its concurrency
  slot** (the agent process is alive, idle on its stdin). The step's own timeout
  clock pauses: it measures agent work, not human latency. The board pins the
  task, rings the terminal bell, and the answer form opens on `enter`. Answering
  resumes the same session where it stopped.
- **`deny`** — for runs that must stay strictly unattended. vincent answers
  immediately through the adapter: questions get a canned "no user is available;
  decide with your best judgment", permission requests are denied. The task never
  leaves `running`.

`input_timeout` is measured **per request** — a new question starts a fresh
window. On expiry the process is killed and the attempt fails under the normal
retry policy.

Adapters that report `supports_input: false` (codex, cursor) never produce input
requests, and `on_input` has no effect on their steps.

## Writing portable command steps

`run` and `check` are rendered, then handed to a shell:

| Platform | Shell |
|---|---|
| POSIX | `/bin/sh -c "<rendered>"` |
| Windows | `pwsh -NoProfile -Command "<rendered>"`, falling back to `powershell` |

Pin one explicitly with `shell: sh | pwsh | cmd` when a step is only ever meant
to run one way. vincent makes no attempt to translate between them: cross-OS
portability of command steps is the author's job.

Every command and check step runs with cwd set to the worktree, inherits the
daemon's environment, and additionally gets:

```
VINCENT_TASK_ID       VINCENT_TASK_TITLE     VINCENT_PROJECT_NAME
VINCENT_PROJECT_PATH  VINCENT_WORKTREE       VINCENT_BRANCH
VINCENT_BASE_BRANCH   VINCENT_STEP_ID        VINCENT_STEP_ATTEMPT
VINCENT_WORKFLOW
```

Those are the portable way to reach task context from a script you would rather
keep out of the YAML:

```yaml
  - id: notify
    type: command
    run: ./scripts/notify.sh
    env:
      SLACK_CHANNEL: "#builds"
```

## Patterns that work

**Split what an agent does badly in one go.** A survey step whose output feeds a
fix step beats one prompt asking for both, because `.Steps` carries the first
result into the second and each half is individually retryable.

**Make the deliverable checkable.** `check: go build ./... && go test ./...` on
the step that writes code turns "the agent said it was done" into "the compiler
agrees". If a step genuinely produces nothing checkable, that is usually a sign
it is doing two things.

**Invert the check when failure is the point.** `check: '! go test ./...'` on a
step whose job is to produce a red test — see
[`fix-and-test`](../../examples/fix-and-test.yaml). Without it, an agent asked
to "add a failing test and fix it" reliably writes a test that passes against
its own fix.

**Gate before anything leaves the machine.** Put a `manual` step in front of any
step that pushes, publishes, deploys or deletes. The gate costs you one
keystroke and is the difference between "an agent wrote to a branch" and "an
agent wrote to production".

**Use `restricted` for steps that only read and write text.** A docs pass has no
business running commands; the mode says so, and the adapter enforces it.

## A checklist before you commit one

- `vincent workflow validate` passes with no warnings.
- Every step that produces something checkable has a `check`.
- Prompts say what "done" means, not just what to attempt.
- Any step that pushes, publishes or deletes sits **after** a `manual` gate.
- Optional `.Task.Fields` reads are wrapped in `{{with}}`.
- Commands work in the shell your teammates have — `&&` is fine everywhere;
  `test -f` is not.

---

## See also

- [Workflow schema](../reference/workflow-schema.md) — every field, every
  default, in one table.
- [Agent CLIs](agents.md) — what each adapter can and cannot do.
- [Task lifecycle](../reference/task-lifecycle.md) — what `blocked` means and
  what you can do from there.
- [Example workflows](../../examples) — the four shipped files.
