# CLI reference

One binary serves every role. `vincent` with no arguments opens the
[TUI](../guides/tui.md); the subcommands are thin clients over the same
localhost API.

- [Exit codes](#exit-codes)
- [Global behavior](#global-behavior)
- [`vincent`](#vincent)
- [`vincent version`](#vincent-version)
- [`vincent doctor`](#vincent-doctor)
- [`vincent agents`](#vincent-agents)
- [`vincent daemon`](#vincent-daemon)
- [`vincent service`](#vincent-service)
- [`vincent project`](#vincent-project)
- [`vincent task`](#vincent-task)
- [`vincent config`](#vincent-config)
- [`vincent status`](#vincent-status)
- [`vincent statusline`](#vincent-statusline)
- [`vincent update`](#vincent-update)
- [`vincent skills`](#vincent-skills)
- [`vincent chat`](#vincent-chat)
- [`vincent workflow`](#vincent-workflow)
- [`vincent trigger`](#vincent-trigger)
- [`vincent github`](#vincent-github)
- [`vincent gc`](#vincent-gc)

---

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | The request was rejected — the daemon answered no (bad id, invalid state transition), a daemon-free command refused it (`workflow validate` on an invalid file, `workflow render` on a template that does not execute, `workflow init` on a name already taken, `trigger validate` on an invalid file, `trigger ls` that matched no file, `trigger apply` on a proposal it refuses, `workflow ls --global` that found no file, `workflow apply` on a proposal it refuses), or the client refused the input before sending anything (a `--fields-file` that is not one JSON object of strings, a `trigger test --event` file that is not one JSON object, a `github pr link` number that is not a positive integer, a `github pr unlink` on a task with no live link, a `project edit` that names no field or whose `--max-parallel` is not a whole number) |
| `2` | No daemon answered |

`vincent daemon status` overloads them usefully: `0` healthy, `1` not running,
`2` running but unresponsive. `vincent doctor` follows the same shape: `0`
healthy, `1` problems found, `2` no daemon answered. So does
[`vincent update`](#vincent-update) — see its own table.
[`vincent github pr show`](#vincent-github-pr-show) and
[`vincent github pr checks`](#vincent-github-pr-checks) set their own for a
different reason: the routes behind them answer `200` whatever they found, so
`1` there means the task has no linked pull request or GitHub could not be
read, not that the request was malformed.

## Global behavior

- **`--json`** is available on every subcommand that prints anything. Empty
  results render as `[]`, never `null`. Advisory warnings go to stderr so stdout
  stays pipeable.
- **No subcommand auto-starts a daemon.** Only the TUI does. A subcommand that
  cannot reach one exits `2` with a pointer to `vincent daemon start` —
  except `vincent doctor`, which prints its whole report first, because the
  daemon being down is one of the things it is there to tell you.
- **Colour only on a terminal.** [`vincent task diff`](#vincent-task-diff) is
  the one subcommand that colours its output, and only when stdout is a terminal
  and `NO_COLOR` is unset (`TERM=dumb` turns it off too). Piped or redirected,
  output is exactly the bytes the daemon served. There is no `--color` flag.
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

### What only the TUI does

Everything else the TUI does has a subcommand. These are the exceptions,
and a test fails when the TUI starts calling the API for something that is
neither a subcommand nor on this list:

| In the TUI | From a shell |
|---|---|
| Creating and editing a workflow in the workflows view | `vincent workflow init`, your `$EDITOR`, then `vincent workflow validate` |
| A task's **Workflow** tab | No subcommand; `vincent workflow render` previews a workflow file |
| The triggers view: its list, a trigger's delivery ledger, a dry poll, and creating, editing, arming or deleting a trigger | `vincent trigger ls`, `validate`, `test` and `apply`. Arming is done in the TUI or in `$EDITOR`, never by `apply` |
| The agent / model / effort preview in the new-task form and the workflows view, and the new-task form's branch-name preview | `vincent workflow render`, which resolves the same triple the same way. No subcommand previews the branch name a task would get; `vincent task show` prints it once the task exists |
| The daemon summary on the board header and daemon view | `vincent daemon status`, `vincent agents`, `vincent doctor` and `vincent config get` between them. The daemon-wide count of slots in use right now, with its lanes and on-input breakdown, is shown only in the TUI; `vincent project ls --json` carries each project's `slots_used` |
| Live updates as they happen | Subcommands poll: `vincent task transcript -f`, `vincent chat transcript -f`, and `vincent chat send` waits for the answer |

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

One report answering "why is nothing running?". Twelve groups:

| Group | Rows |
|---|---|
| Paths | config dir, data dir, config file, whether it parses, and any config path readable beyond its owner |
| Daemon | running / not running / unresponsive, pid, port, version, uptime |
| Log | daemon log path, size, mtime, and the last 20 lines |
| Database | path, size, total on disk including WAL/SHM, applied schema version, `PRAGMA integrity_check`, per-table row counts, workflow-snapshot bytes, and how far back the events table reaches |
| Agents | per adapter: found, path, version, `logged_in`, whether the build is one vincent has been tested against, and whether the adapter can restrict on this OS. Quota is not here — it needs a daemon this report does not; see [`vincent agents`](#vincent-agents) |
| GitHub | whether [`github.enabled`](configuration.md#github) is on, whether `gh` is installed and logged in, whether a token variable is set, and whether issues are readable |
| Container | whether [`container.image`](configuration.md#container) names an image, which image, whether the configured runtime answered, and whether steps run in it or on this host |
| Skills | per [published skill](#vincent-skills): the version this binary ships, the state of the copy in the global skills store, and the agents it is linked into |
| Update | whether [`update.check`](configuration.md#update) is on, the latest stable release and when it was last seen, this binary's version, and whether the running daemon is older than it |
| Backup | whether [scheduled backups](configuration.md#backup) are on, the directory, interval and keep, the last success and last attempt, when the next run is due, the last archive's size, how many scheduled archives are kept, and the last error |
| Storage | disk free under the data dir, worktree count and bytes, orphans |
| Tasks | counts by state, so "12 blocked" is visible without opening the board, plus any task whose state and step runs contradict each other |

Exit `0` when everything checked is healthy, `1` when problems were found (they
are listed under `PROBLEMS`), `2` when no daemon answered.

**Unhealthy is a closed set**: `config.yaml` exists and does not parse, the
daemon is alive but not answering, `integrity_check` is not `ok`, the database
is at a schema version newer than this binary understands, orphaned worktrees
are present, scheduled backups are on and the last attempt failed, or a task is
**unreconciled** — `queued` (or finished) while one of
its step runs is still marked `running`, which means crash recovery could not
close the previous attempt and admission will not run that task until it does
(a `fan_out` step's own row is excluded: a parent waiting on its lanes holds
one by design)
([Troubleshooting](../guides/troubleshooting.md#start-here-vincent-doctor)).
The **Update** rows never set it either: a newer release and a daemon still
running the previous build both leave everything working, so both are stated as
facts with the command that acts on them — never as problems.
The **GitHub** rows never set the exit code either, and they say why: every "no"
they can report — the toggle off, `gh` missing, `gh` logged out, no token —
leaves task creation without an issue working exactly as before, so the row ends
with *tasks can still be created without an issue*. The token row names the
**variable** (`GITHUB_TOKEN` or `GH_TOKEN`), never its value: a diagnostic is
something people paste into issues.
The **Container** rows follow the same rule and for the same reason:
containerization is off by default, so a machine with no runtime — or a Windows
daemon, which cannot host one — runs every step on the host exactly as it always
has. The runtime is probed **even when `container.image` is unset**, because
"would this work if I turned it on" is the question the group exists to answer.
So do the **Skills** rows: a missing, stale or unreadable skill costs you help
in your own agent session and stops no vincent run, because the built-in
workflows that use a skill (`create-workflow`, `update-workflows`,
`create-trigger` and `update-triggers`) carry its text in their own prompts. The group ends with `run: vincent skills install` when
anything is not current.

The **Backup** group is the exception to that rule. When
[`backup.interval`](configuration.md#backup) is set and the last scheduled
backup failed, `PROBLEMS` lists `the last scheduled backup failed: <error>` and
the command exits `1` until a later attempt succeeds. The other rows report
defaults nobody chose. This one reports a feature you turned on, and a backup
that fails without anyone noticing is found out on the day it is needed. With
backups off the group only shows the settings and never sets the exit code. A
backup that is merely overdue, for example because the daemon was stopped, is
not a problem, and neither is a failure to delete old archives after a
successful run, which the group shows as a prune error.

An adapter row also ends with what vincent knows about the build itself:
`untested version` and the builds it was judged against, `incompatible version`
for a build known to break, and `no restricted mode on <os>` where the adapter
cannot honour `permission_mode: restricted` here (cursor on Windows). `untested`
is the normal answer for anyone on a current CLI and is not a defect — agent
CLIs ship far more often than vincent does. A `tested` build prints nothing:
one line of good news per adapter would bury the one warning.

A missing or logged-out agent CLI — or an untested, incompatible or
cannot-restrict one — is reported and deliberately does
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
config parses, adapter detection, published-skill state, the log tail, disk free
and the worktree count — and the database and task rows read
`unknown — daemon not running`. The Backup group shows its settings from
`config.yaml`, but its last attempt is unknown and it raises no problem: that
state lives in the daemon.
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

## `vincent agents`

```sh
vincent agents [--json] [--refresh]
```

One row per agent adapter, in the daemon's registration order: what the TUI's
daemon view shows under *adapters*, for a script or a shell. It is a thin client
of [`GET /v1/agents`](api.md#daemon) and needs a running daemon — with none it exits
`2`.

```
AGENT   VERSION             BUILD     LOGIN          QUOTA                                                                                                NOTES
claude  2.1.226             tested    ok             claude status line · 5h 28.5% → 2026-09-16T14:40:00+02:00 · 7d 62% · read 2026-09-16T11:40:02+02:00
codex   -                   -         -              spent → 2026-09-16T13:40:00+02:00                                                                    not found: …; no mid-run input
cursor  2026.09.02-1c4f7a0  untested  NOT LOGGED IN  unknown                                                                                              no mid-run input; no skill listing
```

| Column | Shows |
|---|---|
| `VERSION` | The installed CLI's version, `-` when none was found |
| `BUILD` | `tested`, `untested` or `incompatible` — whether vincent has been tested against this build. `-` when there is nothing installed to judge |
| `LOGIN` | `ok`, `NOT LOGGED IN` or `unknown`, the words [`vincent doctor`](#vincent-doctor) uses. `unknown` is a probe that could not tell — it timed out, or the CLI predates the command — never a no. `-` for an adapter that is not installed |
| `QUOTA` | The adapter's usage window, below |
| `NOTES` | Bad news only: `not found: <why>`, `no mid-run input` (an `on_input: require` step cannot use it), `no restricted mode on <os>`, `no skill listing` (the adapter cannot list the skills its CLI would load; a daemon that predates the field adds nothing), `option probe failed (curated catalog)`. A healthy adapter's cell is empty |

`QUOTA` is the one block the daemon serves, labelled by where it came from.
Nothing is merged on the client: when a source has reported a reading, that is
what the daemon serves, and the last usage-limit stop it watched is the
fallback ([Agent CLIs](../guides/agents.md#how-much-quota-is-left-and-who-will-say)).

| Cell | Meaning |
|---|---|
| `unknown` | No reading and no recorded stop. Not the same as fine |
| `<source> · <window> <pct> → <reset> · … · read <time>` | A reading: `codex app-server`, `claude status line`, or an unrecognised source as it arrived; every window it named; and when it was taken. A window that named no reset shows no time |
| `spent → <reset>` | A usage-limit stop whose window is still shut, with the reset the CLI stated |
| `spent ≈ <reset>` | The same, with a reset vincent estimated from [`usage_limit_recheck_interval`](configuration.md#usage_limit_recheck_interval) because the CLI named none |
| `ok · last spent <time>` | A stop whose window has since reopened |

Times are local RFC3339: a seven-day window's reset is days away, and a bare
clock would not say which day.

**Exit `0` whenever the daemon answered**, whatever the adapters' health. A
missing, logged-out, untested or quota-spent adapter is the normal state of some
adapter on most machines, and an exit code that fired on it would be no use in a
script. An API error is `1`.

By default the answer comes from the daemon's catalog cache, which re-probes an
adapter on its own when its binary changes. `--refresh` forces a fresh probe of
every adapter first — the TUI new-task view's `R` — and waits for it.

`--json` emits the endpoint's `agents` array unchanged, with every field the
table leaves out: models and efforts, `supports_resume`, the skill fields
(`supports_skill_listing`, `skill_sigil`, `skill_position`), the tested builds
and the raw verdicts.

```sh
vincent agents --json | jq -r '.[] | select(.input_verdict == "supported") | .name'
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
vincent daemon status
```

Reports whether the daemon is running and its identity: pid, port, version and
start time. Exit `0` healthy, `1` not running, `2` unresponsive. Which agent
CLIs the daemon resolved is [`vincent agents`](#vincent-agents).

If the binary you just ran is newer than the daemon that answered — which is
what you have right after [`vincent update`](#vincent-update) — it says so and
tells you to restart it. That is a line, not a failure: the running daemon keeps
its old code until it is restarted, and everything keeps working meanwhile.

### `vincent daemon logs`

```sh
vincent daemon logs [-n | --lines N] [-f | --follow]
```

Prints the tail of `{data_dir}/logs/daemon.log` — 500 lines by default
(`-n`/`--lines`), the same window the TUI's daemon view shows. `-f`
(`--follow`) keeps printing lines as they are appended, on a two-second
cadence, until Ctrl-C.

It reads the file **from disk and never contacts the daemon**, which is the
point rather than a shortcut: the log is worth reading exactly when the daemon
is not there to serve it. So this is one of the few subcommands that can never
exit `2` — no daemon is needed and none is started. A log file that is not there
is an error naming the path, exit `1`; a log with nothing in it prints nothing
and exits `0`.

The data directory is resolved the way every subcommand resolves it —
`VINCENT_DATA_DIR`, else the platform default. There is no `--data-dir` flag
here; the one on `vincent daemon` itself exists for the Windows Scheduled Task,
whose action carries no environment.

Following is rotation-safe: each poll opens, reads and closes the file, because
the daemon rotates by renaming it and a follower holding a handle would break
that rotation on Windows. A rotation mid-follow is waited out and the fresh file
picked up.

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
[Files](files.md#backup-and-restore). To have the daemon take the same archive
on a schedule, set [`backup.interval`](configuration.md#backup); see
[Scheduled backups](files.md#scheduled-backups).

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
| `--default-branch` | Detected **once, here**: `origin/HEAD`, then local `main`, then `master`, then the current branch. The name is stored and never re-detected; what is refreshed later is the branch's content, per task, when `fetch_base_branch` is on |
| `--workflow` | None — a task that names no workflow runs `adhoc` |
| `--max-parallel` | Unset — only the global cap applies |

Registration is refused if no default branch can be determined (a detached or
unborn HEAD); pass `--default-branch` explicitly.

### `vincent project ls`

```sh
vincent project ls [--json]
```

Lists registered projects with their ids, paths and defaults.

### `vincent project edit`

```sh
vincent project edit <id> [--name NAME] [--path PATH] [--default-branch BRANCH]
                          [--workflow NAME] [--max-parallel N] [--branch-template TMPL]
                          [--json]
```

Changes a registered project's settings. Only the fields whose flags you pass
are sent; every other field keeps its value.

| Flag | Changes | Empty value |
|---|---|---|
| `--name` | The project name | Refused by the daemon |
| `--path` | The repository the project points at. Sent as typed, so it must be absolute, and it passes the same checks as `project add` | Refused by the daemon |
| `--default-branch` | The branch new tasks start from. It must exist as a local branch | Refused by the daemon |
| `--workflow` | The default workflow for new tasks | Clears it: tasks fall back to `adhoc` |
| `--max-parallel` | This project's concurrency cap, a whole number of at least `1` | Clears it: only the global cap applies |
| `--branch-template` | This project's [`branch_template`](configuration.md#branch_template) | Clears it: the project uses the global one again |

A value of only whitespace counts as empty. `--max-parallel` refuses anything
that is not a whole number before sending anything; `0` or a negative number is
sent, and the daemon refuses it.

```sh
vincent project edit 1 --max-parallel 2 --branch-template 'feat/{{.Slug}}'
vincent project edit 1 --workflow "" --max-parallel ""    # clear both
vincent project edit 1 --path /new/checkout --default-branch main
```

When `--path` points at a repository that lacks the stored default branch, the
daemon refuses the change. Pass `--default-branch` in the same command, as in
the last example.

With no field flag, the command exits `1` without contacting the daemon, even
when none is running. It never starts a daemon. A refusal from the daemon, such
as a name already in use or a missing branch, is printed as the daemon wrote it
and exits `1`. `--json` prints the updated project.

### `vincent project rm`

```sh
vincent project rm <id> [--force] [--json]
```

Removes the project registration and its task rows. It never prompts: `--force`
is the whole confirmation story, because this tree exists to be scripted.

Two refusals reach you intact, and they want opposite things:

| Refusal | What to do |
|---|---|
| `project has N non-archived task(s)` | Archive or cancel them, or pass `--force` to archive them on the way out |
| `task N is running; cancel it before deleting the project` | Cancel that task. `--force` cannot help — it is refused either way |

`--json` emits `{"id": N, "removed": true}`. The endpoint answers `204`, so
there is no task to print, and an empty stdout would be a parse error in
whatever wraps this.

## `vincent task`

### `vincent task add`

```sh
vincent task add --project ID (--title TITLE | --github-issue N | --github-pull N)
                 [--workflow NAME] [--description TEXT] [--base-branch BRANCH]
                 [--branch NAME] [--priority N] [--agent NAME] [--model M]
                 [--effort E] [--field NAME=VALUE]... [--fields-file PATH]
                 [--paused] [--restricted] [--max-task-cost-usd USD]
                 [--json]
```

Creates a task. It is `queued` immediately unless you pass `--paused`, which
holds it in `paused` until [`vincent task resume`](#vincent-task-pause) — there
is no separate draft state.

| Flag | Notes |
|---|---|
| `--project` | **Required** |
| `--title` | Required unless `--github-issue` or `--github-pull` supplies one; also the source of the branch slug |
| `--workflow` | Defaults to the project's default workflow |
| `--base-branch` | What the task branches **from**. Defaults to the project's default branch |
| `--branch` | What the task's branch is **called**. Used verbatim and wins over any template; defaults to the project's or the global [`branch_template`](configuration.md#branch_template) |
| `--priority` | Higher runs first; default 0 |
| `--field name=value` | Task field; repeat for more. Everything after the first `=` is the value, and a repeated name uses the last value |
| `--fields-file PATH` | Read fields from a JSON object of string values; `-` reads it from stdin. Combines with `--field`, which wins for a name both supply |
| `--agent` / `--model` / `--effort` | The task-level override. It replaces workflow `defaults`, never an explicit step field |
| `--github-issue N` | Create the task from GitHub issue `N`. See below |
| `--github-pull N` | Create the task from GitHub pull request `N`, **running it on that pull request's head branch**. See below |
| `--paused` | Create the task paused; it starts only when resumed (`vincent task resume`). The scheduler never sees it before then |
| `--restricted` | Run every agent step restricted, even one whose workflow says full-auto. Refused at creation if a step's agent cannot restrict on this host (cursor on Windows) |
| `--max-task-cost-usd USD` | This task's own spend cap in USD; the lower of it and config's [`max_task_cost_usd`](configuration.md#max_task_cost_usd) applies, so it can tighten the global cap but not lift it. Inert on codex and cursor, which report no cost |

The last three are sent only when you name them, and are recorded on the task
(`--json` shows `restricted` and `max_task_cost_usd`).

Declared workflow fields are validated by the daemon, while additional names
remain valid and are recorded on the same open field map:

```sh
vincent task add --project 1 --workflow release --title "Release 2.0" \
  --field ticket=OPS-42 --field owner=ana
```

```
task 62 created: Release 2.0 (release, branch vincent/62-release-2-0)
  fields: owner, ticket (2)
```

The confirmation line lists **names and a count, never values** — a field can
carry a ticket key or a customer name, and this line ends up in scrollback and
CI logs. It is read off the created task, so a field the daemon filled in — from
`--github-issue`, or from a **required** field's declared
[`default:`](workflow-schema.md#default), which the daemon substitutes for an
omitted key — is listed too. `--json` prints the task instead, values and all.

#### Fields from a file or stdin

`--fields-file` takes one JSON object whose values are all strings — the form a
generator produces, and the one that carries spaces, newlines and quotes
without shell escaping:

```sh
vincent task add --project 1 --workflow release --title "Release 2.0" \
  --fields-file ./release-inputs.json

jq -n '{ticket: $t, notes: $n}' --arg t OPS-42 --arg n "$(cat notes.md)" |
  vincent task add --project 1 --workflow release --title "Release 2.0" \
    --fields-file -
```

The two flags **combine**: the file is the base map and each `--field`
overrides its own name, which is the same last-wins rule `--field` already
follows, one level out.

```sh
vincent task add --project 1 --workflow release --title "Release 2.0" \
  --fields-file ./release-inputs.json --field ticket=OPS-43   # ticket is OPS-43
```

Rejected locally with exit 1, before the daemon is called:

| | |
|---|---|
| A value that is not a JSON string | A number, boolean, `null`, array or object. The message names the **key** and never the value |
| An empty or all-whitespace name | The same rule `--field` applies |
| Anything after the first JSON object | One document per file; a second is never silently discarded |
| More than 4 MiB | The API's own body bound (§13.1), applied to the read so a pipe cannot be unbounded |

A name repeated *inside* the JSON object takes its last value, which is what a
JSON decoder does. Names the workflow never declared stay valid either way —
declaring `fields:` does not close the map — and everything the daemon
validates (required, `type`, `pattern`, [`enum`](workflow-schema.md#enum-fields)
membership, per-field size) is still checked by the daemon.

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

#### Creating a task from a pull request

```sh
vincent task add --project 1 --github-pull 412
```

```
task 62 created: #412 Add a thing (adhoc, branch feature/add-a-thing)
  from lezli01/vincent#412, running on its head branch feature/add-a-thing
```

Same shape as `--github-issue` — the number and nothing else, resolved by the
daemon, explicit flags winning over what it would fill in — with one difference
that is the whole point: **the task's branch is the pull request's head branch**,
not `vincent/{id}-{slug}`. Its worktree is that branch checked out with an
upstream, so when a workflow pushes, the commits land on the pull request.

That branch is the one thing you cannot override. `--branch` is ignored for such
a task, and `vincent task retry --branch` on it is refused: renaming it would
detach the task from the pull request it was created for.

What it fills in: the title (`#N ` and the pull request title), the description
(the pull request body plus a trailing `GitHub pull request #N: <url>` line), and
a workflow field declared as `pull`, which receives the **number** — that is how
a `run:` step acts on the pull request, since a command step reads the
environment and not the template context.

Things worth knowing before you use it:

- **Closed and merged pull requests work.** Redoing a reverted one and acting on
  a merged one are why `--state` exists on
  [`vincent github prs`](#vincent-github-prs).
- **A fork runs, and cannot push back.** Its head is fetched from
  `refs/pull/N/head` into a branch with no upstream. The command says so on the
  line above; a delivery step will fail rather than push somewhere nobody
  watches.
- **A local branch of that name is fast-forwarded**, or, if it has diverged, the
  task blocks with `pull_branch_diverged` and your unpushed commits are left
  alone. A branch already checked out somewhere — including your own main
  checkout — blocks with `pull_branch_checked_out`, because git cannot put one
  branch in two worktrees.
- **Archiving never deletes that branch.** It is not a branch vincent cut, and
  `delete_empty_branch_on_archive` does not apply to it — which matters most for
  a merged pull request, whose branch has no commits past its base.
- `--github-issue` and `--github-pull` are mutually exclusive: they would prefill
  the same title and description from different sources.

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
vincent task show <id> [--step RUN] [--json]
```

Shows one task with its step runs, the actions valid right now, and any pending
input request.

The `base` row is where the task started: `master @ 1a2b3c4` when the daemon
recorded the commit the branch was cut from, or the branch name alone when it
did not — the worktree does not exist yet, or nothing was fetched
([`fetch_base_branch`](configuration.md#fetch_base_branch) is off, the base has
no upstream, or the fetch failed). A `refresh` row follows only when something
about that start needs your attention: the fetch failed, so the base may be
stale, or your local base branch was not fast-forwarded, with the reason and
git's message. A healthy refresh prints no row.

A queued task waiting on something other than a free slot prints a `hold` row:
the reason, and when the daemon will try again. `usage_limit until
2026-09-15T14:20:00+02:00` is an agent's usage limit being waited out;
`retry_backoff until …` is a step's
[`retry_backoff`](workflow-schema.md#step-fields) pacing its next attempt. The
time is local RFC3339, the way `doctor` and `daemon status` print instants, and a
hold with no resume time prints the reason alone. A task in the ordinary queue
prints no row. A held task is never `blocked`, so the `hold` and `blocked` rows
never appear together. `--json` carries the same facts as `queued_reason` and
`admit_not_before`.

The `origin` row says which definition the task's workflow name resolved to —
`built-in`, `project .vincent/workflows/adhoc.yaml`, `global
workflows/release.yaml`, `derived from task 41` for a fan-out lane, or `unknown`
for a task created before vincent recorded it. A registry-backed origin is
followed by a `sha256:` digest of the source it was loaded from; `derived` and
`unknown` carry none. A project or global workflow shadows a built-in of the
same name, so this is what tells a repository's own `adhoc.yaml` from the
built-in `adhoc` long after the fact. It is captured once, at creation, and
never updated.

An `awaiting_input` task prints the request it is parked on, numbered. Those
numbers are what [`vincent task answer`](#vincent-task-answer) takes — the wire
format is keyed by question *text*, and nobody should have to retype a sentence:

```
actions   answer, cancel

awaiting input: question
  1. Which database?
     suggested: postgres, sqlite
  2. deploy: Which regions?  (one or more)
     suggested: eu, us
  answer with `vincent task answer 7 --answer 1=<value>`
```

The `actions` row is the daemon's own `available_actions`, so a script reads
what is legal instead of probing for `409`s. Every name in it has a
`vincent task <action>` subcommand, under the tree's spelling: the API says
`follow_up` where the command is `follow-up`, so a script turning one into the
other replaces `_` with `-`.

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

`--step RUN` prints what one attempt was *given* instead of the task. `RUN` is a
step_run id from that `RUN` column, the same id
[`vincent task transcript --step`](#vincent-task-transcript) takes. After one
header line (run id, step id, attempt, state) come the four sections of the
TUI's Step Details tab, in its order:

| Section | What it holds |
|---|---|
| `input:` | The rendered prompt, `run` and `check` bodies, printed in full and indented, then the result summary |
| `resolution:` | Agent, model and effort, each with the level that supplied it (`claude (from the step)`); permission mode; timeout; check timeout; shell; working dir; and the include chain the step was resolved from |
| `control flow:` | What `if:` rendered to, the loop iteration, the `for_each` item and the `for_each` list as its items, and the fan-out lane |
| `outcome:` | Tokens, cost, active duration, time waiting on a human, exit code, check exit code, failure and skip reasons, whether a human edited the step before this retry, and the transcript path |

An agent step always has a `rendered prompt` entry and a command step a
`rendered run` entry. `rendered check` and `if: rendered to` appear only when the
step had one. Two wordings are different facts: `not recorded (this attempt
predates the record)` means nothing was recorded, and `(rendered empty)` means
the template rendered to nothing. A body cut at the record's 64 KiB ceiling opens
the input section with a notice saying so. On a retried agent step, vincent
appends the previous attempt's failure to the prompt; a
`--- appended by vincent: the previous attempt's failure ---` line marks where
the workflow's own text ends.

With `--json`, `--step` prints the attempt's step run object unchanged: the
matching element of the `steps` array `task show --json` prints. `vincent task
show 7 --step 12 --json | jq -r .rendered_prompt` is the exact bytes. A run id
that is not one of the task's own runs exits `1` with `Error: task 7 has no step
run 12`. A fan-out lane's runs belong to the lane's task, so ask that task.

### `vincent task transcript`

```sh
vincent task transcript <id> [--step RUN] [-f | --follow] [--json | --raw]
```

Prints one attempt's transcript — the complete record of what it did, which
`task show` only names the file of.

`--step` takes a **step_run id**: the `RUN` column `task show` prints, which is
unambiguous across retries where every attempt is its own run. The same id given
to [`vincent task show --step`](#vincent-task-show) prints what that attempt was
handed rather than what it did. Omitted, it
selects the running attempt if there is one, and otherwise the newest attempt by
run id (creation order — which, unlike the step order, stays chronological when a
task has parallel steps or fan-out lanes).

| Output | What it is |
|---|---|
| default | The records rendered as text, the vocabulary the TUI's output pane renders: the run header (working directory and the tools the agent was given) as a first `# ` line, assistant output, tool calls and their outcomes, the skills that ran as `> skill <name> <args>` lines, a subagent's work behind a `| ` rail, command output, the agent's running to-do list as a `# plan:` line, vincent's own annotations, and a closing `= done` line carrying whatever the agent reported about the run — elapsed time, turns, an unusual stop or terminal reason, permission denials, cost. Token usage is dropped — `task show` carries it |
| `--json` | The normalized records as NDJSON, one JSON object per line, in vincent's vocabulary including its `vincent.*` annotations. This is the `jq` route |
| `--raw` | The agent's own JSONL, byte for byte, exactly as it was recorded |

What an *agent's* command printed, and the hunks of a file edit it made, are the
two records the default rendering drops. The output pane shows both at `verbose`
only and this command has no verbosity control, so the alternative to dropping
them is showing every command's whole output and every edit's whole patch to
every reader; `--raw` and `--json` both carry them for a reader who wants the
body. An edit's outcome line still says how much it changed, as `< +13 −9`. The result line stops at the pane's `normal` content for the
same reason: the API-time, cache and per-model breakdown is `--json` only.
An adapter that reports none of that metadata — codex — prints no header and a
bare `= done`. Cursor reports part of it: its `# ` line is the working directory
with no tools, and its result line carries the elapsed time, as in
`= done (2.0s)`.

A **subagent's** work prints nested, in the order it arrived, at the pane's
[`normal` content](../guides/tui.md#when-the-agent-runs-subagents): its prose,
tool calls, outcomes, skill loads and errors, each behind a `| ` rail, and
never its reasoning or plan. A `| -> <description>` line names the subagent whenever the
output moves into one or from one to another, and the next line from the agent
itself ends the rail. When a subagent ends, one line on the rail says how:

```text
> Agent Check each claim in the gate walkthrough against the code
< started in background
| -> Verify the gate walkthrough
| > Bash git log --oneline -5
| = completed - Verify the gate walkthrough - 14 tool uses - 5m00s
```

A failed subagent's line starts `| ! failed`, a stopped one's `| ~ stopped`,
and any other status `| - <status>`. Tool uses and duration appear only when
the agent reported them. `< started in background` is the outcome of a call
that launched a subagent without waiting for it. The label names the
subagent by the description it was started with, or by the spawning call's
summary when that is all there is. Only
[Claude Code](../guides/agents.md#claude-code) reports subagents.

A **skill** that ran prints as `> skill <name> <args>`, with ` (forked)` for a
skill that ran as its own sub-run, and a load that carries the agent CLI's
refusal prints as `! skill <name> failed: <error>` (`invocation` in place of a
name it did not give). A `Skill` call Claude Code refuses loads nothing, so it
prints as `> Skill <name>` with the refusal on the `! ` outcome line under it.
The skill's own text — the `SKILL.md` it loaded — is never printed;
`--raw` has it. A skill the agent loaded itself comes from a `Skill` tool call,
and prints on that call's line in place of `> Skill <name>`, so its outcome
stays directly under it:

```text
> skill echo-probe zebra
< Launching skill: echo-probe
ECHO-PROBE zebra
```

That pairing needs the call and the load in the same fetch. Without `-f` they
always are. Under `-f` a poll can land between the two: the call has then
already printed as `> Skill echo-probe`, and the load prints nothing rather
than read as a second call — the one case where the skill's arguments are not
shown. A load whose call is before the range the command opened on prints
where it is. Only [Claude Code](../guides/agents.md#claude-code) reports skill
loads.

Everything a reader reads goes to **stdout**, including a command step's stderr,
which is tagged `[stderr]` rather than split onto the other file descriptor: a
transcript is one interleaved stream and two descriptors would scramble the
ordering that makes it readable. The command's own diagnostics go to stderr, so
stdout stays pipeable.

`-f` (`--follow`) opens on the tail and then resumes from the record boundary
the daemon reports, printing records as the attempt writes them. It ends when
that attempt stops running — it does not wait for a later retry, which is a
different run.

A step run that never had a transcript — a manual gate — prints a line saying so
on stderr and exits `0`; nothing failed. A transcript whose file is gone (pruned
by `transcript_retention_days`, or deleted) exits `1`.

### `vincent task diff`

```sh
vincent task diff <id> [--by lane] [--stat] [--json]
```

Prints the task's diff: its worktree compared with the merge-base of its base
and `HEAD` — committed, staged and unstaged changes to tracked files. Untracked
files are not included. It is the same change the TUI's Diff tab shows, read
from the same endpoint.

Piped or redirected, the output is **exactly the bytes the daemon served**, with
no size limit, so it can go straight to `git apply`:

```sh
vincent task diff 7 > task-7.patch
vincent task diff 7 | git apply --check
```

On a terminal with `NO_COLOR` unset, file headers are bold, `@@` hunk lines
cyan, additions green and removals red.

`--by lane` splits a fan-out parent's diff by the lane that produced each
change. Every section is printed in the daemon's order under one ASCII header
line, a section with no change included, and the remainder is always last:

```
# lane api (task 42, merge 3f1c9a0b2d4e)
diff --git a/api.go b/api.go
…
# remainder (the task's own commits and uncommitted work)
diff --git a/README.md b/README.md
…
```

A task that fanned out nothing prints a single remainder section. `git apply`
ignores the header lines, so the grouped output still applies. `lane` is the
only grouping: any other `--by` value exits `1` before a request is sent.

`--stat` prints a per-file table instead of the patch. `ADDED` and `REMOVED`
count the lines inside hunks; a binary file reads `binary` in both. A renamed
file is named by its new path, a deleted one by its old path. With `--by lane`
a leading `LANE` column credits each file to its section, `-` for the
remainder. The table is never coloured, and an empty diff still prints its
header row.

```
FILE      ADDED   REMOVED
api.go    +12     -3
logo.png  binary  binary
```

| Flags | `--json` output |
|---|---|
| none | `{"diff": "<text>"}` |
| `--by lane` | The sections array as the API serves it: `lane_id`, `child_task_id`, `merge_commit`, `remainder`, `diff`. Never empty — the remainder is always there |
| `--stat` | `[{"path", "added", "removed", "binary"}]` |
| `--stat --by lane` | The same rows, each also carrying `lane_id`, `child_task_id` and `remainder` |

An empty diff prints nothing and exits `0`, as `git diff` does (`{"diff": ""}`
and `[]` under `--json`). A task with no worktree yet, one whose worktree is
gone, or an unknown task exits `1` with the daemon's message on stderr.

### `vincent task cancel`

```sh
vincent task cancel <id> [--json]
```

Aborts the task, killing any running process (graceful termination, then a kill
after 10 seconds). Valid from `queued`, `running`, `awaiting_input`,
`awaiting_gate`, `awaiting_children`, `blocked` and `paused`; anything else exits
1 with the state it actually found. From `awaiting_children` it cascades to every
unfinished lane.

### `vincent task follow-up`

```sh
vincent task follow-up <id> (--prompt TEXT | --run CMD | --workflow NAME)
                            [--agent NAME] [--model M] [--effort E] [--field NAME=VALUE]...
                            [--paused] [--json]
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

`--field name=value` sets a field for this run only, laid over the task's own,
with the spelling `task add` uses; repeat it for more. A `--workflow` that
declares fields checks them exactly as `task add` does — the task's values
included — so a required field the task never carried, with no default, exits 1
until a `--field` supplies it. The task keeps the fields it was created with.

The command returns as soon as the run is queued — the scheduler admits it like
anything else. When it ends the task returns to the state it came from: `done`
to `done`, `aborted` to `aborted`, whatever the run did. A follow-up never
changes a task's verdict, and it is repeatable.

`--paused` records the follow-up but holds the task `paused` instead of queuing
the run, so nothing starts until `vincent task resume <id>`. Cancelling a held
follow-up drops it and leaves the task `aborted`.

It was the first human action to get a command line, and it got one because
batches want one:

```sh
for id in 41 42 43 44 45 46; do
  vincent task follow-up "$id" --run 'git rebase origin/main'
done
```

The rest followed (task 048); they are documented below.

### `vincent task pause`

```sh
vincent task pause <id> [--json]
vincent task resume <id> [--json]
```

`pause` holds the task at the **next step boundary** — a running step finishes
first, it is not killed. Valid from `queued` and `running`. `resume` returns a
paused task to the queue, where the scheduler admits it like any other; valid
from `paused`.

### `vincent task approve`

```sh
vincent task approve <id> [--json]
vincent task reject <id> [--json]
vincent task skip <id> [--json]
```

`approve` passes the manual gate the task is waiting on and the workflow
continues. `reject` fails it, and the task blocks with reason
[`rejected`](task-lifecycle.md#failure-reasons). Both are valid from
`awaiting_gate` only.

`skip` abandons the step the task is sitting on and advances to the next one.
It is valid from `awaiting_gate` **and** from `blocked` — skipping a step that
failed is how a workflow gets past a check the run does not need.

### `vincent task retry`

```sh
vincent task retry <id> [--branch NAME] [--prompt TEXT | --prompt-file FILE]
                        [--run CMD | --run-file FILE] [--paused] [--json]
```

Re-runs the step the task blocked on. Valid from `blocked`, and from
`awaiting_children`, where it means something else — see below. With no flags it
is a plain retry; the flags change what gets re-run, and each one is a different
kind of recovery:

| Flag | What it does |
|---|---|
| `--branch NAME` | Renames the task's branch **before** the retry re-admits it. This is the [`branch_exists`](task-lifecycle.md) recovery: the task, its id and its transcripts all survive, which deleting and re-creating it would not. Refused on a task created from a pull request |
| `--prompt` / `--run` | Edit+retry. The text replaces that step's prompt or command in **this task's** workflow snapshot, and in no other task's — the registry file is untouched |
| `--prompt-file` / `--run-file` | The same, read from a file, or from stdin with `-`. A replacement prompt is usually several lines, which argv is a poor place for |
| `--paused` | Records the retry, and any edit or rename with it, but holds the task `paused` instead of queuing it; `vincent task resume` re-admits it. A blocked parent's lanes are held too. Refused on a parent parked in `awaiting_children`, whose retry never queues it |

`--prompt` with `--prompt-file` (or `--run` with `--run-file`) is a usage error:
they are two spellings of one value, so it exits 1 before any request is sent.

```sh
vincent task retry 7 --prompt-file - <<'EOF'
The check failed because the fixture path is wrong on Windows.
Use filepath.Join rather than a slash-separated literal.
EOF
```

On a fan-out parent parked in `awaiting_children` the same command **cascades**:
the parent has no step of its own to re-run, so one call re-admits every blocked
lane beneath it, at any depth, and the parent stays parked while they run. Fix
the cause first — each lane's step re-runs exactly as it was — then:

```console
$ vincent task retry 42
task 42 is now awaiting_children
  2 blocked descendants re-admitted
```

All three flags are refused from that state: a `fan_out` step carries no prompt
or command to edit, and `--branch` would rename the branch every live lane holds
as its base. Edit the blocked lane itself instead. With `--json` the count rides
beside the task's own fields as `retried_descendants`; a retry that re-admitted
nothing omits it and prints the plain task object, where
[the API's own body](api.md) always carries the field and reads `0`.

### `vincent task repair`

```sh
vincent task repair <id> (--prompt TEXT | --prompt-file FILE)
                         [--agent NAME] [--model M] [--effort E] [--json]
```

Runs one ad-hoc agent with this prompt in the blocked task's **existing**
worktree. Valid from `blocked`. Whatever the agent does, the task returns to
`blocked` at the same step with the same reason: a repair changes the worktree,
and a human still decides whether to retry.

A prompt is required — `--prompt` and `--prompt-file` are mutually exclusive and
one of them must be given, and `-` reads stdin. `--agent`, `--model` and
`--effort` apply to this run only, standing in for the step level of §8.6's
chain; a value no catalog recognizes is a warning on stderr, not a failure.

### `vincent task chat`

```sh
vincent task chat <id> [--title TITLE] [--agent NAME] [--model M] [--effort E] [--json]
```

Opens a [chat](#vincent-chat) that works in a stopped task's **own** worktree
and branch, to ask why it blocked, look over what a gate is about to approve,
or talk about finished work. Valid from `blocked`, `awaiting_gate`, `done` and
`aborted`; anything else exits 1 with the state it actually found. It prints the
chat — its id, the task, its title, agent and branch — and the two commands
that come next; `--json` emits the chat object, which names the task as
`linked_task_id`.

The chat opens with the task's context already in it: the task's objective, and
the failed step, the gate or the last step it stopped on. Talk to it with
[`vincent chat send`](#vincent-chat-send), and end it with
[`vincent chat close`](#vincent-chat-close).

The task does **not** move. It keeps its state, and while the chat is open the
task is locked: every action on it but `cancel` exits 1, and it carries
`open_chat_id` and offers `[cancel]` — or nothing, where cancel is not legal
either — in `available_actions`. `vincent task cancel` stops the chat's turn
and closes the chat along with aborting the task. A task holds one open chat at
a time, so a second `task chat` exits 1 and names the open one:

```
Error: task 7 is locked by chat 12, which works in its worktree; close the chat first
  chat 12 is open on it: continue with `vincent chat send 12 MESSAGE`, or end it with `vincent chat close 12`
```

The first line is the daemon's, printed as it stands; the second is the
command's own.

`--title` defaults to the task's. `--agent`, `--model` and `--effort` stand in
for the step level of §8.6's chain, as a repair's do; the agent must be able to
resume its own session, and one that cannot exits 1 with
`agent_cannot_resume`. The chat runs with the permission mode a repair on the
task would — a task whose workflow is `restricted` gets a restricted chat, and
a chat that resolves to codex exits 1 there, because codex cannot keep a
resumed turn restricted — and in the task's container when its workflow runs in one.

A task that never got a worktree — blocked on `branch_exists`, or aborted
before it was admitted — exits 1 with `task_has_no_worktree`: vincent does not
create one for a chat.

### `vincent task archive`

```sh
vincent task archive <id> [--force] [--json]
```

Archives a finished task and removes its worktree. Valid from `done` and
`aborted`. When
[`delete_empty_branch_on_archive`](configuration.md#delete_empty_branch_on_archive)
is on, a branch carrying no commits past its base is deleted too, and what
happened to it is printed under the state line — and carried in `--json` as
`branch`.

A worktree with **uncommitted changes** is refused with exit 1 and
`details.reason: "worktree_dirty"`; the command says so and names the way out.
`--force` is the confirmation, and it discards those changes:

```
Error: worktree_dirty: worktree ~/.local/share/vincent/worktrees/7 has local changes (untracked included); confirm with force
  pass --force to archive it anyway, discarding those changes
```

The first line is the daemon's, printed as it stands; the second is the
command's own.

### `vincent task delete`

```sh
vincent task delete <id>... [--branch] [--json]
vincent task delete --before <date|duration> [--branch] [--json]
```

Aliased as `vincent task rm`. Permanently deletes an **archived** task: the
row, its step attempts and its transcript directory. This is the only thing in
vincent that removes a task row — [retention](files.md#transcripts) removes
transcript *files* and never a row.

It does not prompt. The command tree exists for scripting, and the daemon's
refusals are the confirmation story:

| Refusal | `details.reason` |
| --- | --- |
| The task is not archived | `not_archived` |
| It is a fan-out parent whose lanes still exist — delete those first | `has_lanes` |
| A handed-off chat points at it — that chat would be left pointing at nothing | `handoff_target` |

Each is exit 1 carrying the daemon's own wording, which names the row that is
holding on. An unknown id is exit 1 with a 404.

`--branch` additionally deletes the task's local branch. [§10's standing
rule](configuration.md#delete_empty_branch_on_archive) is unchanged by asking: a
branch carrying **any** commit past its base is kept and reported
`has_commits`. The remote branch is
never touched — that leg belongs to
[`delete_remote_branch_on_archive`](configuration.md#delete_remote_branch_on_archive)
and to archive alone.

`--before` sweeps instead of naming ids: every task archived before a date
(`2026-01-31`, or a full RFC3339 instant) or a duration back from now (`30d`,
`12h`). It is one `DELETE` per row, sequentially — there is no bulk endpoint —
and it does not stop at the first refusal. `--json` emits one entry per row
with `id`, `deleted`, `branch` and, on a refusal, `reason` and `error`. Exit is
1 if any row was refused or failed.

### `vincent task import`

```sh
vincent task import <archive.tar.gz> <task-id> [--project <id>] [--json]
```

Brings one **archived** task back out of an archive written by
[`vincent daemon backup`](#vincent-daemon-backup): its row, its step attempts
and its transcripts. It is the undo for [`vincent task delete`](#vincent-task-delete),
and it also imports a task from another installation's backup when that id is
free here. The task keeps its id and comes back archived and read-only; no
branch or worktree is touched.

Unlike [`vincent daemon restore`](#vincent-daemon-restore) it needs a
**running** daemon, and exits `2` without one: an import writes rows, and only
the daemon opens the database. The archive path is resolved before it is sent.

```
imported task 12 "Add login" into project 3: 4 step run(s), ids kept, transcripts 1.2KB
```

`ids renumbered` instead of `ids kept` means a step attempt id was already taken
here, so every attempt got a fresh id in its original order. `--json` prints the
response body. Refusals are exit `1` with the daemon's wording:

| Refusal | `details.reason` |
| --- | --- |
| A task here already holds that id | `task_exists` |
| The task was not archived in the backup | `not_archived` |
| No project here has the backed-up project's id **and** name — pass `--project <id>` to import into another one | `project_mismatch` |
| It is a fan-out lane whose parent is not here — import the parent first | `parent_missing` |
| `{data_dir}/transcripts/{task-id}/` already exists — move it aside | `transcripts_present` |
| The archive has no task with that id | `task_not_in_backup` |
| `--project` names no project | `project_not_found` |
| The archive's schema is newer than this binary's | `schema_too_new` |

An archive from an older vincent imports: the daemon migrates a staged copy of
its database, never your live one. There is no bulk import — one call per task.

### `vincent task answer`

```sh
vincent task answer <id> (--answer N=VALUE... | --allow | --deny |
                          --body FILE) [--json]
```

Answers the input request an `awaiting_input` task is parked on; the run resumes
in place. There are two ways in, and they do not mix — the flags below are
mutually exclusive, and one of them is required.

**By number, for a person.** `N` is the position
[`vincent task show`](#vincent-task-show) prints the question under. Repeat the
flag for one index to give a multi-select several values; everything after the
**first** `=` is the value, so a URL or a regex needs no escaping:

```sh
vincent task answer 7 --answer 1=postgres --answer 2=eu --answer 2=us
```

The wire format is keyed by question *text* (§13.2) and stays that way — the
numbering is a CLI convenience that never reaches the daemon. The command reads
the pending request first, maps the numbers onto it, and checks the answer
locally before posting: a wrong number or a missing answer costs no round trip.
The daemon validates it again and remains the authority.

A **permission** request is decided, not answered: `--allow` or `--deny`, and
`--answer` on one is refused.

**By payload, for a script.** `--body FILE` posts a §13.2 answer payload
verbatim, with `-` reading stdin and no per-flag reconstruction:

```sh
jq -n '{answers: {"Which database?": ["postgres"]}}' |
  vincent task answer 7 --body -
```

## `vincent config`

```sh
vincent config get [key] [--json]
vincent config set <key> <value> [--json]
```

Reads and changes the daemon's
[`config.yaml`](configuration.md) through the daemon, which owns the file and
hot-reloads it. It is a client of `PATCH /v1/config` like every other command
here, not a second editor — so a `set` is the same operation, with the same
validation, that the [TUI's daemon view](../guides/tui.md#daemon) performs.

Keys are the dotted paths `config.yaml` carries, which are the paths the
[configuration reference](configuration.md) documents:

```sh
vincent config get                      # every key, one `path = value` per line
vincent config get max_parallel_tasks   # 3
vincent config set max_parallel_tasks 6
vincent config set log_level debug
vincent config set defaults.agent_timeout 90m
vincent config set notify.on "blocked awaiting_gate"
vincent config set notify.command "/usr/local/bin/notify-me"
vincent config set environment.set "LANG=C.UTF-8 TZ=Etc/UTC"
vincent config set triggers.enabled true
vincent config set tui.keys "refresh=f5 pause=x reject=p"
```

Details worth knowing:

- **A set takes effect without a restart.** The daemon validates the whole
  candidate file, writes it and applies the result before answering, so the
  next `vincent config get` reads the new value. The one exception is `listen`:
  it is written to the file and the running daemon keeps the address it bound,
  which the command says out loud.
- **An invalid value changes nothing.** The file is left byte-identical and the
  command exits 1 with the daemon's message. There is no partial application.
- **Your comments survive.** `config.yaml` ships as a documented template, and
  the daemon edits a key in place — or uncomments its documented block where it
  stands — rather than regenerating the file. A `notify.on` set turns the
  commented-out `notify:` block on without flattening the paragraph above it.
- **Lists and argv are whitespace-separated in one argument**, as in the
  examples above. An argument containing a space cannot be written this way and
  has to be edited in the file. Setting a list to `""` empties it, which is how
  the notify hook is switched off from the command line. `environment.set` and
  `tui.keys` take `NAME=VALUE` pairs the same way; a set replaces the whole
  map, so `vincent config set tui.keys ""` restores the shipped keymap.
- **`environment.inherit` takes `all`, `none`, or a list of names.**
- **Per-project settings are not here.** They live in the database and are
  edited with [`vincent project`](#vincent-project) or the TUI's projects view.

## `vincent status`

```sh
vincent status <message> [--json]
```

Records what the current step is doing, in its own words. It runs **from inside
a step** — an agent's shell tool, or a `command` step's script — and takes no
task or step argument: it reads `VINCENT_TASK_ID` and `VINCENT_STEP_ID` from
[the environment](../guides/workflows.md#the-vincent-environment) the daemon
sets on every agent and command step. It does not work in a step that runs
[in a container](configuration.md#container), where there is no vincent binary
and no daemon on `127.0.0.1`; a containerized agent uses the `step_status`
[MCP tool](../guides/mcp.md#steps-in-a-container) instead.

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

## `vincent statusline`

```sh
vincent statusline [--wrap-b64 <payload>]
```

Reports claude's usage windows to the daemon, from inside Claude Code's status
line. **You do not run this by hand.** Claude Code runs it, once per render, as
the `statusLine.command` in `~/.claude/settings.json`; the
[daemon view](../guides/tui.md)'s `i` key is what puts it there, after showing
the exact JSON it will write.

Claude Code has no usage subcommand to poll, but it hands its status line a JSON
object on stdin carrying `rate_limits.five_hour.used_percentage`,
`rate_limits.seven_day.used_percentage` and each window's `resets_at`. This
command reads that object, pushes the windows to the daemon, and prints the
status line you had before — so the number reaches vincent's board and detail
views without a second process polling anything.

Details worth knowing:

- **It never breaks your status line.** A daemon that is down, an auth failure,
  a push that times out, a stdin body that is not JSON: every one of them still
  runs the wrapped command, still prints its output and still exits 0.
- **`--wrap-b64` carries what vincent displaced**, base64url of the
  `statusLine` object that was in the settings file. Its `command` is run with
  the same stdin, and its stdout is what you see. Uninstalling puts that object
  back verbatim, including removing the key when there was nothing there.
- **The reading is as durable as the daemon.** It is held in memory, not the
  database, so a daemon restart drops it until Claude Code renders again.
- It reports for the `claude` adapter only; codex answers the same question on
  request, through its own app-server, with nothing to install.

## `vincent update`

```sh
vincent update [--check] [--dry-run] [--require-signature] [--json]
```

Asks GitHub for the latest **stable** release and, unless `--check` is given,
installs it over this binary. A prerelease is never offered.

This command talks to the release feed directly rather than through the daemon,
so it works with no daemon running and before the daemon's own background check
has ever polled. That is also what makes
[`update.check: false`](configuration.md#update) a literal promise: with the
background check off, the daemon makes no request, and only running this
command does.

**It only replaces a binary vincent owns** — a direct-download archive, or one
you placed by hand. For an install a package manager owns it changes nothing
and prints that channel's command:

| Channel | Printed |
|---|---|
| Homebrew | `brew upgrade vincent` |
| Scoop | `scoop update vincent` |
| WinGet | `winget upgrade --id lezli01.Vincent --exact` |
| mise | `mise upgrade vincent` |
| `go install` | `go install github.com/lezli01/vincent/cmd/vincent@latest` |
| deb / rpm | the release page, to fetch the new package |
| not identifiable | the release page — treated as package-managed |

**What is verified before anything runs.** The release signs `checksums.txt`,
not each asset, so the chain is the one the release notes tell you to run by
hand: the cosign signature over `checksums.txt` against the project's identity
and issuer, then the downloaded archive's SHA-256 against that verified file.
On any mismatch nothing is replaced and the old binary is left byte-identical.

`cosign` is used when it is on your `PATH` and is not bundled. Without it the
checksum check runs alone and the command says plainly that the signature was
**not** verified; `--require-signature` makes a missing `cosign` fatal instead.

There is no prompt — running the command is the decision, and this tree does
not prompt because its purpose is scripting. Use `--dry-run` to see what would
happen. After a successful swap the running daemon still has the old build, and
the command prints the restart for your install.

Exit codes:

| | `0` | `1` | `2` |
|---|---|---|---|
| `--check` | up to date | the check failed (offline, rate-limited) | an update is available |
| no flag | nothing to do, **or** swapped successfully | verification or the swap failed; the binary is untouched | an update exists but a package manager owns this install |

`--json` carries `swapped`, which is what separates the two `0`s.

```sh
vincent update --check --json
{
  "current_version": "v0.4.1",
  "latest_version": "v0.5.0",
  "update_available": true,
  "published_at": "2026-08-21T09:31:07Z",
  "release_url": "https://github.com/lezli01/vincent/releases/tag/v0.5.0"
}
```

## `vincent skills`

vincent publishes agent skills — `skills/vincent-workflows/` and
`skills/vincent-triggers/` — so an agent you talk to **directly**, outside a
vincent run, knows how to author a vincent workflow or an event trigger. Runs
inside vincent do not need them: the built-in `create-workflow` and
`update-workflows` workflows carry the first skill's text in their own prompts,
and `create-trigger` and `update-triggers` carry the second's.

Neither subcommand talks to the daemon, so **neither can exit 2**. Detection is
a filesystem read and works on a machine with no node installed; only `install`
shells out.

These are the skills vincent publishes, not the ones an agent loads. For the
skills a chat's own agent CLI would load in its directory, and the exact text
that invokes one, see
[`vincent chat skills`](#vincent-chat-skills).

### `vincent skills ls`

```sh
vincent skills ls [--json]
```

Aliased as `vincent skills list`. One row per published skill: the version this
binary ships, the state of the copy on disk, and the agents it is linked into.

| State | Meaning |
|---|---|
| `not installed` | No copy on this machine |
| `installed and current` | The installed version is the shipped one |
| `out of date` | The installed copy predates this binary's; both versions are named. A copy carrying no `metadata.version` — every copy installed before that marker existed — reads `installed unversioned` |
| `newer than this build` | The installed copy is ahead — a downgraded binary, not an up-to-date skill |
| `differs` | The versions are unequal and at least one is not semver, so no direction is claimed |
| `unreadable` | A copy exists and its `SKILL.md` could not be read or parsed at all |

`skills add … -g` keeps **one** copy in a global store, `~/.agents/skills/`, and
links it into each agent's directory — so there is one row per skill, not one
per agent, and one version rather than three.

The agents column is what is on disk. It can name agents vincent does not drive
(one store serves Cline, Copilot, Zed and the rest), and it can disagree with
`npx skills list -g`, whose agent column is that CLI's remembered *selection*
rather than the links it left behind.

### `vincent skills install`

```sh
vincent skills install [NAME...] [--agent NAME]... [--json]
```

Installs the named skills, or — with no name — every published skill that is not
already current. It runs, once per skill:

```sh
npx skills add lezli01/vincent --skill NAME --agent claude-code codex cursor --yes --global
```

That is the published command with the interactive agent picker answered.
`--agent` narrows the selection to some of `claude`, `codex`, `cursor`; the
default is all of them. Note the `skills` CLI's slug for claude is
`claude-code`.

It needs `npx` on `PATH` and, on a first run, network — the package is
downloaded before anything happens. Without `npx` it exits **1** with a message
naming the dependency and printing the line to run once node is installed;
`vincent skills ls` is unaffected either way.

Exit `0` everything asked for is installed · `1` an install failed.

## `vincent chat`

Chats are conversations with an agent (spec §5.5). Each one you start gets its
own git worktree and `vincent/{id}-{slug}` branch, exactly as a task does, and
every turn resumes the agent's own session — so turn N sees turns 1..N-1. A
chat opened on a task with [`vincent task chat`](#vincent-task-chat) works in
that task's worktree and branch instead.

Chats are not tasks. They never appear on the board, they run no workflow, and
a chat turn never waits for a scheduler slot: it starts when you send it, or it
is refused because `max_parallel_chats` chats are already running.

Only an agent CLI that can resume its own session can hold a chat. All three
shipped adapters can — `claude` with `--resume`, `codex` with `exec --json resume <id>`,
`cursor` with `--resume`. An adapter that cannot is refused at creation with
`agent_cannot_resume` rather than having the conversation faked by replaying
the log as prompt context.

### `vincent chat start`

```sh
vincent chat start TITLE --project ID [--agent NAME] [--model M] [--effort E]
                  [--base BRANCH] [--message TEXT | --message-file PATH|-] [--json]
```

Starts a chat and prints its id, agent and branch. `--agent` defaults to the
first installed adapter that can resume a session. `--message` sends a first
turn straight away and waits for it, which is `start` plus `send` in one call.
`--message-file` does the same with a message read from a file, or from stdin
when the argument is `-`, exactly as [`chat send`](#vincent-chat-send) reads
one; the two flags are mutually exclusive. With neither, the chat is created
and no message is sent.

The file is read before the chat is created, so a missing file or input
`--message-file` refuses (below) creates no chat. A message the daemon refuses
after creation — one over the send route's body bound, say — leaves the chat
created and `idle`, as `--message` does.

### `vincent chat send`

```sh
vincent chat send CHAT_ID [MESSAGE] [--message-file PATH|-] [--json]
```

The message is the second argument or the content of `--message-file`, and
exactly one of the two: giving both, or neither, is refused and exits 1.

Sends a message and blocks until the turn ends, then prints the agent's answer
on stdout. A failed turn prints its reason on stderr and exits 1 — including
`session_lost`, which means the agent CLI no longer knows the session this chat
was resuming. The chat stays usable; starting a fresh conversation is a
decision you make, not one vincent makes silently. `cursor` is the exception,
and not one vincent can fix: it never refuses an unknown session id — it
starts a fresh chat under it and answers — so a cursor chat whose session has
aged out replies without remembering rather than failing.

While it waits, an in-progress indicator — a moving glyph and an elapsed clock,
`⠋ working… 14s` — is drawn on **stderr**, so a long turn never looks wedged.
It is suppressed entirely in the two cases where anything extra would be wrong:
under `--json`, and whenever stderr is not a terminal. A piped or redirected
`vincent chat send` therefore emits exactly the bytes it would have emitted
without it, on both streams. The answer itself is always stdout and only stdout,
and the indicator's line is erased before anything else is written, so no
residue can precede the answer or an error.

If the agent asks a question mid-turn the chat enters `awaiting_input` and the
send keeps waiting, because the turn has not ended. Answer it from another
terminal with [`vincent chat answer`](#vincent-chat-answer).

A chat turn is bounded by the same two clocks a workflow step is: it fails with
`timeout` past `defaults.agent_timeout` (60 minutes by default) and with
`input_timeout` if nobody answers within `defaults.input_timeout` (24 hours).
Either expiry kills the process, returns the chat to `idle` and releases its
`max_parallel_chats` slot.

Interrupting `vincent chat send` stops the polling, not the turn: the turn
belongs to the daemon. [`vincent chat cancel`](#vincent-chat-cancel) is what
ends it.

Exits 1 with `chat_cap_reached` when `max_parallel_chats` chats already hold a
live agent process. The send is refused, never queued.

#### `--message-file`

`--message-file PATH` reads the message from a file, and `--message-file -`
from stdin. The message never passes through the shell's argv, so nothing
rewrites it on the way, and vincent sends the bytes it read unchanged:

- **Nothing is trimmed.** A trailing newline from `echo`, a heredoc or an
  editor is part of the message. `printf` adds none:

  ```sh
  printf '%s' '/review the auth change' | vincent chat send 12 --message-file -
  ```

- **Empty input is refused**, before any request. Whitespace alone is not
  empty and is sent as it is.
- **Input that is not valid UTF-8 is refused**, before any request, and the
  error never quotes it. A message travels as a JSON string, where an invalid
  byte would silently become U+FFFD. A leading UTF-8 byte-order mark is valid
  UTF-8 and is sent unchanged — and a BOM in front of `/name` stops the agent
  seeing an invocation. Windows PowerShell 5.1's `Out-File -Encoding utf8`
  writes one.
- **The read is capped at 4 MiB**, so an unbounded pipe cannot be buffered
  whole, and one byte more is refused with the limit named. That is the
  CLI's bound, not the message limit: the send route takes a request body of
  at most 64 KiB ([API](api.md#request-bodies)), so a longer message is still refused by the
  daemon with `payload_too_large`, and its message is printed.

Every one of those refusals exits 1. So does a missing file.

#### Quoting a skill invocation

[`vincent chat skills`](#vincent-chat-skills) prints the exact text that
invokes each of the chat's skills, and repeats the rule below for that chat's
own sigil.

A message that invokes an agent's skill — `/review`, or codex's `$review` —
starts with a character a shell treats specially, and a shell that changes it
says nothing. vincent sends what it receives and never repairs a message, so
what the agent gets is whatever the shell left:

- **bash, zsh and pwsh** expand `$name` inside double quotes. Single-quote it:
  `vincent chat send 12 '$review the auth change'`.
- **pwsh** passes `/name` through literally.
- **Git Bash** (MSYS2) rewrites an argument beginning with `/name` into a
  Windows path, so the agent receives `C:/Program Files/Git/review`. Set
  `MSYS_NO_PATHCONV=1` for the command, or use `--message-file`, which is
  never an argument. See
  [troubleshooting](../guides/troubleshooting.md#the-agent-received-cprogram-filesgitreview-instead-of-review).

### `vincent chat answer`

```sh
vincent chat answer CHAT_ID (--answer N=VALUE... | --allow | --deny) [--json]
```

Answers the request an `awaiting_input` chat is parked on; the turn resumes in
place. Questions are answered by the number `vincent chat show` prints them
under — repeat `--answer` for one index to give a multi-select several values —
and a permission request takes `--allow` or `--deny`. It is `vincent task
answer` for a chat, flag for flag, because it is the same request.

### `vincent chat cancel`

```sh
vincent chat cancel CHAT_ID [--json]
```

Stops the chat's live turn and kills its process tree, returning the chat to
`idle` and releasing its slot. This is what `send` cannot do by being
interrupted.

### `vincent chat list`

```sh
vincent chat list [--project ID] [--task ID] [--archived] [--json]
```

One line per chat: id, state, agent, title. `--json` emits the chat objects.
`--task` narrows the list to the chats opened on one task with
[`vincent task chat`](#vincent-task-chat).

Archived, handed-off and closed chats are hidden unless you pass `--archived`,
the way `vincent task list --archived` works. All three terminal states come
back under the one flag: a handed-off or closed chat is as done with as an
archived one.

### `vincent chat show`

```sh
vincent chat show CHAT_ID [--json]
```

The chat's header and every turn in order, with each turn's prompt, answer and
— where it failed — its reason. A chat parked in `awaiting_input` prints the
request first, with its questions numbered: those are the numbers
`vincent chat answer --answer N=VALUE` reads.

`show` prints what each turn was asked and what it answered, not what the agent
did along the way; [`vincent chat transcript`](#vincent-chat-transcript) prints
that.

### `vincent chat transcript`

```sh
vincent chat transcript CHAT_ID [--turn N] [-f | --follow] [--json | --raw]
```

Prints one turn's transcript — the complete record of what the agent did while
answering it. It is [`vincent task transcript`](#vincent-task-transcript) for a
chat, and reads the same way.

`--turn` takes the turn number `chat show` prints as `--- turn N (STATE) ---`. Omitted,
it selects the running turn if there is one, and otherwise the newest turn. A
turn number the chat does not have exits `1` with
`Error: turn N not found on chat M`, and a chat with no turns yet exits `1` with
`Error: chat M has no turns yet`.

| Output | What it is |
|---|---|
| default | The records rendered as text, exactly as `task transcript` renders an attempt: the run header as a first `# ` line, assistant output, tool calls and their outcomes, the skills that ran as `> skill <name> <args>` lines, a subagent's work behind a `| ` rail, the agent's running to-do list as a `# plan:` line, vincent's own annotations, and a closing `= done` line carrying whatever the agent reported about the turn |
| `--json` | The normalized records as NDJSON, one JSON object per line, in vincent's vocabulary including its `vincent.*` annotations. This is the `jq` route |
| `--raw` | The agent's own JSONL, byte for byte, exactly as it was recorded |

As on `task transcript`, what an agent's command printed and the hunks of its
edits are the two records the default rendering drops; `--raw` and `--json` both
carry them. Everything a reader
reads goes to **stdout**, and the command's own diagnostics go to stderr.

`-f` (`--follow`) opens on the tail and then resumes from the record boundary
the daemon reports, printing records as the turn writes them. It ends when that
turn stops running, with `turn N is done` (or `failed`, or `interrupted`) on
stderr. A turn waiting on an answer is still running, so the follow keeps
going; it does not wait for a later `chat send`, which is a different turn.

A turn that failed before its transcript was opened — `agent_unavailable` or
`transcript_io_error` — prints `turn N (REASON) has no transcript` on stderr and
exits `0`; the reason is the whole answer. A transcript whose file is gone
(pruned by `transcript_retention_days`, or deleted) exits `1`.

### `vincent chat skills`

```sh
vincent chat skills CHAT_ID [--refresh] [--json]
```

The skills the chat's **agent CLI** would load in the chat's directory — its
own worktree, or its linked task's — and the exact text that invokes one. Not
to be confused with [`vincent skills`](#vincent-skills), which is about the
skills *vincent publishes* for you to install into your agents; these are the
agent's own, discovered by asking its CLI.

| Column | What it is |
|---|---|
| `SKILL` | The name the CLI reported, verbatim. Names may repeat: two skills can share one |
| `INVOKE` | The exact text to put in a message, built by the daemon's adapter. This is why it is a column and not something you assemble — codex disambiguates a duplicated name as `[$name](path)`, so two rows with one name differ only here |
| `ARGS` | The CLI's own argument hint, blank when it gave none |
| `DESCRIPTION` | The CLI's own description, blank when it gave none |

Every cell is the agent CLI's own word. vincent normalizes nothing, invents no
scope, and puts no `-` in a cell the CLI left empty. `scope`, `plugin`, `path`
and `aliases` are carried by `--json` only — claude reports no scope at all, so
a column for it would be blank for every claude chat.

**stdout is the table and nothing else.** Everything else goes to stderr, so
`vincent chat skills 12 | wc -l` counts skills:

- The invocation line, for example
  `invoke: vincent chat send 12 '$NAME your message'`, followed by its quoting
  note. Both are built from the sigil and position the daemon reported, not
  from the agent's name, and it prints whenever the agent can invoke a skill —
  including for an agent that can invoke one but cannot list them, and for an
  empty list. See [Quoting a skill invocation](#quoting-a-skill-invocation).
- `no skill list: REASON` when the agent cannot list them, and
  `skill list unknown: REASON` when nobody can say — a probe that failed, an
  adapter that is no longer registered, or a chat whose task runs in a
  container. Under either there is no table at all.
- `warning: PATH: MESSAGE` for each entry the CLI found and could not load.

The answer comes from the daemon's per-directory cache; `--refresh` asks the
CLI again first. `--json` emits the response object unchanged, with `skills`
and `problems` always arrays.

Exit `0` whenever the daemon answered, whatever the verdicts say — an agent
that cannot list its skills is the normal state of a healthy machine, and an
exit code that fires on the normal state is no use in a script. Exit `1` on an
unknown chat, a terminal one (it has no next turn, so no directory to list
for), or a linked chat whose task no longer has a worktree. Exit `2` with no
daemon.

### `vincent chat archive`

```sh
vincent chat archive CHAT_ID [--force] [--json]
```

Removes the chat's worktree and, under `delete_empty_branch_on_archive`, an
empty branch with it — the same archive a task gets. A worktree with local
changes is refused; `--force` is the way past it.

A chat opened on a task is refused with `chat_linked_to_task`: the worktree
is the task's. [`vincent chat close`](#vincent-chat-close) is how that chat
ends.

### `vincent chat delete`

```sh
vincent chat delete CHAT_ID... [--branch] [--json]
vincent chat delete --before <date|duration> [--branch] [--json]
```

Aliased as `vincent chat rm`. `vincent task delete` for a chat: permanently
deletes an **archived** or **closed** chat — the row, its turns and its
transcript directory.

`--branch` on a chat opened on a task is refused (`chat_linked_to_task`): the
branch is the task's. A `--before --branch` sweep does not skip those rows, so
each one is reported as an error and the sweep exits 1; sweep without
`--branch` to remove them.

A `handed_off` chat is refused (`details.reason: "handed_off"`). The task it
was handed to owns the worktree and the branch, so that task is what to delete;
a `--before` sweep skips handed-off rows rather than collecting a refusal it
can see coming.

`--before` measures from when the chat *ended*, which for a terminal chat is
its `updated_at` — chats have no `archived_at` column, because the transition
into a terminal state is the last write the row takes.

### `vincent chat handoff`

```sh
vincent chat handoff CHAT_ID --title TITLE [--workflow NAME] [--description TEXT]
                     [--field name=value ...] [--fields-file FILE] [--priority N]
                     [--agent NAME] [--model NAME] [--effort LEVEL] [--json]
```

Creates a task that adopts the chat's worktree, branch, base branch and base
SHA exactly as they are, and leaves the chat terminal (`handed_off`) and linked
to the task. It prints the task and the workspace it inherited; `--json` emits
`{"task": …, "chat": …}`, the created task and the chat as it now stands.
Nothing is copied, renamed or committed: committed *and* uncommitted work are
both there when the task's first step runs, because the directory is simply not
touched.

The flags are `vincent task add`'s, minus the ones a handoff has no say in: the
project, the base branch and the branch name all come from the chat, and the two
GitHub prefills (`--github-issue`, `--github-pull`) are not offered.
`--description` is where the conversation's context goes: nothing about the
chat reaches the workflow's prompts automatically.

Only an idle chat can be handed off. A live turn must be finished or cancelled
first (409), a worktree in the middle of a merge, rebase, cherry-pick, revert
or bisect is refused by name (409, code `repo_operation_in_progress`), and a
chat that has already been handed off or archived is refused for the same
reason (409). Ordinary uncommitted changes are **not** a refusal. From then on
the task owns the worktree and the branch: `vincent chat archive` is not legal
on a handed-off chat, so chat cleanup can never remove task-owned state.

A chat opened on a task is refused with `chat_linked_to_task`: its worktree and
branch already belong to that task.

### `vincent chat close`

```sh
vincent chat close CHAT_ID [--json]
```

Ends a chat opened with [`vincent task chat`](#vincent-task-chat). A live turn
is cancelled first; the chat becomes `closed`, a terminal state, and the task's
lock lifts, so its actions come back. It prints `chat N closed; task M is
unlocked`; `--json` emits the chat as it now stands.

Nothing on disk is touched: the worktree and branch are the task's, and they
stay exactly as the chat left them — which is the point of talking to an agent
there. Retry, approve or archive the task afterwards as you would have.

A chat started with `vincent chat start` cannot be closed — archive it — and
neither can a chat that is already closed; both exit 1. A closed chat is hidden
from `vincent chat list` unless you pass `--archived`. `vincent chat delete`
removes a closed chat's row and transcripts like an archived one's, but refuses
`--branch` on it (`chat_linked_to_task`): the branch is the task's.

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
| `--from <example>` | Start from a [shipped example](../../examples) instead of the skeleton: `converge`, `cursor-review`, `docs-update`, `feature-pr`, `fix-and-test`, `go-checks`, `ship`, `split-work`. They are embedded in the binary, so this works from any directory |
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
vincent workflow ls --global [--json]
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

**`--global` reads `{config_dir}/workflows/` directly and needs no daemon.** It
prints one absolute path per line and exits `1` when there are no global
workflow files, including when the directory does not exist. It is the
inventory of a global [`update-workflows`](../guides/workflows.md) run. With
`--json` it prints each file's `file`, `name`, `version` (the token
`GET /v1/workflows` reports and a PATCH checks), `valid` and `errors`, which
is what a proposal's manifest records for
[`vincent workflow apply`](#vincent-workflow-apply). A file that does not
parse is still listed, so it can be repaired. A file that is not a regular
file, or is over 1 MiB, is reported on stderr and left out. `--global` with
`--project` is a usage error.

### `vincent workflow apply`

```sh
vincent workflow apply --proposal <task_id> [--check]
```

Installs a proposal to change the global workflows into
`{config_dir}/workflows/`. A global `update-workflows` run stages the proposal
and ends with this command, after a person approves it at the run's manual
gate. It needs no daemon. The registry reloads the global scope when a file is
written, and the change reaches every project at once.

A proposal is a directory, `{data_dir}/workflow-proposals/<task_id>/`, holding
the whole proposed `*.yaml`/`*.yml` files, each named by the live file's base
name, and a `manifest.json`. The manifest maps each staged file's base name to
the `version` that `vincent workflow ls --global --json` reported for it, or
to `"absent"` for a new file:

```json
{ "feature-pr.yaml": "<version from ls --global --json>", "shared-checks.yaml": "absent" }
```

Apply refuses the whole proposal, writes nothing, and names every offending
file, when:

- a staged file does not validate (the same verdict as
  [`vincent workflow validate`](#vincent-workflow-validate));
- a staged file has no manifest entry, a manifest entry has no staged file, or
  the directory holds anything else;
- a file changed since its version was recorded, a file recorded `"absent"`
  now exists, or a recorded file is gone;
- a staged name is not a bare file name, or a new file is not named
  `<name>.yaml` after its `name:`;
- a staged file **renames** its workflow: its `name:` differs from the live
  file's. A rename would break every task, `include` and trigger that names the
  workflow;
- a staged `name:` is already declared by another global file the proposal
  does not replace, or by another staged file.

Every check runs before any write. Each file is written atomically; an
existing file keeps its mode and a new one is created `0644`. `wrote <path>`
is printed per file, then the staging directory is removed. A failure part-way
through is reported with what was written and is not rolled back. An empty
manifest installs nothing and succeeds.

`--check` runs every check, writes and removes nothing, and prints the staged
files' absolute paths, one per line, sorted. It is the global run's relist, so
a stale or malformed proposal blocks before anyone is asked to approve it.

Exit `0` when the proposal was installed or checked; `1` when it was refused,
nothing is staged at that path, or a write failed.

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

It checks that each template **parses**. It never runs one — see
[`vincent workflow render`](#vincent-workflow-render) for that.

### `vincent workflow render`

```sh
vincent workflow render <file> [--task ID] [--project ID]
                               [--title S] [--description S] [--field k=v]...
                               [--agent A] [--model M] [--effort E] [--json]
```

Executes every template the file declares — `prompt`, `run`, `check`,
`instructions`, `if` and `for_each` — and prints what each step would send,
with the [agent/model/effort triple](workflows.md) it resolves to and the level
that supplied each field.

`validate` parses a template; this **runs** it. That is the difference that
matters, because templates render with `missingkey=error`: `{{.Task.Titel}}`,
`{{.Task.Fields.ticket}}` on a task that sets no `ticket`, and
`{{.Steps.plan.Reslt}}` all validate cleanly and fail at run time. Before this
command the only way to find out was to create a task and watch a step fail.

```
$ vincent workflow render .vincent/workflows/review.yaml
steps[0] plan (agent)
  agent: claude (adapter)  model: sonnet (workflow)  effort: - (adapter)
  prompt:
    Review <task.title> on branch <branch>
steps[1] verify (command)
  run:
    go test ./...
.vincent/workflows/review.yaml: ok — review, 2 step(s) rendered, 0 warning(s)
```

**It needs no daemon**, so it belongs in the same pre-commit hook as
`validate`. Values a run discovers — the worktree, a previous step's result, a
previous attempt's failure — bind to visible placeholders such as `<worktree>`
and `<steps.plan.result>`, so the output reads as a preview and never as the
literal prompt an agent will receive. A field a workflow declares
[`required`](workflow-schema.md) binds too, because a real task is guaranteed
to carry it: to its [`default:`](workflow-schema.md#default) where it has one,
else an [`enum`](workflow-schema.md#enum-fields)'s first declared value, else
the `<field.NAME>` placeholder. An optional or undeclared field stays absent, so
reading one without `{{ with index .Task.Fields "x" }}` is reported — which is
exactly the bug a real run would hit.

Supply the rest yourself: `--title`, `--description`, repeated `--field k=v`,
and `--agent`/`--model`/`--effort` for a task-level override. `--task ID` binds
a real task's title, description, fields, branch and override triple instead,
and `--project ID` binds that project's facts; both need a daemon, and both
also resolve `include` steps and named fan-out lanes through the registry.
Without one, those steps are reported as unresolved and every other step still
renders.

A derived fan-out's `lane:` template renders once, not once per item: its
steps are marked `<derived lane>` with the `for_each` they expand over
(`derived_lane` in `--json`), and its own `if`, `id`, `needs` and `fields` bind
each `.Item` key they read to a placeholder such as `<item.id>`.

A `fan_out` step's own row draws its lane graph. A `lanes:` block has one line
per wave, in wave order. Each line names that wave's lanes in declaration order.
A lane that [`needs:`](workflow-schema.md) others says which, and a lane with an
`if:` is tagged `guarded`, because a lane its guard skips orders nothing. Waves
are numbered from 1, the same way the engine derives them, and they cover every
declared lane: whether a guard holds is decided at run time, not here.
`schedule: eager` is shown only when the step declares it. On a list where no
lane needs another, the line also says the step runs as barrier. A lane that
names a registry workflow keeps its id and edges in the parent's block even
offline, and a `fan_out` nested inside a lane draws its own block on its own
row. The step rows and the step count below the block are unchanged.

```
steps[1] spread (fan_out)
  schedule: eager
  lanes:
    wave 1: api, db (guarded)
    wave 2: wire (needs api, db)
```

A `lane:` template has no width until it runs, so its block is one line and
draws no waves. `at most N` appears only when `max_lanes:` is set:

```
steps[1] build (fan_out)
  schedule: eager
  lanes:
    <derived lane>: unknown width, at most 8, one per item of {{ .Steps.plan.Result }}
```

`--json` adds the same graph to that step only: `schedule` (the resolved mode,
so `barrier` when none is named), `max_lanes` when set, and `lanes`, one
`{id, needs, wave, guarded}` per lane. A `lane:` template yields a single
`{"id": "<derived lane>", "derived": true, "for_each": [...]}` with no `wave`.

Exit `0` clean, `1` a template that does not execute, `2` no daemon answered a
`--task`/`--project`. A guard that renders to something other than `true`/`false`
is a warning, not a failure: a preview placeholder can legitimately make one
non-boolean.

`.Host` is the machine running the command, not a remote daemon — the only
honest answer offline, and the one place a preview and a real run can differ.

## `vincent trigger`

Event triggers turn outside events into tasks. You create and edit them in the
[TUI](../guides/tui.md), by hand under `{config_dir}/triggers/`, or with the
`create-trigger` and `update-triggers`
[built-in workflows](../guides/triggers.md#letting-an-agent-write-triggers),
and turn them on with `triggers.enabled` in [`config.yaml`](configuration.md).
`validate` and `ls` read the files directly and need no daemon. `apply` installs
a proposal those built-ins staged, and never arms anything. `test` is the dry
run, and needs a daemon.

### `vincent trigger validate`

```sh
vincent trigger validate <file> [--json]
```

Checks one trigger file without a daemon. The verdict is the one
`POST /v1/triggers/validate` gives, plus one more check: the file's `id:` must
equal its name without `.yaml`, which is what the registry requires of a file
under `{config_dir}/triggers/`. The file does not have to be in that directory,
but its name must end in `.yaml`, the only files the registry loads.

```
$ vincent trigger validate label-to-task.yaml
label-to-task.yaml: ok — trigger label-to-task
```

An invalid file prints one `  error: line LINE: PATH: MESSAGE` line per error
on stderr, then `FILE: invalid (N error(s))`. `--json` prints one object, with
`id` only when the file is valid:

```json
{ "file": "label-to-task.yaml", "valid": false,
  "errors": [ { "path": "source.project", "line": 4, "message": "…" } ] }
```

Exit `0` valid, `1` invalid or unreadable, as for
[`workflow validate`](#vincent-workflow-validate).

### `vincent trigger ls`

```sh
vincent trigger ls --project <id> [--json]
```

Reads `{config_dir}/triggers/*.yaml` without a daemon and prints the path of
every file whose `source.project` is `<id>`, one per line. `<id>` is the
project's numeric id, the one `vincent project ls --json` reports.

A file that does not validate is still listed when its `source.project` can be
read, so a broken trigger for the project can be found and repaired. A file
whose project cannot be read at all is reported on stderr and left out.

`--json` prints an array with one object per listed file:

| Field | Value |
|---|---|
| `file` | The file's path |
| `id` | The trigger id |
| `project` | `source.project` |
| `version` | The file's version token, which [`apply`](#vincent-trigger-apply) compares |
| `valid` | Whether the file validates |
| `enabled` | The file's `enabled` |
| `on_fire` | The file's `on_fire`, or `""` when it leaves the key out |
| `permission` | The file's `permission`, or `""` when it leaves the key out |
| `errors` | Why the file does not validate |

Exit `0` when at least one file matched, `1` when none did, with or without
`--json`. That makes it a probe: a workflow step can branch on whether a
project has any triggers at all.

### `vincent trigger apply`

```sh
vincent trigger apply --proposal <task_id> --project <id>
```

Installs a trigger proposal into `{config_dir}/triggers/` **without arming
anything**. It is the step the `create-trigger` and `update-triggers` built-ins
end with, and the only way either one writes a trigger.

A proposal is a directory, `{data_dir}/trigger-proposals/<task_id>/`, holding
the full proposed `<id>.yaml` files and a `manifest.json`. The manifest maps
each trigger id to the `version` that `vincent trigger ls --json` reported for
the file it replaces, or to `"absent"` for a new file:

```json
{ "label-to-task": "absent", "ci-failure-follow-up": "<version from ls --json>" }
```

Apply refuses the whole proposal, writes nothing, and names every offending
file and key, when:

- a staged file does not validate;
- a staged file's `source.project` is not `--project`;
- a staged file has no manifest entry, or a manifest entry has no staged file;
- a file changed since its version was recorded, a file recorded `"absent"`
  now exists, or a recorded file is gone;
- a file **arms** a trigger compared with the file on disk, where a new file
  compares against no file: `enabled` from `false` or absent to `true`,
  `on_fire` from absent or `propose` to `create`, or `permission` from absent
  or `restricted` to `workflow`.

A value that is already armed may stay, and disarming is always allowed. There
is no flag to override any of this. Arm a trigger yourself, in the TUI's
triggers view, which asks first, or in your editor. Apply never touches
`triggers.enabled` in `config.yaml`.

Each file is written `0600`, and `wrote <path>` is printed for it. Once every
file is written, the proposal directory is removed.

`removed <dir>` follows. A proposal with an empty manifest and no staged file
installs nothing and is removed the same way: that is `update-triggers` finding
every trigger already right.

Exit `0` installed, `1` refused, nothing staged at that path, or a write failed.

### `vincent trigger test`

```sh
vincent trigger test <id> --event FILE [--json]
```

Sends one sample event to `POST /v1/triggers/{id}/test` and prints what the
trigger would do with it. The stages print in pipeline order: the `match:`
result, the `if:` verdict, the rendered dedupe key and whether the ledger
already holds it, the request the action would replay, and — for a trigger that
sets [`overrun:`](../guides/triggers.md#overrun-what-happens-while-the-last-run-is-still-going)
— the group it named and the tasks it found in flight there. A stage the event
never reached prints `-`.

```
$ vincent trigger test triage --event bug.json
trigger triage, event bug-1
  match:    matched
  if:       true
  dedupe:   bug-1 (new)
  action:   create_task POST /v1/tasks
  body:     {
              "project_id": 1,
              "title": "Triage Crash",
              "paused": true,
              "restricted": true
            }
  outcome:  fired: the action would be replayed
```

`FILE` holds one JSON object, the event as the trigger's templates see it under
`.Event`. `--event -` reads it from stdin. An outcome of `fired` means the
action **would** be replayed. Nothing is sent and nothing is written: no task,
no ledger row, no cursor and no event. The command works while the trigger is
disabled or `triggers.enabled` is off, because it fires nothing.

Unlike [`workflow render`](#vincent-workflow-render), it needs a daemon.
Whether an event would be deduplicated depends on the daemon's delivery ledger.

Exit `0` when the event was judged, whatever the outcome. Exit `1` when the
file is not one JSON object, when the daemon refused the request (an unknown id,
or a trigger file that does not validate), or when the outcome is `error`
because a template did not render. Exit `2` when no daemon answered.

## `vincent github`

A project's GitHub issues and pull requests, a task's link to its pull request,
and the actions that open a task's pull request and act on it. `issues`, `prs`,
`status`, [`pr show`](#vincent-github-pr-show) and
[`pr checks`](#vincent-github-pr-checks) are read-only.
[`pr link`](#vincent-github-pr-link) and
[`pr unlink`](#vincent-github-pr-unlink) write only vincent's own record of the
link, and send nothing to GitHub. [`pr create`](#vincent-github-pr-create) and
the `pr merge`, `close`, `reopen`, `comment` and `rerun` commands below it write
to GitHub, and each writes only when you run it. The daemon makes every call — a
client never talks to GitHub. All of these need a daemon.

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

### `vincent github prs`

```sh
vincent github prs --project ID [--state open|closed|all] [--limit N] [--json]
```

Lists the project's pull requests, newest first, and names the task each one is
linked to. `--state` defaults to `open`.

```
PR     STATE  TITLE                                        BRANCH                            TASK
#412   open   List a GitHub project's open pull requests   vincent/231-list-open-pull-reque…  #61
#401   draft  Rework the board header                      vincent/9-rework-the-board-header  -
```

`STATE` is `open`, `draft`, `closed` or `merged`; the listing defaults to open,
so `closed` and `merged` need `--state closed` or `--state all` — or a task's
own link, which reads live whatever the listing was asked for.
`TASK` is the board task this pull request is linked to. The daemon makes that
link in the background every
[`github.poll_interval`](configuration.md#github), matching a pull request's
head branch against a task's own branch; a link made or removed by hand with
[`pr link`](#vincent-github-pr-link) or [`pr unlink`](#vincent-github-pr-unlink)
wins over it.

### `vincent github pr create`

```sh
vincent github pr create --task ID --title TITLE [--body TEXT] [--draft] [--json]
```

Pushes the task's branch to `origin` and opens its pull request. It and the
`pr merge`, `close`, `reopen`, `comment` and `rerun` commands below are the only
commands under `vincent github` that write to GitHub, and each acts only on the
task you name, only when you run it.

```
$ vincent github pr create --task 61 --title "List a project's open pull requests" --draft
Pushed vincent/61-list-open-pull-requests to origin.
Created octo/repo#412 (draft)
https://github.com/octo/repo/pull/412
```

Only **committed** work is pushed: anything uncommitted in the task's worktree
is not in the pull request. The push never forces — a diverged, protected or
rejected push creates no pull request, changes nothing on the remote, and
fails with a named reason (`push_rejected`, `push_no_credential`,
`push_failed`).

When the branch pushes but the pull request cannot be created — a credential
with no write scope, say — this is **not** an error. It prints the compare URL
instead and exits 0, because the branch is on the remote and GitHub's own page
now works:

```
$ vincent github pr create --task 61 --title "List a project's open pull requests"
Pushed vincent/61-list-open-pull-requests to origin.
vincent could not create the pull request (no_write_scope).
Open this instead:
https://github.com/octo/repo/compare/main...vincent%2F61-list-open-pull-requests?expand=1&title=…
```

A task that already has a linked pull request is refused: unlink it first. The
same action is `P` in the TUI, in the task workspace and on the Pull Requests
takeover.

### `vincent github pr link`

```sh
vincent github pr link NUMBER --task ID [--json]
```

Links pull request `NUMBER` to a task by hand — for a pull request opened from
a branch vincent did not create, or one the head-branch matching got wrong.

```
$ vincent github pr link 412 --task 61
Linked task 61 to octo/repo#412.
```

Only vincent's own record is written. Nothing is sent to GitHub, and the number
is not checked there: a wrong one shows up as not found the next time it is
read, with [`pr show`](#vincent-github-pr-show). A link made by hand wins over
the daemon's matching and clears an earlier unlink. `--json` prints the task.

Exit `1` when `NUMBER` is not a positive integer (refused before anything is
sent), or when the daemon refused — a project whose `origin` is not a
github.com URL has no repository to link to. Exit `2` when no daemon answered.
The same action is on the TUI's Pull Requests takeover.

### `vincent github pr unlink`

```sh
vincent github pr unlink --task ID [--json]
```

Removes a task's pull-request link, and keeps it removed.

```
$ vincent github pr unlink --task 61
Unlinked octo/repo#412 from task 61.
vincent will not link it again on its own; `vincent github pr link` restores it.
```

The unlink is **sticky**: the daemon's head-branch matching will not link that
pull request to the task again. Nothing is sent to GitHub. `--json` prints the
task, whose `github_pull` is now `suppressed` with its repo and number kept.

A task with no live link — never linked, or already unlinked — is refused with
exit `1` before the unlink is sent, and nothing about it changes. Exit `2` when
no daemon answered. The same action is `u` on the TUI's Pull Request tab and on
the Pull Requests takeover.

### `vincent github pr show`

```sh
vincent github pr show --task ID [--json]
```

A task's linked pull request, read live from GitHub on every call — so a pull
request that has since merged or closed says so.

```
$ vincent github pr show --task 61
octo/repo#412: List a GitHub project's open pull requests
state      open
branch     vincent/61-list-open-pull-requests → main
linked by  auto
url        https://github.com/octo/repo/pull/412
```

`state` is `open`, `draft`, `closed` or `merged`; `linked by` is `auto` for the
daemon's head-branch match and `human` for a link made by hand.

Exit `0` when the pull request was read. Exit `1` when the task has no live
link — it then prints the GitHub page that would open one, when there is one —
or when GitHub could not be read, with the reason (the integration switched
off, no credential, the pull request not found). Exit `2` when no daemon
answered. `--json` prints the answer as the daemon gave it, with `linked` and
any `reason`, and exits by the same rule.

### `vincent github pr checks`

```sh
vincent github pr checks --task ID [--json]
```

The CI checks on the head commit of a task's linked pull request, read live
from GitHub on every call and never cached.

```
$ vincent github pr checks --task 61
octo/repo#412: failure on d3adb33fd3ad
CHECK              STATE        RUN   URL
build              failure      5150  https://github.com/octo/repo/actions/runs/5150/job/71
test               in_progress  5150  https://github.com/octo/repo/actions/runs/5150/job/72
license/cla        success      -     https://cla.example.test/octo/repo/pull/412
ci/legacy-builder  success      -     https://legacy.example.test/build/9
```

The first line is the whole commit's state — `failure` if anything failed,
`in_progress` while anything is still running, `success` when everything that
finished passed. `RUN` is the GitHub Actions run behind a check, and `-` for a
third-party check or a legacy commit status. A pull request with no checks
prints one line saying none are reported on its head commit.

Exit `0` when the checks were read, **whatever CI concluded**: read the verdict
from `--json`'s `.state`. Exit `1` when the task has no live link or GitHub
could not be read, with the reason. Exit `2` when no daemon answered. `--json`
prints the answer as the daemon gave it, with `linked` and any `reason`, and
exits by the same rule. The same rows are the TUI's Pull Request tab.

### `vincent github pr merge`

```sh
vincent github pr merge --task ID --method merge|squash|rebase --head-sha SHA [--json]
```

Merges the task's linked pull request. There is no confirmation prompt, so both
`--method` and `--head-sha` are required: they are where you name exactly what
is sent. There is no default method and no config key for one.

```
$ vincent github pr merge --task 61 --method squash --head-sha 3f9c2e1d…
Merged octo/repo#412 (merged)
https://github.com/octo/repo/pull/412
```

The daemon reads the pull request first and refuses — merging nothing — when
its head is no longer `--head-sha` (`head_changed`), a check is still running
(`checks_running`), the branch is behind its base (`branch_behind`), or GitHub
would not merge it as it stands (`not_mergeable`: a conflict, a draft, a
required review, or a pull request already closed or merged). The merge itself
is pinned to `--head-sha`, so a push landing after that read is refused too. It
never deletes the branch and never uses an admin override.

### `vincent github pr close` and `vincent github pr reopen`

```sh
vincent github pr close --task ID [--json]
vincent github pr reopen --task ID [--json]
```

Closes the task's linked pull request without merging it, or reopens a closed
one, and prints the pull request as it reads afterwards.

### `vincent github pr comment`

```sh
vincent github pr comment --task ID (--body TEXT | --body-file PATH) [--json]
```

Posts a comment on the task's linked pull request and prints its URL.
`--body-file -` reads the comment from stdin. An empty comment is refused. Run
it twice and it comments twice.

### `vincent github pr rerun`

```sh
vincent github pr rerun --task ID --run-id ID [--json]
```

Re-runs the failed jobs of one GitHub Actions run. The run must be behind a
**failed**, Actions-backed check on the pull request's current head, as the
daemon's live [check rollup](api.md#github-pull-requests) reads it; any other
run id is refused (`bad_request`) and nothing is sent. A run id below 1 is
refused (`validation_failed`) before GitHub is asked at all.

Every one of those five commands needs a linked pull request — a task with none,
or whose link was removed, is refused with `pull_not_linked` — and a credential
that may write: a 403 from GitHub is `no_write_scope`. None of the five is an
MCP tool, so an agent running in a step cannot reach them.

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

The `issues` row covers pull requests too: they are read through the same
credential and the same gate.

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
