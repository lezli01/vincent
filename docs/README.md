<p align="center">
  <img src="assets/logo.png" alt="vincent" width="420">
</p>

# vincent documentation

Vincent is a local-first control plane for AI coding-agent workloads. A
background daemon owns the state and the execution — SQLite, git worktrees,
agent CLI subprocesses — and every client (the TUI, the `vincent` subcommands,
`curl`) is a thin consumer of its localhost API.

It gives locally installed agents durable scheduling, reusable workflows,
isolated branches, deterministic checks, human gates, crash recovery, and one
place to see the work. Start with the [feature tour](features.md), or run a real
task with the [Quickstart](getting-started/quickstart.md).

---

## Explore vincent

| I want to… | Start here |
|---|---|
| Understand why vincent is awesome | [Why vincent is awesome](why/README.md) |
| Understand what vincent can do | [Features](features.md) |
| Run my first managed agent task | [Quickstart](getting-started/quickstart.md) |
| Learn the daemon, workflow, task, and worktree model | [Concepts](getting-started/concepts.md) |
| Build reliable, cost-aware workflows | [Writing workflows](guides/workflows.md) |
| Operate active workloads from the terminal | [Using the TUI](guides/tui.md) |
| Integrate vincent with scripts or CI | [Scripting vincent](guides/scripting.md) |

## What makes it different

- **Work survives the client.** Close the TUI or terminal and the daemon keeps
  scheduling, running, and recording tasks.
- **Workflows are more than prompts.** Mix agents with commands, approval gates,
  parallel groups, fan-out, conditions, loops, and reusable includes.
- **Every task is isolated.** A dedicated git worktree and branch protect your
  checkout and let multiple changes run at once.
- **Success is verified.** Checks, retries, timeouts, blocked-step recovery, and
  durable transcripts make outcomes inspectable instead of aspirational.
- **People retain control.** Review diffs, approve gates, answer supported agents,
  edit and retry failed steps, and choose exactly when delivery happens.
- **The interface is yours to choose.** Use the full TUI, JSON-capable CLI, or
  localhost REST + SSE API on Windows, macOS, and Linux.

## Why vincent is awesome

The reference tells you how vincent works. This series tells the story behind
what makes it awesome: the repeated agentic workflows, practical frustrations,
and design choices that made a durable orchestrator feel necessary.

| Article | What it covers |
|---|---|
| [The workflow I kept repeating—and how vincent was born](why/the-workflow-i-kept-repeating-and-how-vincent-was-born.md) | How a recurring QA-ticket routine revealed that the missing tool was a reusable workflow, not another prompt |
| [Spend inference only where it belongs](why/spend-inference-only-where-it-belongs.md) | How command-first workflows reduce cost by reserving agents for work that actually requires judgment |
| [“Done” is not a success condition](why/done-is-not-a-success-condition.md) | Why agent output becomes trustworthy only when objective checks define success |
| [Automation without giving up control](why/automation-without-giving-up-control.md) | How human gates keep review and authorization inside an otherwise automated process |
| [The terminal should not own the work](why/the-terminal-should-not-own-the-work.md) | Why durable execution belongs to a daemon rather than a terminal tab |
| [Failure should be a state, not a dead end](why/failure-should-be-a-state-not-a-dead-end.md) | How retained context and explicit recovery actions turn failure into a decision point |
| [One task, one branch, no checkout traffic jam](why/one-task-one-branch-no-checkout-traffic-jam.md) | How isolated worktrees make concurrent agentic work practical |
| [A workflow is executable team knowledge](why/a-workflow-is-executable-team-knowledge.md) | Why a versioned playbook is more valuable than a prompt only one person remembers |
| [Bring your agent, keep your control plane](why/bring-your-agent-keep-your-control-plane.md) | How local adapters provide choice without pretending every agent has the same capabilities |
| [From a wall of terminals to one control room](why/from-a-wall-of-terminals-to-one-control-room.md) | Why agentic workloads need an interface built around state, history, and attention |

[Browse the series →](why/README.md)

## Getting started

| Page | What it covers |
|---|---|
| [Installation](getting-started/installation.md) | Download, verify, and put `vincent` on your `PATH`; installing an agent CLI; upgrading and uninstalling |
| [Quickstart](getting-started/quickstart.md) | Register a repository, run your first task, approve it, ship the branch |
| [Concepts](getting-started/concepts.md) | Daemon, project, workflow, task, step, worktree — and how they fit together |

## Guides

| Page | What it covers |
|---|---|
| [Writing workflows](guides/workflows.md) | The authoring guide, in 14 sections: the nine step types, control flow, templates, checks, retries, agents, portability |
| [Agent CLIs](guides/agents.md) | Claude Code, Codex and Cursor: installing, authenticating, and what each one can and cannot do |
| [Using the TUI](guides/tui.md) | The board, task detail, the five takeover screens, every key |
| [Scripting vincent](guides/scripting.md) | `--json`, exit codes, and driving the API directly from a script or CI |
| [Running at login](guides/running-at-login.md) | `vincent service install` on launchd, systemd and Task Scheduler |
| [Troubleshooting](guides/troubleshooting.md) | The failures people actually hit, and what each one means |

## Platforms

Vincent runs the same feature set on all three platforms, and the places where
that is *not* true are stated rather than smoothed over.

| Page | What it covers |
|---|---|
| [Windows](platforms/windows.md) | SmartScreen, `%APPDATA%`/`%LOCALAPPDATA%`, PowerShell command steps, the Scheduled Task, the one restricted-mode gap |
| [macOS](platforms/macos.md) | Gatekeeper, the quarantine attribute, the universal `.pkg`, `~/Library/Application Support`, the LaunchAgent, `PATH` capture |
| [Linux](platforms/linux.md) | XDG directories, the systemd user unit, `loginctl enable-linger` |

## Reference

| Page | What it covers |
|---|---|
| [CLI](reference/cli.md) | Every command, flag, and exit code |
| [Configuration](reference/configuration.md) | `config.yaml` key by key, plus per-project settings |
| [Files and directories](reference/files.md) | Where vincent puts everything, on every platform |
| [Workflow schema](reference/workflow-schema.md) | The complete YAML field reference |
| [Task lifecycle](reference/task-lifecycle.md) | States, human actions, step outcomes, block reasons |
| [HTTP API](reference/api.md) | REST endpoints, the SSE streams, the error envelope |

## Also here

- [Security model](security-model.md) — what full-auto means, what the worktree
  does and does not isolate, and how to tighten it.
- [FAQ](faq.md) — the short answers.
- [Example workflows](../examples) — five ready-to-copy files.
- [Changelog](https://lezli01.is-a.dev/vincent/changelog.html) — user-visible changes in every release.

## Contributing

Want to improve vincent itself? The [Contributing guide](https://lezli01.is-a.dev/vincent/contributing.html)
covers development setup, architecture pointers, documentation expectations,
tests, cross-platform checks, commit conventions, and pull requests.

The repository also retains maintainer specifications, engineering work
records, acceptance walkthroughs, and historical decision records. They support
implementation work and stable code citations, but they are not prerequisites
for installing or using vincent; the contributing guide points to the relevant
record when a change needs one.

## Conventions in these docs

- Write `vincent` with a lowercase `v`, except when it begins a complete
  sentence. Preserve case-sensitive external identifiers such as
  `lezli01.Vincent` exactly.
- `{config_dir}` and `{data_dir}` are the platform-native directories resolved
  in [Files and directories](reference/files.md).
- Shell samples are POSIX unless a PowerShell equivalent is shown beside them.
