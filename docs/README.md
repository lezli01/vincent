<p align="center">
  <img src="assets/logo.png" alt="vincent" width="420">
</p>

# vincent documentation

vincent is a local-first control plane for AI coding-agent workloads. A
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
| [Using the TUI](guides/tui.md) | The board, task detail, the four takeover screens, every key |
| [Scripting vincent](guides/scripting.md) | `--json`, exit codes, and driving the API directly from a script or CI |
| [Running at login](guides/running-at-login.md) | `vincent service install` on launchd, systemd and Task Scheduler |
| [Troubleshooting](guides/troubleshooting.md) | The failures people actually hit, and what each one means |

## Platforms

vincent runs the same feature set on all three platforms, and the places where
that is *not* true are stated rather than smoothed over.

| Page | What it covers |
|---|---|
| [Windows](platforms/windows.md) | SmartScreen, `%APPDATA%`/`%LOCALAPPDATA%`, PowerShell command steps, the Scheduled Task, the one restricted-mode gap |
| [macOS](platforms/macos.md) | Gatekeeper and quarantine, `~/Library/Application Support`, the LaunchAgent, `PATH` capture |
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
- [Changelog](../CHANGELOG.md) — user-visible changes in every release.
- [Commercial licensing](../COMMERCIAL-LICENSE.md) — vincent is source-available
  and dual-licensed: free for personal and non-commercial use under the
  [PolyForm Noncommercial License 1.0.0](../LICENSE), separate license required
  for commercial or business use.

## Contributing

Want to improve vincent itself? The [Contributing guide](../CONTRIBUTING.md)
covers development setup, architecture pointers, documentation expectations,
tests, cross-platform checks, commit conventions, and pull requests.

The repository also retains maintainer specifications, engineering work
records, acceptance walkthroughs, and historical decision records. They support
implementation work and stable code citations, but they are not prerequisites
for installing or using vincent; the contributing guide points to the relevant
record when a change needs one.

## Conventions in these docs

- `{config_dir}` and `{data_dir}` are the platform-native directories resolved
  in [Files and directories](reference/files.md).
- Shell samples are POSIX unless a PowerShell equivalent is shown beside them.
