# Files and directories

Vincent keeps two directories: **config** (things you edit) and **data** (things
it owns). Both are platform-native, and both can be overridden.

- [Where they are](#where-they-are)
- [The config directory](#the-config-directory)
- [The data directory](#the-data-directory)
- [Worktrees and branches](#worktrees-and-branches)
- [Transcripts](#transcripts)
- [Overriding the locations](#overriding-the-locations)
- [What is safe to delete](#what-is-safe-to-delete)
- [Backup and restore](#backup-and-restore)

---

## Where they are

| Purpose | Linux | macOS | Windows |
|---|---|---|---|
| Config | `~/.config/vincent/` | `~/Library/Application Support/vincent/` | `%APPDATA%\vincent\` |
| Data | `~/.local/share/vincent/` | `~/Library/Application Support/vincent/data/` | `%LOCALAPPDATA%\vincent\` |

On Linux, `XDG_CONFIG_HOME` and `XDG_DATA_HOME` are honored. macOS is the one
platform where data nests inside config's directory.

## The config directory

```
{config_dir}/          # created 0700
  config.yaml          # daemon configuration — you or a client edit this, created 0600
  workflows/*.yaml     # global workflows, available to every project
```

Both are watched. Editing `config.yaml` hot-reloads valid changes; saving a
workflow file reloads the registry. Neither needs a restart.

`config.yaml` is also written by the daemon, on
[`PATCH /v1/config`](api.md#daemon) — what
[`vincent config set`](cli.md#vincent-config) and the TUI's daemon view call. It
edits the key in place, so your comments and key order survive, and replaces the
file atomically at `0600`. A hand-edit racing a client edit is last-writer-wins.

**The directory and `config.yaml` are owner-only**, because
[`environment.set`](configuration.md#environment) values are literal and people
put tokens in them. On POSIX every daemon start drops group and other access
from both — the same re-tightening the `token` file gets — and logs the path
and the mode it found; `vincent doctor` reports the same as a warning with the
exact `chmod`. That pass touches the modes only — it never rewrites your file's
*contents* — and if you widened those modes on purpose, the next start will
narrow them again. On Windows the modes carry no access control and the per-user
ACL of `%APPDATA%` applies instead.

Everything here is yours: it is safe to put under version control, sync between
machines, or hand-edit — with one caveat from the modes above: a literal
[`environment.set`](configuration.md#environment) value travels with the file,
so prefer inheriting the name. Nothing in this directory is generated after
first start — the modes above are the one thing a later start touches, and it
touches no content.

Project-scoped workflows live in the repository instead, at
`.vincent/workflows/*.yaml`, and shadow a global file of the same name.

## The data directory

```
{data_dir}/
  vincent.db                                        # SQLite, WAL mode
  token                                             # API bearer token, 0600
  daemon.json                                       # { port, pid, started_at }
  daemon.lock                                       # single-instance lock
  tui.json                                          # TUI-local view state
  logs/daemon.log                                   # rotated, size-capped
  worktrees/{task_id}/                              # one git worktree per task
  worktrees/chat-{chat_id}/                         # one git worktree per chat
  transcripts/{task_id}/{step_index}-{attempt}.jsonl
  transcripts/chat-{chat_id}/{turn_seq}.jsonl
```

| File | What it is |
|---|---|
| `vincent.db` | Every project, task, step run and durable event. **Only the daemon opens it** — one writer, WAL mode, a single connection, which is what makes `SQLITE_BUSY` impossible in-process. Migrations are embedded and applied in a transaction at startup |
| `token` | The API bearer token, created `0600` at first start. On Windows it relies on the per-user ACL of `%LOCALAPPDATA%`. Anyone who can read it can drive your daemon |
| `daemon.json` | How clients find the daemon: port, pid, start time. Written atomically, removed on graceful shutdown |
| `daemon.lock` | Single-instance enforcement; releases automatically when the process dies |
| `tui.json` | TUI-local view state: the first-run full-auto acknowledgment, and the board's collapsed groups. Written by the TUI, never read by the daemon; deleting it re-shows the full-auto notice and opens every group |
| `logs/daemon.log` | The daemon log, rotated and size-capped. Read by `vincent daemon logs` and the TUI's daemon view — from disk in both cases, so it still works when the daemon is what died |

## Worktrees and branches

Each task gets a git worktree at `{data_dir}/worktrees/{task_id}/`, checked out
on a branch named:

```
vincent/{id}-{slug}
```

where `slug` comes from the task title. A title that sanitizes to nothing gives
`vincent/{id}` with no trailing dash.

That is the **default**. The name is configurable — a template per project or in
`config.yaml`, or a literal for one task — see
[Configuration](configuration.md#branch_template).

A [chat](cli.md#vincent-chat) gets one on exactly the same terms, at
`{data_dir}/worktrees/chat-{chat_id}/` on its own `vincent/{id}-{slug}` branch.
The directory is prefixed because tasks and chats share one root and are
numbered independently — without it, chat 7 and task 7 would claim one path.
The name is informational: `vincent gc` decides what is a stray from the
database claims, which cover both tables, so a chat's worktree is never
mistaken for an orphan.

Two rules worth internalizing:

- **The worktree is disposable.** Archiving a task removes it. If it has
  uncommitted changes, archiving refuses unless forced — that work would be lost.
- **The branch is not — as long as it holds a commit.** Vincent never deletes a
  branch that has commits past the base it was cut from, so everything an agent
  actually wrote accumulates in your repository until you remove it. The one
  branch archiving does delete is one that received *no* commit at all, which is
  what a workflow that never writes to the repository leaves behind
  ([`delete_empty_branch_on_archive`](configuration.md#delete_empty_branch_on_archive)).
  The daemon is the reliable list, since a configured name need not start with
  `vincent/`:

  ```sh
  vincent task ls --archived      # the branch column
  git worktree list
  ```

Your own checkout is never touched: vincent reads the repository to create
worktrees and never modifies your working tree, current branch or stash.

**What reclaims a worktree.** Archiving the task, normally. A worktree whose task
row is gone — a deleted project whose removal failed, a crash before the path was
recorded — is nobody's, and nothing archives it; that is what
[`vincent gc`](cli.md#vincent-gc) is for. The daemon reports those at startup and
counts them on the daemon view, but it never deletes one on its own.

## Transcripts

```
{data_dir}/transcripts/{task_id}/{step_index}-{attempt}.jsonl
{data_dir}/transcripts/chat-{chat_id}/{turn_seq}.jsonl
```

One file per **attempt** — or, for a chat, per **turn** — JSONL. It contains
the agent's own event stream verbatim — lossless and replayable — interleaved with vincent's namespaced
`vincent.*` annotation lines.

Two consequences of storing the raw stream rather than a parsed one:

- **Normalization happens on read.** Improving a parser improves transcripts
  already on disk: reasoning recorded in a run from last week renders today.
- **Unknown event types are kept.** A line the parser does not recognize is
  transcripted anyway and surfaced in the TUI's verbose output mode.

Transcripts are bounded by `transcript_max_bytes` per attempt and pruned for
**archived** tasks past `transcript_retention_days` — see
[Configuration](configuration.md).

Both of those are **task-scoped**. A chat's turn transcripts are written with no
size cap and are not pruned when the chat is archived; they stay until you
remove them.

**What reclaims a transcript.** Retention, for as long as the task row exists.
Deleting a project deletes its task rows, and retention walks rows — so those
directories are reached by no retention pass, ever, and are reclaimed by
[`vincent gc`](cli.md#vincent-gc) instead.

They contain the rendered prompt and everything the agent did. **Read one before
pasting it into an issue.**

## Overriding the locations

```sh
VINCENT_CONFIG_DIR=/tmp/v-cfg VINCENT_DATA_DIR=/tmp/v-data vincent daemon start
```

Both override their directory outright. This is how the test suite isolates
state, and it is the clean way to run a second throwaway instance beside your
real one.

For a foreground daemon started by a manager with no per-process environment,
the same values are available as flags:

```sh
vincent daemon --config-dir /srv/v-cfg --data-dir /srv/v-data
```

> **A service captures these at install time.** A service does not inherit the
> shell that installed it, so `vincent service install` writes the directories in
> effect into the unit. If you change them, reinstall — otherwise your CLI and
> your service use different databases and each sees a world the other does not.

## What is safe to delete

| Path | Deleting it means |
|---|---|
| `logs/daemon.log` | Nothing; it is recreated |
| `transcripts/{task_id}/`, `transcripts/chat-{chat_id}/` | That task's or chat's output history is gone; the record and its metrics stay |
| `worktrees/{task_id}/` | Effectively an unregistered archive — prefer archiving the task, which does it properly. For a directory whose task no longer exists, prefer `vincent gc`, which checks it is not somebody's live worktree first |
| `daemon.json`, `daemon.lock` | Only safe while the daemon is stopped; both are recreated |
| `token` | Recreated at next start, and every existing client must re-read it |
| `vincent.db` | **Everything is gone** — projects, tasks, history. Branches in your repositories survive. This is the row [Backup and restore](#backup-and-restore) exists for |
| `{config_dir}/` | Your config and global workflows; defaults are rewritten at next start |

## Backup and restore

```sh
vincent daemon backup ~/vincent-2026-08-25.tar.gz     # needs a running daemon
vincent daemon restore ~/vincent-2026-08-25.tar.gz    # needs a stopped one
```

One `.tar.gz`, four things in it:

| Entry | What it is |
|---|---|
| `vincent.db` | A consistent copy of the database, taken with SQLite's own `VACUUM INTO` |
| `transcripts/` | `{data_dir}/transcripts/`, in full |
| `config/config.yaml`, `config/workflows/` | Your config and global workflows |
| `manifest.json` | The vincent version, the schema version, and when it was taken |

**Do not copy `vincent.db` by hand while the daemon runs.** Under WAL a
committed row lives in `vincent.db-wal` until a checkpoint, so a copy of
`vincent.db` alone is missing recent work, and copying the three files
separately gives a set that can restore into a torn database. The backup
command exists because the obvious thing is the wrong thing. If you have no
vincent binary to hand, the honest fallback is to **stop the daemon first**,
then copy `vincent.db`, `vincent.db-wal` and `vincent.db-shm` together.

**Backup needs a running daemon and refuses without one.** Only the daemon
opens the database, so only the daemon can copy it — the same rule
[`vincent doctor --fix`](cli.md#vincent-doctor---fix) follows. It does not
need a *quiet* daemon: `VACUUM INTO` writes elsewhere under a read
transaction, so a backup may be taken while tasks are running.

**The archive is as large as your history is.** Transcripts are included in
full and are not opt-out; the per-attempt cap alone is 512 MB
([`transcript_max_bytes`](configuration.md)). The command prints the bytes it
wrote, broken down by database and transcripts, rather than pretending the
artifact is small.

**Not in the archive, and recoverable without it:** `worktrees/` (disposable —
the branches they held are in your repositories), `token`, `daemon.json`,
`daemon.lock`, `logs/` and `tui.json`. A restored installation mints a **fresh
API token** at next start, so every client re-reads it.

**Restore is the only vincent command that touches the data directory
directly**, because the daemon it would overwrite has to be down for it to be
safe. It refuses:

- while a daemon is running — stop it first;
- when the manifest's schema version is newer than the binary you are running,
  since migrations are up-only and cannot be stepped back;
- when the destination already holds `vincent.db` (or a leftover `-wal`/`-shm`),
  `transcripts/`, `config.yaml` or `workflows/` — unless `--force`.

With `--force` each of those is **moved aside** as `<name>.bak-<timestamp>`.
Nothing is deleted on any path, and the command prints where everything went.

To move an installation to another machine, restore into an empty pair of
directories and start the daemon; your repositories and their branches are the
other half, and vincent recreates worktrees as tasks need them.

To remove vincent entirely: `vincent service uninstall`, `vincent daemon stop`,
delete the binary, delete both directories, then clean up any branches left in
the repositories you used — the ones that carry commits, since the empty ones
went when their tasks were archived. Take the list from `vincent task ls
--archived` **before** deleting the database: a configured branch name need not
start with `vincent/`.

---

## See also

- [Configuration](configuration.md) · [CLI](cli.md)
- [Windows](../platforms/windows.md) · [macOS](../platforms/macos.md) ·
  [Linux](../platforms/linux.md)
