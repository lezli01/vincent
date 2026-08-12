<p align="center">
  <img src="assets/logo.png" alt="vincent" width="420">
</p>

# vincent documentation

vincent is a local-first control plane for AI coding-agent workloads. A
background daemon owns the state and the execution — SQLite, git worktrees,
agent CLI subprocesses — and every client (the TUI, the `vincent` subcommands,
`curl`) is a thin consumer of its localhost API.

New here? Read [Concepts](getting-started/concepts.md) for the five nouns the
whole system is built from, then walk the
[Quickstart](getting-started/quickstart.md).

---

## Getting started

| Page | What it covers |
|---|---|
| [Installation](getting-started/installation.md) | Download, verify, and put `vincent` on your `PATH`; installing an agent CLI; upgrading and uninstalling |
| [Quickstart](getting-started/quickstart.md) | Register a repository, run your first task, approve it, ship the branch |
| [Concepts](getting-started/concepts.md) | Daemon, project, workflow, task, step, worktree — and how they fit together |

## Guides

| Page | What it covers |
|---|---|
| [Writing workflows](guides/workflows.md) | The authoring guide: step types, checks, templates, retries, permission modes |
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
- [Specification](versions/v0/spec.md) — the normative document. Section
  numbers (§7.2, §9.7 …) are cited throughout the code and these docs; when a
  guide and the spec disagree, the spec wins.
- [Task breakdown](versions/v0/tasks.md) — how the implementation was
  delivered, task by task, with the decisions behind each one.
- [Example workflows](../examples) — four ready-to-copy files.

## Conventions in these docs

- `{config_dir}` and `{data_dir}` are the platform-native directories resolved
  in [Files and directories](reference/files.md).
- Shell samples are POSIX unless a PowerShell equivalent is shown beside them.
- `§n` references point at [the spec](versions/v0/spec.md).
