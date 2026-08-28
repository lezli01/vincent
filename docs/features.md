# Features

Vincent turns locally installed coding agents into managed, repeatable
workloads. Your repositories, credentials, worktrees, transcripts, and task
state stay on your machine; vincent provides the control plane around them.

## At a glance

| Area | Highlights |
|---|---|
| Orchestration | Durable daemon, priority queue, global and per-project concurrency, per-user service installation |
| Git isolation | One worktree and branch per task, configurable branch names, safe archive cleanup |
| Workflows | Validated YAML, templates, declared task fields, checks, retries, timeouts, platform restrictions |
| Control flow | Parallel groups, isolated fan-out and merge, conditions, loops, breaks, reusable workflow includes |
| Agents | Claude Code, Codex, and Cursor; per-workflow, per-step, and per-task selection |
| Human oversight | Approval gates, mid-run answers where supported, blocked-step recovery, edit-and-retry, ad-hoc repair agents, follow-up runs on finished tasks, a notify hook that reaches you with no client open |
| Visibility | Grouped task board, live output, durable transcripts, metrics, file-grouped diffs, workflow graph |
| GitHub | Create a task from an issue, prefilled and editable; issue details in templates; read-only, no stored credential |
| Integration | Full CLI, JSON output, stable exit codes, localhost REST API, durable state SSE and live output streams |
| Operations | Automatic usage-limit waits, one-command diagnostics, orphan cleanup, database integrity checks, backup and restore |
| Platforms | Windows, macOS, and Linux; Homebrew, a universal macOS `.pkg`, WinGet, Scoop, mise, deb/rpm, and archives |

## Orchestrate work instead of terminals

The daemon owns task state, agent processes, workflow execution, scheduling,
and git worktrees. The TUI and CLI are clients, so closing either does not stop
the work behind it. `vincent service install` can start the daemon with your
login on launchd, systemd, or Windows Task Scheduler.

The scheduler admits work by priority and creation time while enforcing a
global concurrency cap and an optional cap for each project. A task waiting at
a human gate, blocked step, or fan-out join releases its slot instead of
starving other work.

Each task runs in a dedicated git worktree on its own branch. Parallel tasks do
not collide with one another, and vincent never changes your active checkout.
The branch convention is configurable globally, per project, or for one task.

## Express the workflow the work needs

Workflows are reusable YAML files that reload when saved and can travel with a
repository under `.vincent/workflows/`. Strict validation catches unknown
fields and invalid combinations before a task starts.

Three step types perform work or wait for a person:

- `agent` runs Claude Code, Codex, or Cursor with a rendered prompt.
- `command` runs a deterministic shell command in the task worktree.
- `manual` creates an explicit approval gate.

Six structural types compose those steps:

- `parallel` runs independent sub-steps concurrently in one worktree.
- `fan_out` creates child tasks with isolated branches and merges their results.
- `condition` ends a sequence early when a rendered condition is true.
- `loop` repeats a body by count or over a discovered list.
- `break` exits a loop cleanly.
- `include` reuses another workflow inside the current one.

Prompts, commands, checks, and instructions use Go templates. A step can read
task and project data, declared task fields, loop context, and earlier step
results. Workflows can declare ordered inputs with labels, descriptions,
required flags, types, and validation patterns; the TUI renders them before the
task is submitted, while additional ad hoc fields remain available.

The [workflow guide](guides/workflows.md) explains the patterns, and the
[workflow schema](reference/workflow-schema.md) lists every field.

## Verify outcomes and recover cleanly

An agent saying “done” is not the success condition. Agent and command steps
can carry a `check` command, and the step advances only when both its body and
its check succeed. A retry receives the actual failure, so the next attempt can
correct the work rather than guess what happened.

Timeouts are enforced, retry counts are bounded, and exhausted steps become
`blocked` instead of being silently skipped. From there a person can retry,
edit the step for this task and retry, skip it, or cancel the task.

Spend can be bounded as well. `max_task_cost_usd` blocks a task with
`cost_limit` once its cost across every attempt passes a ceiling you set. It is
off by default, it counts one task at a time, and it only sees the agents that
report cost at all.

When the problem is in the worktree rather than in the step, a blocked task can
also be **repaired**: one throwaway agent, prompted by you and handed the
blocked step's failure context, runs in that task's existing worktree and
branch. It changes files and nothing else — the task returns to `blocked` at the
same step with the same reason, so you read the diff and then decide. The repair
is recorded as its own step run with its own transcript and cost, and does not
consume the blocked step's retries.

A finished task keeps its worktree, its branch and its commits until you archive
it, and that window is where the last mile of real work lives — a branch that
needs rebasing onto a `main` that moved, one more commit a review asked for, a
stray file to drop. A **follow-up run** does that work inside vincent: give a
`done` or `aborted` task an agent prompt, a shell command, or the name of a
workflow, and it runs on that task's own branch in that task's own worktree,
recorded in its own ledger with a transcript and cost accounting. It is
repeatable, and it never changes the task's verdict — a done task comes back
`done` and an aborted one comes back `aborted`. It is the one human action with
a command line, because "rebase these six finished branches" is a batch.

Every state transition is persisted before execution. If the daemon dies
mid-step, restart recovery finalizes the interrupted attempt, verifies and
stops orphan processes, and reruns the step without charging it as a failed
retry. Every attempt also keeps a durable JSONL transcript.

When Claude Code reports a usage limit, vincent treats it as a temporary wait
instead of a failure: the task returns to the queue without consuming a retry or
slot, shows its next admission time, and starts again when the window reopens.

A step that fails on something transient can borrow the same wait. Give it
`retry_backoff: 30s` and its retry is paced rather than immediate: the task
returns to the queue, gives up its slot so other work carries on, shows when it
will resume, and re-runs the step by itself. Unlike a usage limit the attempt
still counts against `max_retries` — the wait decides when a retry happens, not
whether there is one.

## Keep people in the loop

Human oversight is part of the workflow instead of an informal terminal habit:

- Add `manual` steps before publishing, merging, deployment, or any other
  irreversible boundary.
- Review the task's file-grouped git diff before approving it.
- Answer a supported agent's structured question while its session remains
  alive, or require that capability when the workflow depends on it.
- Pause active work at a step boundary, change task priority, and recover a
  blocked step without discarding its branch or transcript.
- Send a one-off repair agent into a blocked task's worktree when the fix is a
  file change, and still decide yourself whether the step then re-runs.
- Follow a finished task up with one more agent run, shell command or workflow
  in its existing worktree, as many times as it takes, before archiving it.
- Use bulk selection to act on several eligible tasks while refusals remain
  selected for follow-up.
- Let a long-running step say what it is doing: `vincent status "<message>"`
  from inside an agent or command step reports a line that shows live on the
  board and stays on the finished attempt as the step's own account of how it
  went. It is opt-in per workflow — vincent never asks an agent for it.

- **Be told when you are needed, with nothing open.** Point `notify.command` at
  a script and the daemon runs it whenever a task enters a state you listed —
  `blocked`, `awaiting_gate`, `awaiting_input`, `done` — writing a JSON envelope
  with the task, the transition, the reason and the agent's question to its
  standard input. It composes with whatever you already use:
  `terminal-notifier`, `notify-send`, `msg`, a Slack `curl`, a file drop. That
  is the point of walking away: the daemon is designed to run with no client
  attached, and this is how it says it needs you. See
  [`notify`](reference/configuration.md#notify).

The daemon computes the actions valid in each state and sends that list to
every client, so the TUI, CLI, and API agree on what can happen next. See the
[task lifecycle](reference/task-lifecycle.md) for the complete model.

## Operate from a purpose-built TUI

Running `vincent` opens a Bubble Tea interface for active agent workloads:

- A filterable, grouped task board shows state, current step, elapsed time,
  reported cost, and — on a wide terminal — the step's own status message.
- Task detail keeps the attempt timeline beside live output and the git diff.
- Guided task creation exposes project, workflow, declared fields, git and
  priority settings, agent overrides, and a final review stage.
- Project and workflow workspaces keep navigation visible beside contextual
  details on wider terminals and fall back to compact layouts when needed.
- The workflow graph visualizes parallel groups, fan-out lanes and merges,
  conditions, loops, guards, checks, and nested includes.

The screenshots in the main [README](../README.md#tui-tour) are real renders
using representative workloads. [Using the TUI](guides/tui.md) documents every
view and key.

## Bring the agent you already use

Vincent invokes locally installed and authenticated CLIs; it stores no agent
API keys or login credentials.

| Agent | What vincent integrates |
|---|---|
| Claude Code | Model and effort discovery, usage and cost reporting, restricted mode, and mid-run questions |
| Codex | Headless execution and restricted mode; no mid-run input or cost reported by the CLI |
| Cursor | Headless execution and model discovery; reasoning effort is part of the model id, and restricted mode is unavailable on Windows |

Agent, model, effort, and permission settings resolve from step to task override
to workflow default to adapter default. The TUI shows which level won, and free
text remains available when a newly released model is not in a catalog yet.
Read [Agent CLIs](guides/agents.md) for installation and capability details.

## Script and integrate it

The TUI, command-line subcommands, and external integrations all use the same
localhost API.

- Every subcommand supports `--json` for machine-readable output.
- Stable exit codes distinguish a rejected request from an unavailable daemon.
- REST endpoints cover projects, workflows, tasks, actions, output, and diffs.
- Creating a task takes an optional idempotency key, so a create that loses its
  response can be re-sent without making a second task.
- Server-sent events provide durable state replay and live per-task output.
- `vincent workflow validate` works without a daemon or installed agent CLI, so
  it fits pre-commit hooks and CI. `vincent workflow render` belongs there too:
  it executes a workflow's templates against a preview context, so a reference
  no task would satisfy is caught before a task is created.

Start with [Scripting vincent](guides/scripting.md), then use the complete
[HTTP API reference](reference/api.md) when you need direct integration.

## Start from a GitHub issue

On a project whose `origin` remote points at github.com, the new-task form
offers an issue row above the title. It opens the same type-to-filter picker the
project and workflow rows use, listing open issues newest first. Selecting one
fills in the title — prefixed `#N`, so the board row says which issue it is —
the body plus a `GitHub issue #N: <url>` link line, and any declared `fields:`
named exactly `issue`, `labels`, `assignee`, or `milestone`. Every value lands
in an ordinary editable row, so a guess can be corrected or cleared before the
task exists.

A declared `issue` field gets the issue **number**, which is how a `command`
step reads it: step bodies receive the environment of [§8.5](reference/workflow-schema.md#environment),
not the template context, so `{% raw %}{{ index .Task.Fields "issue" }}{% endraw %}` is what puts the
number into a `run:` body.

Templates receive the issue as `.Issue` — number, title, body, URL, state,
labels, author, assignee, and milestone — zero-valued when nothing is linked, so
`{% raw %}{{ if .Issue.Number }}{% endraw %}` lets one workflow serve both. The issue is fetched once
at creation and stored on the task, so runs stay reproducible and no step render
touches the network. Fan-out lanes inherit their parent's issue.

`vincent task add --github-issue 200` takes the same path as the form, with
explicit flags winning, and `vincent github issues` lists issues without the TUI.

Vincent stores no credential: it prefers your existing `gh` CLI and falls back to
`GITHUB_TOKEN`/`GH_TOKEN` from the daemon's environment. Access is read-only —
nothing is ever written to GitHub. When it is unavailable the row does not
appear, `vincent doctor` reports why, and everything else is unaffected. Set
`github.enabled: false` in `config.yaml` to switch it off entirely.

See the [new-task form](guides/tui.md), the
[configuration reference](reference/configuration.md), and the
[workflow schema](reference/workflow-schema.md) for `.Issue`.

## Diagnose and maintain it

`vincent doctor` produces one report covering paths, configuration, daemon
health, the recent log tail, the database's footprint, row counts and integrity,
agent availability, login state and whether the installed CLI build is one
vincent has been tested against, the GitHub integration, disk use, worktrees,
and task counts. It supports JSON output for bug
reports and automation, while `--fix` can reclaim orphans and compact the
database when it is safe to do so.

`vincent daemon logs` and `vincent task transcript` put the two artifacts a
failure is diagnosed from on the command line, so neither needs the TUI. The log
command reads `{data_dir}/logs/daemon.log` **from disk and never contacts the
daemon**, so it still answers when the daemon is the thing that is broken; `-f`
follows it. The transcript command prints one attempt's complete record — the
file `vincent task show` only names — rendered as text for a person, as NDJSON
for `jq`, or as the agent's own dialect byte for byte, with `-f` following an
attempt while it is still running.

`vincent gc` focuses on orphaned worktrees and transcripts. It supports a dry
run, refuses to remove dirty or unknown work without an explicit force, and
never deletes a branch or anything outside vincent's data roots. The daemon
also reports orphaned paths at startup.

`vincent daemon backup` writes one `.tar.gz` holding a consistent copy of the
database, every transcript, your `config.yaml` and your global workflows. The
daemon takes the copy with SQLite's own `VACUUM INTO`, so it can be taken
**while tasks are running** — unlike copying `vincent.db` by hand, which under
WAL is missing whatever has not been checkpointed. `vincent daemon restore` is
the reverse, runs against a stopped daemon, and deletes nothing: a destination
that already holds state needs `--force`, which moves the old state aside as
`<name>.bak-<timestamp>`.

See [Troubleshooting](guides/troubleshooting.md) for the diagnostic workflow,
[Files](reference/files.md#backup-and-restore) for what an archive holds and
what it leaves out, and the [CLI reference](reference/cli.md) for exact flags
and exit codes.

## Run it on your platform

Vincent is one self-contained Go binary with no runtime, CGO dependency, or
external database. Releases cover Windows, macOS, and Linux, and are published
as archives plus platform-friendly packages:

- Homebrew or a universal installer package on macOS
- WinGet or Scoop on Windows
- deb and rpm packages on Linux
- mise or release archives on all platforms

Release archives are checksummed, the checksum manifest is signed with cosign,
and builds carry GitHub attestations. There is no OS code signing on either
macOS or Windows — both are recurring certificate purchases this project has not
made — so a downloaded release meets Gatekeeper or SmartScreen once, and
[Installation](getting-started/installation.md) documents the one command that
clears it. Platform-specific
installation, service, shell, and restricted-mode differences are documented in
[Installation](getting-started/installation.md) and the
[platform guides](README.md#platforms).

## Common ways to use vincent

- **Verified feature delivery:** implement with an agent, build and test with a
  command, stop for review, then publish only after approval.
- **Converging repair loops:** run a probe, let an agent repair the result, and
  repeat until the check is green or the iteration bound is reached.
- **Parallel implementation:** fan out independent changes into child tasks and
  merge the finished branches back in a declared order.
- **Cross-agent review:** implement with one adapter and review with another.
- **Repository-specific intake:** declare ticket numbers, environment choices,
  flags, and other validated task fields directly in the workflow.
- **Repeatable maintenance:** replace prompt-heavy steps with commands where the
  operation is deterministic, reserving agent calls for reasoning and edits.

Ready to try it? Follow the [Quickstart](getting-started/quickstart.md), write
your first workflow with `vincent workflow init` (`--from` starts it from one of
the [example workflows](../examples)), or install the
[workflow-authoring skill](../skills/vincent-workflows/SKILL.md).
