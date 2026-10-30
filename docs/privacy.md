---
title: Privacy policy
description: What vincent stores on your machine, what it passes to agent CLIs, what it sends over the network, and how to delete it.
permalink: /privacy.html
---

# Privacy policy

**Last updated:** 2026-10-30

vincent is software you run on your own machine. It has no servers, no
accounts and no telemetry, and its publisher, László Szabó, receives nothing
from your installation. This page says what the program itself keeps, where it
keeps it, what it hands to other programs, and which network calls it makes. It
describes vincent as it works today; the [security model](security-model.md)
and the [files and directories reference](reference/files.md) are the long
versions of the same facts.

- [What is stored locally, and why](#what-is-stored-locally-and-why)
- [What is passed to agent CLIs and their providers](#what-is-passed-to-agent-clis-and-their-providers)
- [Retention and deletion](#retention-and-deletion)
- [How tokens and configuration are protected](#how-tokens-and-configuration-are-protected)
- [Telemetry and network calls](#telemetry-and-network-calls)
- [Contact](#contact)

---

## What is stored locally, and why

Everything vincent keeps is in two directories under your user account, plus
the git repositories you register as projects. Nothing is stored anywhere else.

| Purpose | Linux | macOS | Windows |
|---|---|---|---|
| Config | `~/.config/vincent/` | `~/Library/Application Support/vincent/` | `%APPDATA%\vincent\` |
| Data | `~/.local/share/vincent/` | `~/Library/Application Support/vincent/data/` | `%LOCALAPPDATA%\vincent\` |

On Linux `XDG_CONFIG_HOME` and `XDG_DATA_HOME` are honored, and on every
platform `VINCENT_CONFIG_DIR` and `VINCENT_DATA_DIR` override both.

**In the data directory:**

- **`vincent.db`**, an SQLite database: your projects, tasks, chats, issues,
  the step runs of each task — including the **rendered prompt or command**
  each attempt was handed — and the event history the clients replay. This is
  the daemon's working state; without it nothing survives a restart.
- **Transcripts** under `transcripts/`: the agent CLI's own output stream for
  every step attempt and chat turn, stored verbatim so a run can be inspected
  and replayed.
- **Daemon logs** under `logs/`, for diagnosing the daemon itself.
- **`token`**, the bearer token that authenticates clients to the local API,
  and **`daemon.json`** (port, process id, start time), which is how clients
  find the daemon. `daemon.lock` enforces a single instance.
- **`tui.json`**, the terminal UI's own view state.
- **Staged proposals** under `trigger-proposals/` and `workflow-proposals/`,
  while a task that drafts them waits for your approval.
- **Backups** under `backups/`, only if you turn scheduled backups on.
- **Worktrees** under `worktrees/`: one git working copy per task or chat.

**In the config directory:** `config.yaml`, your global workflows and your
event triggers. These are files you write; the daemon writes them only when a
client asks it to.

**In your repositories:** each task and chat works on its own branch, named
`vincent/{id}-{slug}` by default, with its worktree registered in that
repository. What an agent commits stays in your repository like any other
commit.

## What is passed to agent CLIs and their providers

vincent never calls a model provider itself and holds no provider credential.
An agent step or a chat turn **spawns the agent CLI you installed** — Claude
Code, Codex, Cursor or GitHub Copilot — and hands it the step's prompt. That
CLI then reads and edits your repository and talks to its provider under its
own login and its own terms. What the provider receives, keeps and uses is
governed by that provider's policy, not this one.

The daemon also spawns those CLIs for **probes**, outside any task: version and
`--help` checks, the option catalog and login status. Some of these contact
the provider under the CLI's own credentials — for example cursor's `models`
and `status` probes are network calls, and `claude auth status` reports the
CLI's login. vincent reads only the command's output; it never reads the CLIs'
credential files.

If you run a step in a container, the CLI's credential directories
(`~/.claude`, `~/.codex`, `~/.cursor`, `~/.copilot`) are mounted into it by
default so the CLI can log in; see the
[security model](security-model.md#credentials).

## Retention and deletion

- **Transcripts.** Those of an archived task, or of an ended chat, are deleted
  once they are older than
  [`transcript_retention_days`](reference/configuration.md#transcript_retention_days)
  — **90 days by default**, measured from archiving or ending. `0` keeps them
  forever. Transcripts of tasks and chats that are still live are kept.
- **Task and chat records.** Archiving keeps the row and removes the
  worktree. [`vincent task delete`](reference/cli.md#vincent-task-delete) and
  [`vincent chat delete`](reference/cli.md#vincent-chat-delete) permanently
  remove an archived row together with its transcripts. Deleting a project
  removes its task rows; [`vincent gc`](reference/cli.md#vincent-gc) reclaims
  the worktrees and transcript directories left with no row.
- **Branches.** Archiving deletes a task's branch only when it received no
  commit. A branch that holds commits is never deleted by vincent.
- **Logs.** `daemon.log` rotates at 10 MB and keeps three rotated files.
- **Backups.** Scheduled backups are off by default. When on, the daemon keeps
  the newest [`backup.keep`](reference/configuration.md#backup) archives (7 by
  default) and deletes older ones. A backup contains the database, every
  transcript and your config.

**To delete everything:** stop the daemon (and `vincent service uninstall` if
you installed the service), delete the config and data directories above, and
in each repository you used remove the task and chat branches (`vincent/*` by
default) and run `git worktree prune`.

## How tokens and configuration are protected

- On Linux and macOS vincent creates the data directory `0700` and the config
  directory `0700`. `token`, `config.yaml`, transcripts, trigger files, staged
  proposals and backup archives are written `0600`. Every daemon start
  re-tightens `token`, `config.yaml` and the config directory if their modes
  were widened, and `vincent doctor` reports a broad mode.
- On Windows file modes carry no access control; the per-user access control
  lists of `%APPDATA%` and `%LOCALAPPDATA%` protect the same files.
- **There is no secret redaction.** Transcripts and step records hold exactly
  what an agent or command printed and exactly the prompt or command it was
  given. A secret interpolated into a prompt, or printed by a tool, is on disk
  in the database and the transcript, and backups copy both. Values you put in
  `config.yaml`'s `environment.set` are stored as plain text. See the
  [security model](security-model.md#what-is-not-in-scope).

## Telemetry and network calls

vincent collects **no telemetry** and has **no account**. It sends no usage
data, crash reports or identifiers to anyone.

The daemon makes **two kinds of call on its own**, and each can be switched
off:

| Call | What it sends | Default | Switch off |
|---|---|---|---|
| Release check | One unauthenticated `GET` to GitHub for the latest vincent release. No token, no identifier | every 24 h | [`update.check: false`](reference/configuration.md#update) or `update.poll_interval: 0` |
| GitHub reconciler | For projects whose `origin` is on github.com only: reads that repository's issues and open pull requests through `gh` or `GITHUB_TOKEN`/`GH_TOKEN` | every 5 min | [`github.poll_interval: 0`](reference/configuration.md#github), or `github.enabled: false` for no GitHub call at all |

Everything else happens only because you, a workflow you wrote or a trigger you
armed asked for it:

- The agent CLIs a step or chat runs, and the commands in your workflows.
- Git operations on your repositories, including fetching a task's base branch
  from its remote before the task starts
  ([`fetch_base_branch`](reference/configuration.md#fetch_base_branch)).
- `vincent update`, which downloads and installs a release when you run it.
- GitHub writes — pushing a branch, opening, merging or commenting on a pull
  request — which happen only when you ask for them.
- Event trigger sources you configure: poll commands, GitHub issue and pull
  request polls, and schedules.
- [`notify.command`](reference/configuration.md#notify), a program of yours the
  daemon runs when a task changes state.

**The API is local.** The daemon's REST, event stream and MCP server listen on
a loopback address only, and every request except the health check needs the
bearer token from `{data_dir}/token`. An `http` trigger receives events on
that same loopback API, authenticated by the token and an HMAC signature; to
receive pushes from elsewhere you would have to forward them yourself.

## Contact

Questions about this policy can be asked in public on
[GitHub issues](https://github.com/lezli01/vincent/issues). For anything you
would rather not post in public, use
[a private security advisory](https://github.com/lezli01/vincent/security/advisories/new).
vincent is published by László Szabó.

When this policy changes, the **Last updated** date above changes with it, and
the change is listed in the [changelog](https://lezli01.is-a.dev/vincent/changelog.html).
