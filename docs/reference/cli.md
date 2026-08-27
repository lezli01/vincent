# CLI reference

One binary serves every role. `vincent` with no arguments opens the
[TUI](../guides/tui.md); the subcommands are thin clients over the same
localhost API.

- [Exit codes](#exit-codes)
- [Global behavior](#global-behavior)
- [`vincent`](#vincent)
- [`vincent version`](#vincent-version)
- [`vincent doctor`](#vincent-doctor)
- [`vincent daemon`](#vincent-daemon)
- [`vincent service`](#vincent-service)
- [`vincent project`](#vincent-project)
- [`vincent task`](#vincent-task)
- [`vincent status`](#vincent-status)
- [`vincent workflow`](#vincent-workflow)
- [`vincent github`](#vincent-github)
- [`vincent gc`](#vincent-gc)

---

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | The request was rejected — either the daemon answered no (bad id, invalid state transition), or one of the daemon-free commands refused it (`workflow validate` on an invalid file, `workflow init` on a name already taken) |
| `2` | No daemon answered |

`vincent daemon status` overloads them usefully: `0` healthy, `1` not running,
`2` running but unresponsive. `vincent doctor` follows the same shape: `0`
healthy, `1` problems found, `2` no daemon answered.

## Global behavior

- **`--json`** is available on every subcommand that prints anything. Empty
  results render as `[]`, never `null`. Advisory warnings go to stderr so stdout
  stays pipeable.
- **No subcommand auto-starts a daemon.** Only the TUI does. A subcommand that
  cannot reach one exits `2` with a pointer to `vincent daemon start` —
  except `vincent doctor`, which prints its whole report first, because the
  daemon being down is one of the things it is there to tell you.
- Clients discover the daemon by reading `{data_dir}/daemon.json` and then
  health-probing it, so a stale file from an unclean shutdown produces the same
  "no daemon" answer rather than a transport error later.
- `--help` works at every level.

---

## `vincent`

```sh
vincent
```

Opens the TUI, starting a daemon in the background if none is reachable.

## `vincent version`

```sh
vincent version
```

Prints one line: version, commit and build date. A binary built with the plain
`go build` toolchain falls back to `debug.ReadBuildInfo`, so the fields are never
empty.

## `vincent doctor`

```sh
vincent doctor [--json] [--fix [--force]]
```

One report answering "why is nothing running?". Eight groups:

| Group | Rows |
|---|---|
| Paths | config dir, data dir, config file, whether it parses, and any config path readable beyond its owner |
| Daemon | running / not running / unresponsive, pid, port, version, uptime |
| Log | daemon log path, size, mtime, and the last 20 lines |
| Database | path, size, total on disk including WAL/SHM, applied schema version, `PRAGMA integrity_check`, per-table row counts, workflow-snapshot bytes, and how far back the events table reaches |
| Agents | per adapter: found, path, version, and `logged_in` |
| GitHub | whether [`github.enabled`](configuration.md#github) is on, whether `gh` is installed and logged in, whether a token variable is set, and whether issues are readable |
| Storage | disk free under the data dir, worktree count and bytes, orphans |
| Tasks | counts by state, so "12 blocked" is visible without opening the board, plus any task whose state and step runs contradict each other |

Exit `0` when everything checked is healthy, `1` when problems were found (they
are listed under `PROBLEMS`), `2` when no daemon answered.

**Unhealthy is a closed set**: `config.yaml` exists and does not parse, the
daemon is alive but not answering, `integrity_check` is not `ok`, the database
is at a schema version newer than this binary understands, orphaned worktrees
are present, or a task is **unreconciled** — `queued` (or finished) while one of
its step runs is still marked `running`, which means crash recovery could not
close the previous attempt and admission will not run that task until it does
([Troubleshooting](../guides/troubleshooting.md#start-here-vincent-doctor)).
The **GitHub** rows never set the exit code either, and they say why: every "no"
they can report — the toggle off, `gh` missing, `gh` logged out, no token —
leaves task creation without an issue working exactly as before, so the row ends
with *tasks can still be created without an issue*. The token row names the
**variable** (`GITHUB_TOKEN` or `GH_TOKEN`), never its value: a diagnostic is
something people paste into issues.

A missing or logged-out agent CLI is reported and deliberately does
*not* set the exit code — most machines have one of three adapters installed,
and a doctor that exits `1` almost everywhere is no use in a script. Neither do
task *counts*: twelve blocked tasks is information, not a defect.

A `permissions` row names a config path whose mode grants group or other
access, the mode it should have, and the exact `chmod`. It is a warning: the
daemon tightens both paths on every start, so a row means no daemon has started
on this config or something widened it since — not a reason to exit `1`. There
are no such rows on Windows, where modes carry no access control.

The database rows **measure and change nothing**. `total on disk` is the file
plus its WAL and SHM sidecars, which is the honest figure — the store runs in WAL
mode, so the file alone understates the footprint between checkpoints. `rows`
lists every table in the schema, biggest first, so whichever one is growing is
the first thing you read; the set comes from the database itself, so a table a
later version adds appears without this command being taught about it.
`workflow snapshots` totals the per-task workflow YAML, the second growth driver
beside `events`. `oldest event` is how far back history reaches, which is what
makes a row count extrapolable. There is no threshold, no warning and no
retention window: rows are kept indefinitely, and `--fix` is the only thing that
touches the file at all.

**Without a daemon** the report is still printed in full — paths, whether the
config parses, adapter detection, the log tail, disk free and the worktree
count — and the database and task rows read `unknown — daemon not running`.
They are not read from a second process: only the daemon opens the database. The
byte figures, the row counts and the span are unknown together, for that reason
and no other.

`--json` emits the whole report for scripting and for pasting into a bug report.

### `vincent doctor --fix`

Reclaims orphaned directories and compacts the database. Both are writes, so the
daemon performs them: `--fix` without a running daemon is refused, and the
report is printed anyway.

- **An orphan** is an entry under a data root that no task row claims — the
  residue of a forced project delete or of a removal that failed partway. This
  is the same scan and the same removal [`vincent gc`](#vincent-gc) runs, so the
  two commands cannot disagree; `gc` is the one to reach for when reclaiming is
  the whole point. A non-directory there is reported and never removed.
- A worktree with **local changes** (untracked files included) is skipped unless
  you add `--force`. An orphan whose dirty check cannot run at all — the project
  repo is gone, so the directory is just files — counts as dirty for this
  purpose: nothing is deleted on the strength of a check that did not happen.
- Vincent does **not** run `git worktree prune` in your repositories. A stale
  registration can therefore survive there; the report says so and names the
  command.
- Compaction is a real `VACUUM`, and it is **skipped while any task is running
  or awaiting input** — the rewrite takes an exclusive lock, and stalling a step
  mid-write is worse than declining. The skip is reported with its reason.

```sh
vincent doctor --json | jq '.problems[]'
vincent doctor --fix --force
```

## `vincent daemon`

```sh
vincent daemon [--config-dir DIR] [--data-dir DIR] [--hide-console]
```

Runs the daemon **in the foreground**, logging to stderr as well as the log
file. This is what a service manager invokes.

| Flag | Effect |
|---|---|
| `--config-dir` | Pin the config directory (for a manager with no per-process environment) |
| `--data-dir` | Pin the data directory |
| `--hide-console` | Windows only: release the console the process was handed, when it owns it. Does nothing when run by hand in a terminal |

### `vincent daemon start`

```sh
vincent daemon start
```

Starts the daemon detached in the background and returns once it is answering.

### `vincent daemon stop`

```sh
vincent daemon stop [--force]
```

Graceful shutdown: admission stops, a `daemon.shutting_down` event is emitted,
running processes get 15 seconds to exit before being killed, and their step runs
are marked `interrupted` — the same resume path as a crash, so nothing is lost.

`--force` kills the process if the graceful stop fails.

### `vincent daemon status`

```sh
vincent daemon status [--json]
```

Reports whether the daemon is running, its identity, and which agent CLIs it
resolved. Exit `0` healthy, `1` not running, `2` unresponsive.

### `vincent daemon backup`

```sh
vincent daemon backup <path.tar.gz> [--json]
```

Writes one `.tar.gz` holding the database, every transcript, `config.yaml` and
the global workflows. The database copy is taken with SQLite's own
`VACUUM INTO`, so it is consistent even while tasks are running — unlike
copying `vincent.db`, which under WAL is missing whatever has not been
checkpointed.

**Needs a running daemon**, and exits `2` without one: only the daemon opens
the database, the same rule [`doctor --fix`](#vincent-doctor---fix) follows.
The destination must be a path that does not exist yet; vincent never
overwrites a backup.

Transcripts are included in full, so the archive is as large as your history
is. The command prints the bytes it wrote:

```
wrote /home/you/vincent-2026-08-25.tar.gz (1.4GB: database 8.2MB, transcripts 1.4GB)
```

What the archive does **not** carry, and why, is in
[Files](files.md#backup-and-restore).

### `vincent daemon restore`

```sh
vincent daemon restore <path.tar.gz> [--force] [--json]
```

Unpacks an archive into the config and data directories in effect. This is the
one command that touches the data directory directly rather than through the
API — the daemon it would overwrite has to be down for the restore to be safe.

Refused, exit `1`, when:

| Situation | Why |
|---|---|
| The daemon is running | Restore replaces the files it has open. Stop it first |
| The manifest's schema version is newer than this binary's | Migrations are up-only; a newer database cannot be stepped back |
| The destination already holds `vincent.db` (or a stray `-wal`/`-shm`), `transcripts/`, `config.yaml` or `workflows/` | Use `--force` |

`--force` **moves** each of those aside as `<name>.bak-<timestamp>` and
restores over the gap. Nothing is deleted on any path, and the command prints
where everything went.

Worktrees are not in a backup and are not restored; the branches they held are
in your repositories. A fresh API token is minted at next start, so every
client re-reads it.

## `vincent service`

Registers the daemon with the OS so it starts at login. Always as the invoking
user; no elevation needed on any platform. See
[Running at login](../guides/running-at-login.md).

```sh
vincent service install      # register and start; idempotent
vincent service uninstall    # stop and remove
vincent service status       # installed? running?
```

Backends: a launchd user agent (macOS), a systemd user unit (Linux), a Scheduled
Task (Windows).

## `vincent project`

### `vincent project add`

```sh
vincent project add <path> [--name NAME] [--default-branch BRANCH]
                           [--workflow NAME] [--max-parallel N] [--json]
```

Registers a local git repository.

| Flag | Default |
|---|---|
| `--name` | The directory name |
| `--default-branch` | Detected: `origin/HEAD`, then local `main`, then `master`, then the current branch |
| `--workflow` | None — tasks then name their own |
| `--max-parallel` | Unset — only the global cap applies |

Registration is refused if no default branch can be determined (a detached or
unborn HEAD); pass `--default-branch` explicitly.

### `vincent project ls`

```sh
vincent project ls [--json]
```

Lists registered projects with their ids, paths and defaults.

## `vincent task`

### `vincent task add`

```sh
vincent task add --project ID (--title TITLE | --github-issue N)
                 [--workflow NAME] [--description TEXT] [--base-branch BRANCH]
                 [--branch NAME] [--priority N] [--agent NAME] [--model M]
                 [--effort E] [--field NAME=VALUE]... [--json]
```

Creates a task. It is `queued` immediately — there is no draft state.

| Flag | Notes |
|---|---|
| `--project` | **Required** |
| `--title` | Required unless `--github-issue` supplies one; also the source of the branch slug |
| `--workflow` | Defaults to the project's default workflow |
| `--base-branch` | What the task branches **from**. Defaults to the project's default branch |
| `--branch` | What the task's branch is **called**. Used verbatim and wins over any template; defaults to the project's or the global [`branch_template`](configuration.md#branch_template) |
| `--priority` | Higher runs first; default 0 |
| `--field name=value` | Task field; repeat for more. Everything after the first `=` is the value, and a repeated name uses the last value |
| `--agent` / `--model` / `--effort` | The task-level override. It replaces workflow `defaults`, never an explicit step field |
| `--github-issue N` | Create the task from GitHub issue `N`. See below |

Declared workflow fields are validated by the daemon, while additional names
remain valid and are recorded on the same open field map:

```sh
vincent task add --project 1 --workflow release --title "Release 2.0" \
  --field ticket=OPS-42 --field owner=ana
```

Model and effort **only inherit from a level whose agent matches**, so switching
agent without setting them resets them to the new adapter's default rather than
leaking a claude alias onto a codex step.

A value no catalog knows is accepted with a warning on stderr (the CLI is the
final authority); a value belonging to a *different* adapter's catalog is
rejected with exit 1.

#### From a GitHub issue

```sh
vincent task add --project 1 --github-issue 200
```

```
task 61 created: GitHub integration: select a GitHub issue when creating a task (adhoc, branch vincent/61-github-integration)
  from lezli01/vincent#200: GitHub integration: select a GitHub issue when creating a task
```

The flag carries the **number and nothing else**. The daemon resolves the issue,
so the command line and the TUI's issue picker go through one implementation and
produce the same task from the same issue. It fills in the title (`#N ` and the
issue title), the description (the issue body plus a trailing
`GitHub issue #N: <url>` line), and any of the workflow's declared `issue`,
`labels`, `assignee` or `milestone` fields whose declared type accepts the value
— `issue` being the issue number.

**Every explicit flag wins over what the issue would have filled in**, so
`--title "Something else"` keeps your title and takes the rest from the issue.
`--title` is therefore optional here, and giving neither it nor `--github-issue`
is an error.

The issue is read once and stored on the task; editing it on GitHub afterwards
does not change what a later step renders. It needs the
[`github` integration](configuration.md#github) on, a github.com `origin`, and a
credential — run [`vincent github status`](#vincent-github-status) or
[`vincent doctor`](#vincent-doctor) if the daemon refuses.

### `vincent task ls`

```sh
vincent task ls [--project ID] [--state STATE] [--archived] [--limit N] [--json]
                [--include-children] [--parent ID]
```

Lists tasks. Archived tasks are excluded unless `--archived` is passed. The
table carries `ID`, `STATE`, `PROJECT`, `WORKFLOW`, `STEP` (the k/n cursor),
`BRANCH` and `TITLE`; `--json` adds the rest of the board fields, including the
cost and token totals rolled up across every attempt.

`BRANCH` is the task's own branch, which is how you find what vincent made once
[`branch_template`](configuration.md#branch_template) has moved branch names off
the `vincent/` prefix a glob would look for.

Fan-out lanes are excluded too: the list is the work you asked for, and a
64-task tree would bury it. `--parent ID` lists one fan-out task's lanes in
merge order, and `--include-children` lists everything flat.

Valid states: `queued`, `running`, `awaiting_gate`, `awaiting_input`,
`awaiting_children`, `blocked`, `paused`, `done`, `aborted`, `archived` — see
[Task lifecycle](task-lifecycle.md).

### `vincent task show`

```sh
vincent task show <id> [--json]
```

Shows one task with its step runs, the actions valid right now, and any pending
input request.

The step table's last two columns are different kinds of thing and should not be
read as one. `REASON` is vincent's own `failure_reason`, a closed set of
constants. `STATUS` is what the step said about *itself* through
[`vincent status`](#vincent-status) — free text, `-` when it said nothing, and
never the cause of a failure:

```
RUN  STEP       STATE      AGENT   REASON        STATUS
1    implement  succeeded  claude  -             wired the adapter
2    verify     failed     -       check_failed  3 tests red in internal/store
```

### `vincent task cancel`

```sh
vincent task cancel <id> [--json]
```

Aborts the task, killing any running process (graceful termination, then a kill
after 10 seconds). Valid from `queued`, `running`, `awaiting_input`,
`awaiting_gate`, `blocked` and `paused`; anything else exits 1 with the state it
actually found.

### `vincent task follow-up`

```sh
vincent task follow-up <id> (--prompt TEXT | --run CMD | --workflow NAME)
                            [--agent NAME] [--model M] [--effort E] [--json]
```

Runs one more piece of work in a **finished** task's existing worktree and
branch, before it is archived — recorded in that task's own ledger, with a step
run, a transcript and cost accounting. Valid from `done` and `aborted` only;
anything else exits 1 with the state it actually found.

Exactly one of the three run flags is required, and they are mutually exclusive:

| Flag | Runs |
|---|---|
| `--prompt` | an agent, with this text as its instructions |
| `--run` | a shell command, under the daemon's shell (`/bin/sh`, or `pwsh` on Windows) |
| `--workflow` | a workflow from the registry, against this task's worktree instead of a new one |

`--agent`, `--model` and `--effort` apply to this run and outrank the task's own
overrides and the workflow's `defaults:`; a value no catalog recognizes is a
warning on stderr, not a failure.

The command returns as soon as the run is queued — the scheduler admits it like
anything else. When it ends the task returns to the state it came from: `done`
to `done`, `aborted` to `aborted`, whatever the run did. A follow-up never
changes a task's verdict, and it is repeatable.

This is the one human action with a command line, and it has one because
batches want one:

```sh
for id in 41 42 43 44 45 46; do
  vincent task follow-up "$id" --run 'git rebase origin/main'
done
```

The remaining human actions — approve, reject, retry, repair, skip, pause,
resume, answer, archive — are TUI and API operations; see
[the API reference](api.md#tasks).

## `vincent status`

```sh
vincent status <message> [--json]
```

Records what the current step is doing, in its own words. It runs **from inside
a step** — an agent's shell tool, or a `command` step's script — and takes no
task or step argument: it reads `VINCENT_TASK_ID` and `VINCENT_STEP_ID` from
[the environment](../guides/workflows.md#the-vincent-environment) the daemon
sets on every agent and command step.

```sh
vincent status "running the store suite"
# … later in the same step
vincent status "3 tests red in internal/store"
```

The message has two readings and is one value. While the step runs it is the
live answer to "what is this doing", shown on the board's `STATUS` column and on
the attempt line in the [TUI](../guides/tui.md); the last value set before the
attempt ends stays on the finished attempt as the step's own account of how it
went.

Details worth knowing:

- **It is bounded, not validated.** The message is flattened to one line,
  stripped of control characters and truncated to 256 bytes. Being wordy never
  fails the command. An empty message clears the status.
- **It is silent on success.** Its stdout is the step's transcript, and a step
  that reports progress ten times should not add ten lines of vincent's own
  noise to the record it is summarizing. `--json` prints the stored value if you
  want it.
- **It only works while the step is running.** Afterwards the daemon answers
  `409` and the command exits 1 saying so, rather than dropping the message.
- **Nothing asks an agent to call it.** The daemon appends no instruction to a
  prompt, so an agent reports its status only when the workflow author asked it
  to — see
  [Reporting status from a step](../guides/workflows.md#56-reporting-status-from-a-step).
- Outside a step, with neither variable set, it exits 1 and says so. It never
  guesses a task.

The status is never a `failure_reason`: nothing renders it as the cause of a
failure, because a step killed on a timeout can be carrying a line it wrote
half an hour earlier.

## `vincent workflow`

Aliased as `vincent wf`.

### `vincent workflow init`

```sh
vincent workflow init <name> [--from <example>] [--project ID] [--json]
```

Writes a valid workflow file into the registry and prints the path. This is the
on-ramp: with the binary on your `$PATH` and nothing else — no daemon, no
checkout of this repository, no agent CLI installed — it gets you a file in the
right directory under the right name.

```
$ vincent workflow init release-notes
/home/you/.config/vincent/workflows/release-notes.yaml
Edit it, then `vincent workflow validate /home/you/.config/vincent/workflows/release-notes.yaml`.
The daemon picks it up on save.
```

| Flag | Effect |
|---|---|
| `--from <example>` | Start from a [shipped example](../../examples) instead of the skeleton: `converge`, `cursor-review`, `docs-update`, `feature-pr`, `fix-and-test`. They are embedded in the binary, so this works from any directory |
| `--project ID` | Write into that repository's `.vincent/workflows/` instead of `{config_dir}/workflows/`. **The one part that needs a daemon**, because only the daemon knows which projects exist and where they are |
| `--json` | The written path, name, scope, source example, and what it shadows |

**`<name>` is both the workflow's `name:` field and its file name**, so it is
held to `^[a-z0-9][a-z0-9._-]*$` — the same pattern the built-in
`create-workflow`'s `workflow_name` field uses, stricter than the schema's own
rule for a `name:`. With `--from`, only the file's
top-level `name:` line is rewritten; every comment is handed over untouched,
including a header comment that still names the example it came from.

**Collisions.** It refuses, without writing anything, if the target path already
exists, or if another file **in the same scope** already declares that `name:` —
one scope may not hold a name twice, and the loser by filename order would be
listed as invalid ([shadowing and duplicates](../guides/workflows.md#12-where-workflow-files-live)).
Shadowing a *lower* scope is legitimate and only warns: `--project` says when it
takes a name the global scope or a built-in holds, and the default scope says
when it takes a built-in's. It cannot warn in the other direction — a global
workflow may be shadowed later by a project file in any repository, and without
a daemon this command does not know which repositories exist.

Exit `0` written · `1` refused · `2` no daemon answered (`--project` only).

**`init` versus `create-workflow`.** `init` hands you a file; the built-in
[`create-workflow`](../guides/workflows.md#12-where-workflow-files-live) workflow
*designs* one for you from a description. `create-workflow` needs a running
daemon, a registered project, an installed and authenticated agent CLI, and a
task run that costs tokens and wall-clock time and may park in `awaiting_input`
waiting on a design question. `init` is offline, free, instant and always the
same file. Reach for `init` when you know roughly what you want to write, and
for `create-workflow` when you would rather describe the outcome.

### `vincent workflow ls`

```sh
vincent workflow ls [--project ID] [--json]
```

Lists the merged registry — built-in plus global, with scope badges and
validation status. **Add `--project` to include that repository's
`.vincent/workflows/`**, with shadowing applied; without it you see global scope
only.

The `PLATFORMS` column is the workflow's
[platform restriction](workflow-schema.md#platforms); status `unsupported`
means this host is not in it, so the workflow is listed but cannot back a task
here.

Needs a daemon: only the daemon knows which projects exist.

### `vincent workflow validate`

```sh
vincent workflow validate <file> [--json]
```

Validates a workflow file. **It needs no daemon** — no network, no agent CLI
installed — which makes it usable from a pre-commit hook or a CI job.
([`vincent workflow init`](#vincent-workflow-init) is daemon-free too, except for
`--project`; [`vincent daemon restore`](#vincent-daemon-restore) also runs
without one, but it *refuses* to run while a daemon is up rather than merely
tolerating its absence.)

Exit `0` valid, `1` invalid. Warnings (a model in no catalog) print but do not
fail the command.

## `vincent github`

Read-only views of a project's GitHub issues. Nothing under this command writes
to GitHub, and the daemon makes every call — a client never talks to GitHub.
Both subcommands need a daemon.

### `vincent github issues`

```sh
vincent github issues --project ID [--state open|closed|all] [--limit N] [--json]
```

Lists the project's issues, newest first. Pull requests are never included.

```
ISSUE  STATE  TITLE                                                     LABELS       ASSIGNEE
#200   open   GitHub integration: select a GitHub issue when creating…  enhancement  -
#199   open   Let a step report a custom status message                 enhancement  -
```

`--state` defaults to `open`, `--limit` to the daemon's own bound. Filter the
output yourself — there is no `--query`.

### `vincent github status`

```sh
vincent github status --project ID [--json]
```

Whether *this* project's issues can be read, and if not, why.

```
CHECK    VALUE
enabled  yes
repo     lezli01/vincent
issues   readable via gh
```

It is the per-project half of [`vincent doctor`](#vincent-doctor)'s GitHub rows:
doctor answers "can this machine read GitHub at all", this answers "and is this
project one it would read". A project whose `origin` is not a github.com URL
reports `unavailable: this project's origin remote is not a github.com
repository` — which is not a fault, just a project the issue picker does not
apply to.

## `vincent gc`

```sh
vincent gc [--dry-run] [--force] [--json]
```

Reclaims directories under the data dir that **no task claims**. Two things
produce one: deleting a project whose worktree removal failed (the task rows go
regardless, so nothing can name the directory again), and a crash between
creating a worktree and recording its path.

```
KIND       PATH                                  SIZE     STATUS
worktree   ~/.local/share/vincent/worktrees/41   12.4MB   removed
worktree   ~/.local/share/vincent/worktrees/58   3.1MB    skipped: dirty_unknown
transcript ~/.local/share/vincent/transcripts/41 88.2KB   removed
reclaimed 2 of 3 orphan(s), 12.5MB freed
```

| Flag | Effect |
|---|---|
| `--dry-run` | Prints the identical report and removes nothing |
| `--force` | Also removes worktrees that are dirty or that git cannot judge |
| `--json` | The raw report, including per-entry `skip_reason` and `error` |

**Skip reasons.** A worktree with local changes — untracked files included, the
same rule `git worktree remove` uses — is `worktree_dirty`. One whose repository
has been deleted, or in which `git worktree prune` has run, is `dirty_unknown`:
`git status` fails there, so nobody can say what is inside. That is the *common*
case for a real orphan, so expect a plain `vincent gc` to skip most of what it
lists and to need `--force` once you have looked at the paths. A file sitting
directly under a data root is `not_a_directory` and is never removed.

**What it never does.** It never deletes a branch, never touches a directory any
task row claims, never removes anything outside `{data_dir}/worktrees` and
`{data_dir}/transcripts`, and never modifies a task row. A task pointing at a
worktree that is gone is *reported* at the end of the output and left alone —
recover that one with a retry, which recreates the worktree from the branch.

An entry that could not be removed (a file locked by another process, a
permissions problem) is reported on its own line and the run continues; the
reclaimed totals count only what actually went.

The daemon reports the same orphans at startup — one warning per directory in
`daemon.log`, plus a count on `GET /v1/info` and in the TUI daemon view — but it
never deletes anything by itself.

---

## See also

- [Scripting vincent](../guides/scripting.md) — patterns built on these commands.
- [HTTP API](api.md) — what every subcommand calls.
- [Configuration](configuration.md).
