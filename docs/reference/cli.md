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
- [`vincent config`](#vincent-config)
- [`vincent status`](#vincent-status)
- [`vincent update`](#vincent-update)
- [`vincent chat`](#vincent-chat)
- [`vincent workflow`](#vincent-workflow)
- [`vincent github`](#vincent-github)
- [`vincent gc`](#vincent-gc)

---

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | The request was rejected — the daemon answered no (bad id, invalid state transition), a daemon-free command refused it (`workflow validate` on an invalid file, `workflow render` on a template that does not execute, `workflow init` on a name already taken), or the client refused the input before sending anything (a `--fields-file` that is not one JSON object of strings) |
| `2` | No daemon answered |

`vincent daemon status` overloads them usefully: `0` healthy, `1` not running,
`2` running but unresponsive. `vincent doctor` follows the same shape: `0`
healthy, `1` problems found, `2` no daemon answered. So does
[`vincent update`](#vincent-update) — see its own table.

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

One report answering "why is nothing running?". Ten groups:

| Group | Rows |
|---|---|
| Paths | config dir, data dir, config file, whether it parses, and any config path readable beyond its owner |
| Daemon | running / not running / unresponsive, pid, port, version, uptime |
| Log | daemon log path, size, mtime, and the last 20 lines |
| Database | path, size, total on disk including WAL/SHM, applied schema version, `PRAGMA integrity_check`, per-table row counts, workflow-snapshot bytes, and how far back the events table reaches |
| Agents | per adapter: found, path, version, `logged_in`, whether the build is one vincent has been tested against, and whether the adapter can restrict on this OS |
| GitHub | whether [`github.enabled`](configuration.md#github) is on, whether `gh` is installed and logged in, whether a token variable is set, and whether issues are readable |
| Container | whether [`container.image`](configuration.md#container) names an image, which image, whether the configured runtime answered, and whether steps run in it or on this host |
| Update | whether [`update.check`](configuration.md#update) is on, the latest stable release and when it was last seen, this binary's version, and whether the running daemon is older than it |
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

If the binary you just ran is newer than the daemon that answered — which is
what you have right after [`vincent update`](#vincent-update) — it says so and
tells you to restart it. That is a line, not a failure: the running daemon keeps
its old code until it is restarted, and everything keeps working meanwhile.

### `vincent daemon logs`

```sh
vincent daemon logs [-n N] [-f]
```

Prints the tail of `{data_dir}/logs/daemon.log` — 500 lines by default, the same
window the TUI's daemon view shows. `-f` keeps printing lines as they are
appended, on a two-second cadence, until Ctrl-C.

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
| `--default-branch` | Detected **once, here**: `origin/HEAD`, then local `main`, then `master`, then the current branch. The name is stored and never re-detected; what is refreshed later is the branch's content, per task, when `fetch_base_branch` is on |
| `--workflow` | None — tasks then name their own |
| `--max-parallel` | Unset — only the global cap applies |

Registration is refused if no default branch can be determined (a detached or
unborn HEAD); pass `--default-branch` explicitly.

### `vincent project ls`

```sh
vincent project ls [--json]
```

Lists registered projects with their ids, paths and defaults.

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
                 [--json]
```

Creates a task. It is `queued` immediately — there is no draft state.

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
vincent task show <id> [--json]
```

Shows one task with its step runs, the actions valid right now, and any pending
input request.

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

### `vincent task transcript`

```sh
vincent task transcript <id> [--step RUN] [-f] [--json | --raw]
```

Prints one attempt's transcript — the complete record of what it did, which
`task show` only names the file of.

`--step` takes a **step_run id**: the `RUN` column `task show` prints, which is
unambiguous across retries where every attempt is its own run. Omitted, it
selects the running attempt if there is one, and otherwise the newest attempt by
run id (creation order — which, unlike the step order, stays chronological when a
task has parallel steps or fan-out lanes).

| Output | What it is |
|---|---|
| default | The records rendered as text, the vocabulary the TUI's output pane renders: assistant output, tool calls and their outcomes, command output, vincent's own annotations. Token usage is dropped — `task show` carries it |
| `--json` | The normalized records as NDJSON, one JSON object per line, in vincent's vocabulary including its `vincent.*` annotations. This is the `jq` route |
| `--raw` | The agent's own JSONL, byte for byte, exactly as it was recorded |

Everything a reader reads goes to **stdout**, including a command step's stderr,
which is tagged `[stderr]` rather than split onto the other file descriptor: a
transcript is one interleaved stream and two descriptors would scramble the
ordering that makes it readable. The command's own diagnostics go to stderr, so
stdout stays pipeable.

`-f` opens on the tail and then resumes from the record boundary the daemon
reports, printing records as the attempt writes them. It ends when that attempt
stops running — it does not wait for a later retry, which is a different run.

A step run that never had a transcript — a manual gate — prints a line saying so
on stderr and exits `0`; nothing failed. A transcript whose file is gone (pruned
by `transcript_retention_days`, or deleted) exits `1`.

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
                        [--run CMD | --run-file FILE] [--json]
```

Re-runs the step the task blocked on. Valid from `blocked`. With no flags it is
a plain retry; the flags change what gets re-run, and each one is a different
kind of recovery:

| Flag | What it does |
|---|---|
| `--branch NAME` | Renames the task's branch **before** the retry re-admits it. This is the [`branch_exists`](task-lifecycle.md) recovery: the task, its id and its transcripts all survive, which deleting and re-creating it would not. Refused on a task created from a pull request |
| `--prompt` / `--run` | Edit+retry. The text replaces that step's prompt or command in **this task's** workflow snapshot, and in no other task's — the registry file is untouched |
| `--prompt-file` / `--run-file` | The same, read from a file, or from stdin with `-`. A replacement prompt is usually several lines, which argv is a poor place for |

`--prompt` with `--prompt-file` (or `--run` with `--run-file`) is a usage error:
they are two spellings of one value, so it exits 1 before any request is sent.

```sh
vincent task retry 7 --prompt-file - <<'EOF'
The check failed because the fixture path is wrong on Windows.
Use filepath.Join rather than a slash-separated literal.
EOF
```

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
  the notify hook is switched off from the command line.
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

## `vincent chat`

Chats are conversations with an agent (spec §5.5). Each gets its own git
worktree and `vincent/{id}-{slug}` branch, exactly as a task does, and every
turn resumes the agent's own session — so turn N sees turns 1..N-1.

Chats are not tasks. They never appear on the board, they run no workflow, and
a chat turn never waits for a scheduler slot: it starts when you send it, or it
is refused because `max_parallel_chats` chats are already running.

Only an agent CLI that can resume its own session can hold a chat. Today that
is `claude`; `codex` and `cursor` are refused at creation with
`agent_cannot_resume` rather than having the conversation faked by replaying
the log as prompt context.

### `vincent chat start`

```sh
vincent chat start TITLE --project ID [--agent NAME] [--model M] [--effort E]
                  [--base BRANCH] [--message TEXT] [--json]
```

Starts a chat and prints its id, agent and branch. `--agent` defaults to the
first installed adapter that can resume a session. `--message` sends a first
turn straight away and waits for it, which is `start` plus `send` in one call.

### `vincent chat send`

```sh
vincent chat send CHAT_ID MESSAGE [--json]
```

Sends a message and blocks until the turn ends, then prints the agent's answer
on stdout. A failed turn prints its reason on stderr and exits 1 — including
`session_lost`, which means the agent CLI no longer knows the session this chat
was resuming. The chat stays usable; starting a fresh conversation is a
decision you make, not one vincent makes silently.

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
vincent chat list [--project ID] [--json]
```

One line per chat: id, state, agent, title. `--json` emits the chat objects.

### `vincent chat show`

```sh
vincent chat show CHAT_ID [--json]
```

The chat's header and every turn in order, with each turn's prompt, answer and
— where it failed — its reason. A chat parked in `awaiting_input` prints the
request first, with its questions numbered: those are the numbers
`vincent chat answer --answer N=VALUE` reads.

### `vincent chat archive`

```sh
vincent chat archive CHAT_ID [--force] [--json]
```

Removes the chat's worktree and, under `delete_empty_branch_on_archive`, an
empty branch with it — the same archive a task gets. A worktree with local
changes is refused; `--force` is the way past it.

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

Exit `0` clean, `1` a template that does not execute, `2` no daemon answered a
`--task`/`--project`. A guard that renders to something other than `true`/`false`
is a warning, not a failure: a preview placeholder can legitimately make one
non-boolean.

`.Host` is the machine running the command, not a remote daemon — the only
honest answer offline, and the one place a preview and a real run can differ.

## `vincent github`

A project's GitHub issues and pull requests, and the one action that opens a
pull request for a task. `issues`, `prs` and `status` are read-only;
[`pr create`](#vincent-github-pr-create) is the single command here that writes
to GitHub, and it writes only when you run it. The daemon makes every call — a
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
head branch against a task's own branch; a link made or removed by hand over
[the API](api.md#github-pull-requests) wins over it.

### `vincent github pr create`

```sh
vincent github pr create --task ID --title TITLE [--body TEXT] [--draft] [--json]
```

Pushes the task's branch to `origin` and opens its pull request. This is the
**only** command under `vincent github` that writes to GitHub, and the only
write vincent makes there at all — it never updates, comments on, closes or
merges anything.

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
vincent could not create the pull request (forbidden).
Open this instead:
https://github.com/octo/repo/compare/main...vincent%2F61-list-open-pull-requests?expand=1&title=…
```

A task that already has a linked pull request is refused: unlink it first. The
same action is `P` in the TUI, in the task workspace and on the Pull Requests
takeover.

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
